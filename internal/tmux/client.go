package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/diagnostic"
)

type Runner interface {
	Run(ctx context.Context, stdin []byte, args ...string) (string, error)
}

type Client struct {
	binary string
	socket string
	config string
}

func NewClient(socket string) *Client {
	return &Client{binary: "tmux", socket: socket}
}

// NewClientWithConfig returns a client that passes a tmux configuration file
// on every invocation. tmux only reads it when a command starts the server,
// so the Stormlight server always boots with Stormlight's config regardless
// of which command happens to start it.
func NewClientWithConfig(socket, config string) *Client {
	client := NewClient(socket)
	client.config = config
	return client
}

func NewClientFromEnv() *Client {
	socket := os.Getenv("STORMLIGHT_TMUX_SOCKET")
	if socket == "" {
		socket = DefaultSocket
	}
	return NewClient(socket)
}

func (c *Client) Socket() string {
	return c.socket
}

func (c *Client) Run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	started := time.Now()
	operation := "unknown"
	if len(args) > 0 {
		operation = args[0]
	}
	commandArgs := c.commandArgs(args)

	cmd := exec.CommandContext(ctx, c.binary, commandArgs...)
	if c.socket != "" {
		cmd.Env = withoutTmuxEnvironment(os.Environ())
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		duration := time.Since(started)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diagnostic.Logger().Error("tmux command timed out",
				"operation", operation,
				"duration_ms", duration.Milliseconds(),
			)
			return "", fmt.Errorf("tmux command timed out")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		logTmuxFailure(operation, detail, duration)
		return "", fmt.Errorf("tmux %s: %s", operation, detail)
	}
	diagnostic.Logger().Debug("tmux command completed",
		"operation", operation,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return strings.TrimSpace(stdout.String()), nil
}

func (c *Client) commandArgs(args []string) []string {
	prefix := make([]string, 0, 4+len(args))
	if c.socket != "" {
		prefix = append(prefix, "-L", c.socket)
	}
	if c.config != "" {
		prefix = append(prefix, "-f", c.config)
	}
	return append(prefix, args...)
}

func logTmuxFailure(operation, detail string, duration time.Duration) {
	attributes := []any{
		"operation", operation,
		"duration_ms", duration.Milliseconds(),
		"error", detail,
	}
	expected := operation == "has-session" ||
		(operation == "list-keys" && strings.Contains(detail, "unknown key")) ||
		(operation == "list-panes" && (strings.Contains(detail, "no server running") ||
			strings.Contains(detail, "failed to connect to server")))
	if expected {
		diagnostic.Logger().Debug("tmux probe found no target", attributes...)
		return
	}
	diagnostic.Logger().Error("tmux command failed", attributes...)
}

func withoutTmuxEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
