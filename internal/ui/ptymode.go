package ui

// The experimental full-PTY Spanreed: a live terminal view of the selected
// agent's pane, streamed through internal/ptyview. Toggled with `t`; the
// transcript path remains the default and everything here stays out of its
// way when disabled.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/ptyview"
)

type ptyStartedMsg struct {
	session *ptyview.Session
	id      string
	err     error
}

// ptyFrameMsg is only a wake-up: View reads the session's frame directly,
// so nothing is copied through the message.
type ptyFrameMsg struct {
	id string
}

type ptyStoppedMsg struct {
	id string
}

// ptyStateDir mirrors the rest of the state files (prefs, catalog): XDG
// first, ~/.local/state as the fallback.
func ptyStateDir() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "stormlight")
}

// ptyGridDimensions is the emulator's cell size: the Spanreed content area
// minus the hint row. The transcript viewport spends that row differently
// (its composer swaps in and out), but the grid, the tmux window, and the
// drawn rows must all agree on one number.
func (m Model) ptyGridDimensions() (int, int) {
	width, height := m.interactionDimensions()
	return width, max(2, height-1)
}

func startPTYCmd(backend Backend, id string, width, height int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		session, err := ptyview.Start(ctx, backend, id, ptyStateDir(), width, height)
		return ptyStartedMsg{session: session, id: id, err: err}
	}
}

// ptyWaitCmd blocks on the session's frame channel; each delivery earns one
// redraw and one re-arm. A stopped session goes quiet rather than closing
// the channel, so the stale receiver is dropped when its message no longer
// names the live session.
func ptyWaitCmd(session *ptyview.Session) tea.Cmd {
	return func() tea.Msg {
		<-session.Frames()
		return ptyFrameMsg{id: session.AgentID}
	}
}

func stopPTYCmd(session *ptyview.Session) tea.Cmd {
	return func() tea.Msg {
		session.Close()
		return ptyStoppedMsg{id: session.AgentID}
	}
}

// stopPTY tears down the live view synchronously in the model and returns
// the async half. Callers use it wherever the view must be gone before the
// next thing happens: toggling off, moving selection, attaching, quitting.
func (m *Model) stopPTY() tea.Cmd {
	if m.pty == nil {
		return nil
	}
	session := m.pty
	m.pty = nil
	return stopPTYCmd(session)
}

// interactionFollowCmd is what selection movement returns: the transcript
// reload normally, or a PTY restart on the newly selected agent when the
// live view is on.
func (m *Model) interactionFollowCmd() tea.Cmd {
	if !m.ptyEnabled {
		return m.loadInteractionCmd()
	}
	id := m.selectedAgentID()
	if m.pty != nil && m.pty.AgentID == id {
		// Same agent — the session is already the right one. Restarting
		// here would reset scroll position on every navigation key.
		return nil
	}
	stop := m.stopPTY()
	if id == "" {
		return stop
	}
	width, height := m.ptyGridDimensions()
	return tea.Batch(stop, startPTYCmd(m.backend, id, width, height))
}

// togglePTY flips the live view on or off for the selected agent.
func (m *Model) togglePTY() tea.Cmd {
	if m.ptyEnabled {
		m.ptyEnabled = false
		if m.mode == modePTYFocus {
			m.mode = modeNormal
		}
		m.status = "Transcript view"
		return tea.Batch(m.stopPTY(), m.loadInteractionCmd(), m.syncAgentWindowsCmd())
	}
	id := m.selectedAgentID()
	if id == "" {
		m.status = "No agent selected"
		return nil
	}
	m.ptyEnabled = true
	m.activePane = paneInteraction
	m.status = "Terminal view"
	width, height := m.ptyGridDimensions()
	return startPTYCmd(m.backend, id, width, height)
}

func (m Model) handlePTYStarted(msg ptyStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.ptyEnabled = false
		m.err = msg.err
		m.status = "Terminal view failed"
		diagnostic.Logger().Error("pty view start failed",
			"agent_id", msg.id, "error", msg.err)
		return m, m.loadInteractionCmd()
	}
	if !m.ptyEnabled || msg.id != m.selectedAgentID() {
		// The user toggled off or moved on while the session was starting;
		// this one arrives already unwanted.
		return m, stopPTYCmd(msg.session)
	}
	m.pty = msg.session
	// One sync so every *other* window returns to transcript sizing while
	// this one keeps its 1:1 size.
	return m, tea.Batch(ptyWaitCmd(msg.session), m.syncAgentWindowsCmd())
}

func (m Model) handlePTYFrame(msg ptyFrameMsg) (tea.Model, tea.Cmd) {
	if m.pty == nil || m.pty.AgentID != msg.id {
		return m, nil
	}
	// The frame itself is read in View; this message only re-arms the wait.
	return m, ptyWaitCmd(m.pty)
}

// resizePTYCmd moves the tmux window and emulator to the new grid size.
func resizePTYCmd(session *ptyview.Session, width, height int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := session.Resize(ctx, width, height); err != nil {
			diagnostic.Logger().Warn("pty view resize", "error", err)
		}
		return nil
	}
}

// renderPTYInteraction is renderInteraction's live-terminal branch: same
// heading, then the emulator grid where the transcript viewport would be,
// then one hint row where the composer affordances live.
func (m Model) renderPTYInteraction(managedAgent agent.Agent, width, height int) string {
	title := titleStyle().Render(truncate(agentDisplayTitle(managedAgent), width))
	if rowEmphasisFor(managedAgent) == emphasisWorking {
		title = shimmerText(
			truncate(agentDisplayTitle(managedAgent), width),
			m.shimmerPhaseOrRest(),
			nil,
		)
	}
	meta := interactionMeta(managedAgent, width)
	heading := lipgloss.JoinVertical(lipgloss.Left, title, meta, "")

	gridWidth, gridHeight := m.ptyGridDimensions()
	grid := mutedStyle().Render("Starting terminal view...")
	scrolled := 0
	if m.pty != nil {
		frame := m.pty.Frame()
		grid = frame.Grid
		scrolled = frame.Scrolled
		if m.mode == modePTYFocus {
			grid = overlayCursor(grid, frame.CursorX, frame.CursorY, gridWidth)
		}
	}
	grid = fitGrid(grid, gridWidth, gridHeight)

	hint := "f focus  t transcript  Enter open terminal"
	if m.mode == modePTYFocus {
		hint = "typing reaches the agent — ctrl+q to stop"
	}
	if scrolled > 0 {
		hint = fmt.Sprintf("scrolled %d lines up — wheel down to follow  ·  %s", scrolled, hint)
	}
	composer := mutedStyle().Render(truncate(hint, width))
	if m.mode == modePTYFocus {
		composer = accentStyle().Render(truncate(hint, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, heading, grid, composer)
}

// fitGrid clamps the emulator's render to the pane: exactly height rows,
// no row wider than width. The emulator is sized to match, so this only
// trims the transient frames around a resize.
func fitGrid(grid string, width, height int) string {
	rows := strings.Split(grid, "\n")
	if len(rows) > height {
		rows = rows[:height]
	}
	for index, row := range rows {
		if ansi.StringWidth(row) > width {
			rows[index] = ansi.Truncate(row, width, "")
		}
		rows[index] = rows[index] + ansi.ResetStyle
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

// overlayCursor paints the cell under the terminal cursor in reverse video
// so focus mode shows where typing lands.
func overlayCursor(grid string, x, y, width int) string {
	rows := strings.Split(grid, "\n")
	if y < 0 || y >= len(rows) {
		return grid
	}
	row := rows[y]
	rowWidth := ansi.StringWidth(row)
	if x < 0 || x >= width {
		return grid
	}
	left := ""
	cell := " "
	right := ""
	if x < rowWidth {
		left = ansi.Cut(row, 0, x)
		if got := ansi.Cut(row, x, x+1); strings.TrimSpace(ansi.Strip(got)) != "" {
			cell = ansi.Strip(got)
		}
		right = ansi.Cut(row, x+1, rowWidth)
	} else {
		left = row + strings.Repeat(" ", x-rowWidth)
	}
	rows[y] = left + "\x1b[7m" + cell + "\x1b[27m" + right
	return strings.Join(rows, "\n")
}
