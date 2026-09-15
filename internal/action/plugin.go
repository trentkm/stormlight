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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
)

const (
	ProtocolVersion = 1
	maxPayloadBytes = 256 * 1024
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
	command := exec.CommandContext(ctx, path, append([]string{"prepare"}, args...)...)
	command.Dir = directory
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, pluginError(ctx, name, "prepare", err, stderr.Bytes(), nil)
	}
	payload := bytes.TrimSpace(stdout.Bytes())
	if len(payload) == 0 {
		return nil, fmt.Errorf("action plugin %q emitted no JSON payload", name)
	}
	if len(payload) > maxPayloadBytes {
		return nil, fmt.Errorf(
			"action plugin %q payload is %d bytes; maximum is %d",
			name,
			len(payload),
			maxPayloadBytes,
		)
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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
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
