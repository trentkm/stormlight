package tmux

// The session.PaneStreamer capability: raw byte transport between an agent's
// pane and a live terminal view. Everything here is one-shot tmux
// invocations; the long-lived half (the FIFO reader and the emulator) lives
// in internal/ptyview.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/trentkm/stormlight/internal/session"
)

// PipePaneOpen starts streaming the pane's output into fifoPath. The bare
// pipe-pane first is deliberate: it unconditionally closes any pipe a
// crashed run left behind, where `-o` would merely toggle and leave the
// stale pipe the winner half the time.
func (r *Runtime) PipePaneOpen(ctx context.Context, id, fifoPath string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	if strings.ContainsAny(fifoPath, "'") {
		// The path lands inside a single-quoted shell word below; ours never
		// contain quotes, so refusing is cheaper than escaping.
		return fmt.Errorf("pipe pane: fifo path %q contains a quote", fifoPath)
	}
	if _, err := r.runner.Run(ctx, nil, "pipe-pane", "-t", managedAgent.PaneID); err != nil {
		return fmt.Errorf("clear stale pane pipe: %w", err)
	}
	command := fmt.Sprintf("cat > '%s'", fifoPath)
	if _, err := r.runner.Run(ctx, nil,
		"pipe-pane", "-t", managedAgent.PaneID, command,
	); err != nil {
		return fmt.Errorf("open pane pipe: %w", err)
	}
	return nil
}

func (r *Runtime) PipePaneClose(ctx context.Context, id string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, nil, "pipe-pane", "-t", managedAgent.PaneID); err != nil {
		return fmt.Errorf("close pane pipe: %w", err)
	}
	return nil
}

// CaptureRaw is the emulator seed: escapes intact, wrapped exactly as the
// pane wrapped it. No -J — a terminal emulator wants the pane's rows, not
// rejoined logical lines. history > 0 reaches that far into the pane's
// scrollback above the visible screen; replaying it through the emulator
// rebuilds the history tmux kept while the dashboard was away.
func (r *Runtime) CaptureRaw(ctx context.Context, id string, history int) (string, error) {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return "", err
	}
	args := []string{"capture-pane", "-p", "-e", "-t", managedAgent.PaneID}
	if history > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", history))
	}
	return r.runner.Run(ctx, nil, args...)
}

func (r *Runtime) PaneView(ctx context.Context, id string) (session.PaneView, error) {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return session.PaneView{}, err
	}
	out, err := r.runner.Run(ctx, nil,
		"display-message", "-p", "-t", managedAgent.PaneID,
		"#{cursor_x} #{cursor_y} #{alternate_on} #{pane_width} #{pane_height}",
	)
	if err != nil {
		return session.PaneView{}, fmt.Errorf("read pane view state: %w", err)
	}
	fields := strings.Fields(out)
	if len(fields) != 5 {
		return session.PaneView{}, fmt.Errorf("read pane view state: unexpected reply %q", out)
	}
	numbers := make([]int, 5)
	for index, field := range fields {
		if index == 2 {
			continue
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			return session.PaneView{}, fmt.Errorf("read pane view state: %w", err)
		}
		numbers[index] = value
	}
	return session.PaneView{
		CursorX:   numbers[0],
		CursorY:   numbers[1],
		AltScreen: fields[2] == "1",
		Cols:      numbers[3],
		Rows:      numbers[4],
	}, nil
}

// ResizeAgentWindow sizes one agent's window exactly — the 1:1 contract a
// live terminal view needs — while keeping the same deference to attached
// clients that SyncWindowSizes applies: a human looking at the session owns
// its sizes.
func (r *Runtime) ResizeAgentWindow(ctx context.Context, id string, width, height int) error {
	if width < 20 || height < 5 {
		return nil
	}
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	clients, err := r.runner.Run(ctx, nil,
		"list-clients", "-t", r.sessionName, "-F", "#{client_name}",
	)
	if err != nil || strings.TrimSpace(clients) != "" {
		return nil
	}
	if _, err := r.runner.Run(ctx, nil,
		"resize-window", "-t", managedAgent.WindowID,
		"-x", strconv.Itoa(width), "-y", strconv.Itoa(height),
	); err != nil {
		return fmt.Errorf("resize window %s: %w", managedAgent.WindowID, err)
	}
	return nil
}

// SendRaw delivers bytes to the pane's input verbatim via send-keys -H, one
// two-digit hex argument per byte. A keypress is a handful of bytes, so one
// tmux invocation per press is fine.
func (r *Runtime) SendRaw(ctx context.Context, id string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	args := make([]string, 0, len(data)+4)
	args = append(args, "send-keys", "-t", managedAgent.PaneID, "-H")
	for _, b := range data {
		args = append(args, fmt.Sprintf("%02x", b))
	}
	if _, err := r.runner.Run(ctx, nil, args...); err != nil {
		return fmt.Errorf("send raw bytes: %w", err)
	}
	return nil
}
