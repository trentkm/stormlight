package ui

// The PTY Spanreed: every agent keeps a live terminal session for its whole
// life (internal/ptyview.Manager), and the Spanreed renders the selected
// one — selecting an agent switches terminals, it never starts one. `t`
// flips to the transcript reading view; the terminal is the default.

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
	"github.com/trentkm/stormlight/internal/ptyview"
)

// ptyEnsuredMsg reports that the session herd was reconciled against the
// roster; the interesting work (arming the selected agent's frame wait)
// happens in the handler.
type ptyEnsuredMsg struct{}

// ptyFrameMsg is only a wake-up: View reads the session's frame directly,
// so nothing is copied through the message.
type ptyFrameMsg struct {
	id string
}

// attachReturnedMsg closes the loop on an external attach: the attached
// client resized every window, so the herd's sizes must be reasserted.
type attachReturnedMsg struct {
	name string
	err  error
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

// ptyGridDimensions is every emulator's cell size: the Spanreed content
// area minus the hint row. The grid, the tmux windows, and the drawn rows
// must all agree on one number, and because the whole herd streams at once
// they all share it.
func (m Model) ptyGridDimensions() (int, int) {
	width, height := m.interactionDimensions()
	return width, max(2, height-1)
}

// selectedPTY is the session behind the Spanreed right now; nil while the
// selected agent's session is still starting (or nothing is selected).
func (m Model) selectedPTY() *ptyview.Session {
	if m.ptyManager == nil {
		return nil
	}
	return m.ptyManager.Session(m.selectedAgentID())
}

// ensurePTYCmd reconciles the session herd against the roster. It runs on
// every dashboard refresh; when nothing changed it is a map diff and no
// tmux calls, so the fast cadence stays cheap.
func (m Model) ensurePTYCmd() tea.Cmd {
	if m.ptyManager == nil || !m.ready {
		return nil
	}
	ids := make([]string, 0, len(m.agents))
	for _, managedAgent := range m.agents {
		ids = append(ids, managedAgent.ID)
	}
	width, height := m.ptyGridDimensions()
	manager := m.ptyManager
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		manager.Ensure(ctx, ids, width, height)
		return ptyEnsuredMsg{}
	}
}

// armPTYWait starts (at most) one frame listener for the selected agent's
// session. The armed set is the dedup: a session emits into a 1-slot
// channel, and doubling the listeners would double the redraw messages.
func (m *Model) armPTYWait() tea.Cmd {
	if !m.ptyEnabled {
		return nil
	}
	session := m.selectedPTY()
	if session == nil || m.ptyArmed[session.AgentID] {
		return nil
	}
	m.ptyArmed[session.AgentID] = true
	return func() tea.Msg {
		<-session.Frames()
		return ptyFrameMsg{id: session.AgentID}
	}
}

func (m Model) handlePTYFrame(msg ptyFrameMsg) (tea.Model, tea.Cmd) {
	// The listener that produced this message consumed its token and is
	// gone; only the selected session earns a replacement.
	delete(m.ptyArmed, msg.id)
	if m.ptyEnabled && msg.id == m.selectedAgentID() {
		return m, m.armPTYWait()
	}
	return m, nil
}

// closeAllPTYCmd is the quit path: no pipe or FIFO outlives the dashboard.
func closeAllPTYCmd(manager *ptyview.Manager) tea.Cmd {
	return func() tea.Msg {
		manager.CloseAll()
		return nil
	}
}

// resizeAllPTYCmd reasserts the herd's sizes after an external attach let
// the client resize the windows.
func resizeAllPTYCmd(manager *ptyview.Manager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		manager.ResizeAll(ctx)
		return nil
	}
}

// interactionFollowCmd is what selection movement returns: a transcript
// reload in transcript view; in terminal view just a listener for the
// newly selected agent's frames — its session is already running.
func (m *Model) interactionFollowCmd() tea.Cmd {
	if !m.ptyEnabled {
		return m.loadInteractionCmd()
	}
	return m.armPTYWait()
}

// togglePTY flips between the live terminal and the transcript reading
// view. Purely a view change: the sessions stream on either way.
func (m *Model) togglePTY() tea.Cmd {
	if m.ptyEnabled {
		m.ptyEnabled = false
		m.status = "Transcript view"
		return m.loadInteractionCmd()
	}
	m.ptyEnabled = true
	m.activePane = paneInteraction
	m.status = "Terminal view"
	return m.armPTYWait()
}

// renderPTYInteraction is renderInteraction's live-terminal branch: same
// heading, then the emulator grid where the transcript viewport would be,
// then one hint row where the composer affordances live.
func (m Model) renderPTYInteraction(managedAgent agent.Agent, width, height int) string {
	heading := m.renderInteractionHeading(managedAgent, width)

	gridWidth, gridHeight := m.ptyGridDimensions()
	grid := mutedStyle().Render("Starting terminal...")
	scrolled := 0
	if session := m.ptyManager.Session(managedAgent.ID); session != nil {
		frame := session.Frame()
		grid = frame.Grid
		scrolled = frame.Scrolled
	}
	grid = fitGrid(grid, gridWidth, gridHeight)

	focused := m.terminalFocused()
	hint := "Enter/l terminal  t transcript  F full screen"
	if focused {
		hint = "terminal — ctrl+q to leave  ·  F full screen"
	}
	if scrolled > 0 {
		hint = fmt.Sprintf("scrolled %d lines up — wheel down to follow  ·  %s", scrolled, hint)
	}
	composer := mutedStyle().Render(truncate(hint, width))
	if focused {
		composer = accentStyle().Render(truncate(hint, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, heading, grid, composer)
}

// terminalFocused reports whether the keyboard currently belongs to the
// Spanreed terminal.
func (m Model) terminalFocused() bool {
	return m.ptyEnabled && m.mode == modeNormal &&
		m.activePane == paneInteraction
}

// ptyCursor places the real terminal cursor on the Spanreed's grid when
// the terminal holds focus — the cell the agent's program is writing at,
// blinking like it means it, because it does.
func (m Model) ptyCursor() *tea.Cursor {
	if !m.terminalFocused() {
		return nil
	}
	session := m.selectedPTY()
	if session == nil {
		return nil
	}
	frame := session.Frame()
	if frame.Scrolled > 0 {
		return nil
	}
	width := max(1, m.width-1)
	if width < 72 {
		// The narrow single-pane layout draws no Spanreed grid to anchor
		// a cursor to.
		return nil
	}
	workspaceWidth, agentWidth, _ := m.paneWidths(width)
	gridWidth, gridHeight := m.ptyGridDimensions()
	// The display clips a too-tall emulator to its bottom rows; cursor
	// coordinates are emulator-space and shift with the clip.
	cursorY := frame.CursorY - max(0, frame.Rows-gridHeight)
	if frame.CursorX < 0 || frame.CursorX >= gridWidth ||
		cursorY < 0 || cursorY >= gridHeight {
		return nil
	}
	// Origin constants are measured, not derived: a MARK parked at a known
	// emulator cell calibrated the grid's screen origin at
	// (workspaceWidth+agentWidth+1, ptyGridTop). Recalibrate the same way
	// if the pane chrome changes.
	return tea.NewCursor(
		workspaceWidth+agentWidth+1+frame.CursorX,
		ptyGridTop+cursorY,
	)
}

// ptyGridTop is the screen row of the terminal grid's first cell: header,
// pane title, the heading's three rows, and the pane's top margin.
const ptyGridTop = 6

// fitGrid clamps the emulator's render to the pane: exactly height rows,
// no row wider than width. In tap mode the emulator mirrors the real pane,
// which an attached client can hold larger than the display box — clip
// like tmux does, keeping the bottom rows where the action is and the
// left columns where lines begin.
func fitGrid(grid string, width, height int) string {
	rows := strings.Split(grid, "\n")
	if len(rows) > height {
		rows = rows[len(rows)-height:]
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

