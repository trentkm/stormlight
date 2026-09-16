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
	Protocol int             `json:"protocol"`
	Action   string          `json:"action"`
	Host     string          `json:"host,omitempty"`
	Agent    AgentContext    `json:"agent"`
	Payload  json.RawMessage `json:"payload"`
}

func NewRegistry() *Registry {
	return NewRegistryAt(actionDirectory())
}

func NewRegistryAt(directory string) *Registry {
	return &Registry{directory: directory}
}

// Prepare runs an action plugin on the agent's machine. Its stdout is the
// opaque JSON payload that crosses to the dashboard.
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
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("action plugin %q prepare: %w", name, err)
	}
	if err := command.Start(); err != nil {
		return nil, pluginError(ctx, name, "prepare", err, nil, nil)
	}
	// The payload is bounded, so what is read of it is too: one byte past
	// the limit is proof enough, and the plugin is stopped there rather
	// than allowed to fill memory before a size check could reject it.
	payload, readErr := io.ReadAll(io.LimitReader(stdout, maxPayloadBytes+1))
	if len(payload) > maxPayloadBytes {
		// Closing the pipe is what stops the plugin: its next write is a
		// broken pipe. Cancelling only kills the plugin process itself,
		// and whatever it started inherits the pipe and lives on.
		_ = stdout.Close()
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf(
			"action plugin %q payload exceeds the maximum of %d bytes",
			name,
			maxPayloadBytes,
		)
	}
	if err := command.Wait(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return nil, pluginError(ctx, name, "prepare", err, stderr.Bytes(), nil)
	}
	if readErr != nil {
		return nil, fmt.Errorf("action plugin %q prepare: read payload: %w", name, readErr)
	}
	payload = bytes.TrimSpace(payload)
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
