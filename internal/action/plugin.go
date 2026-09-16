// Package action runs user-installed dashboard-action plugins.
//
// A plugin has two phases. "prepare" runs beside the managed agent and emits
// an opaque JSON payload. "handle" runs beside the dashboard and receives that
// payload together with the agent and host that produced it. Stormlight owns
// only the transport between those phases; editor, workspace, and platform
// policy stays in the executable.
package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

const (
	ProtocolVersion = 1
	maxPayloadBytes = 256 * 1024
	// prepareTimeout bounds the prepare phase. It runs beside the agent,
	// often asking Git about a working tree, so it gets more room than the
	// handler; a plugin still running after this is stuck, not thorough.
	prepareTimeout = 60 * time.Second
	// pipeGrace is how long after a plugin exits — or is stopped — its
	// output pipes are still read before being closed. It exists for the
	// processes a plugin leaves behind holding them: an editor the
	// handler opened, a helper the prepare phase forgot. Their output is
	// not the plugin's, and waiting for them is waiting for the editor
	// to quit.
	pipeGrace = time.Second
)

// Registry finds action plugins in one directory. The default follows the
// same XDG contract as executable workspace resolvers.
type Registry struct {
	directory string
}

// AgentContext is the stable, intentionally small part of an agent that a
// desktop-side plugin may need.
type AgentContext struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cwd  string `json:"cwd"`
}

// HandleInput is written to a plugin's stdin for its handle phase.
type HandleInput struct {
	Protocol int    `json:"protocol"`
	Action   string `json:"action"`
	// Request is the request's id: the key a handler with effects that
	// must not repeat can remember, since a request can be run again
	// after the dashboard running it dies.
	Request string          `json:"request"`
	Host    string          `json:"host,omitempty"`
	Agent   AgentContext    `json:"agent"`
	Payload json.RawMessage `json:"payload"`
}

func NewRegistry() *Registry {
	return NewRegistryAt(actionDirectory())
}

func NewRegistryAt(directory string) *Registry {
	return &Registry{directory: directory}
}

// Prepare runs an action plugin on the agent's machine. Its stdout is the
// opaque JSON payload that crosses to the dashboard.
//
// The payload is read on its own goroutine while the process is waited
// on, because the two can end in either order. A plugin that prints
// forever ends the read first, one byte past the limit, and is stopped
// there; one that closes its stdout and lingers has answered, and is
// stopped a moment later. A plugin that exits but leaves a helper
// holding its stdout ends the wait first; the read is given a moment for
// anything still buffered and then the pipe is closed under the helper,
// which is what stops everything the plugin left behind from stalling
// the agent.
func (r *Registry) Prepare(
	ctx context.Context,
	name, directory string,
	args []string,
) (json.RawMessage, error) {
	path, err := r.plugin(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, append([]string{"prepare"}, args...)...)
	command.Dir = directory
	command.WaitDelay = pipeGrace
	var stderr bytes.Buffer
	command.Stderr = &stderr
	// The pipe is ours rather than StdoutPipe's so that nothing closes
	// its read end but this function: os/exec closes StdoutPipe as soon
	// as the process exits, and a payload written just before that exit
	// is still in the pipe when it does.
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("action plugin %q prepare: %w", name, err)
	}
	command.Stdout = writer
	if err := command.Start(); err != nil {
		reader.Close()
		writer.Close()
		return nil, pluginError(ctx, name, "prepare", err, nil, nil)
	}
	// The child holds the only copy that matters now; keeping this one
	// would keep the read from ever seeing the end.
	writer.Close()
	defer reader.Close()

	type readResult struct {
		payload []byte
		err     error
	}
	readDone := make(chan readResult, 1)
	go func() {
		payload, err := io.ReadAll(io.LimitReader(reader, maxPayloadBytes+1))
		readDone <- readResult{payload: payload, err: err}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	var read readResult
	var waitErr error
	select {
	case read = <-readDone:
		if len(read.payload) > maxPayloadBytes {
			// Closing the pipe is what stops the plugin: its next write
			// is a broken pipe. Cancelling only kills the plugin process
			// itself, and whatever it started inherits the pipe.
			reader.Close()
			cancel()
			<-waitDone
			return nil, fmt.Errorf(
				"action plugin %q payload exceeds the maximum of %d bytes",
				name,
				maxPayloadBytes,
			)
		}
		// The plugin closed its stdout: it has said everything it will.
		// One that is still running a moment later is doing something
		// that is not answering, and its answer does not wait on it.
		select {
		case waitErr = <-waitDone:
		case <-time.After(pipeGrace):
			cancel()
			<-waitDone
		}
	case waitErr = <-waitDone:
		select {
		case read = <-readDone:
		case <-time.After(pipeGrace):
			// The plugin is gone and something it left behind still
			// holds its stdout. A read blocked here has drained
			// everything the plugin wrote, so closing loses nothing of
			// the plugin's; the helper's output was never the payload.
			reader.Close()
			read = <-readDone
		}
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return nil, pluginError(ctx, name, "prepare", waitErr, stderr.Bytes(), nil)
	}
	if read.err != nil && !errors.Is(read.err, os.ErrClosed) {
		return nil, fmt.Errorf("action plugin %q prepare: read payload: %w", name, read.err)
	}
	payload := bytes.TrimSpace(read.payload)
	if len(payload) == 0 {
		return nil, fmt.Errorf("action plugin %q emitted no JSON payload", name)
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("action plugin %q emitted invalid JSON", name)
	}
	return append(json.RawMessage(nil), payload...), nil
}

// Handle runs the matching action plugin on the dashboard's machine.
func (r *Registry) Handle(
	ctx context.Context,
	managedAgent agent.Agent,
	request agent.DashboardActionRequest,
) error {
	path, err := r.plugin(request.Name)
	if err != nil {
		return err
	}
	input, err := json.Marshal(HandleInput{
		Protocol: ProtocolVersion,
		Action:   request.Name,
		Request:  request.ID,
		Host:     managedAgent.Host,
		Agent: AgentContext{
			ID:   managedAgent.ID,
			Name: managedAgent.Name,
			Cwd:  managedAgent.Cwd,
		},
		Payload: append(json.RawMessage(nil), request.Payload...),
	})
	if err != nil {
		return fmt.Errorf("encode action %q handler input: %w", request.Name, err)
	}

	command := exec.CommandContext(ctx, path, "handle")
	command.Dir = filepath.Dir(path)
	command.Stdin = bytes.NewReader(input)
	// A handler's whole job is often to start something else — an
	// editor, a browser — and what it starts inherits its output pipes.
	// Waiting for those to close would be waiting for the editor to
	// quit; the grace period is how long after the handler exits its
	// output is still read before the pipes are closed on it.
	command.WaitDelay = pipeGrace
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return pluginError(
			ctx,
			request.Name,
			"handle",
			err,
			stderr.Bytes(),
			stdout.Bytes(),
		)
	}
	return nil
}

func (r *Registry) plugin(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if r.directory == "" {
		return "", fmt.Errorf("cannot resolve the dashboard actions directory")
	}
	path := filepath.Join(r.directory, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf(
			"action plugin %q is not installed in %s",
			name,
			r.directory,
		)
	}
	if err != nil {
		return "", fmt.Errorf("inspect action plugin %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("action plugin %q is not an executable file", name)
	}
	return path, nil
}

func validateName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("action name %q is invalid", name)
	}
	for index := 0; index < len(name); index++ {
		value := name[index]
		if value >= 'a' && value <= 'z' ||
			value >= '0' && value <= '9' ||
			index > 0 && (value == '-' || value == '_' || value == '.') {
			continue
		}
		return fmt.Errorf(
			"action name %q must use lowercase letters, digits, dots, hyphens, or underscores",
			name,
		)
	}
	return nil
}

func pluginError(
	ctx context.Context,
	name, phase string,
	err error,
	stderr, stdout []byte,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		return fmt.Errorf("action plugin %q %s: %w", name, phase, err)
	}
	return fmt.Errorf("action plugin %q %s: %w: %s", name, phase, err, detail)
}

func actionDirectory() string {
	if configured := os.Getenv("STORMLIGHT_ACTIONS_DIR"); configured != "" {
		return configured
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "stormlight", "actions")
}
