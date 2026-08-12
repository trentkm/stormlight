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
		if m.mode == modePTYFocus {
			m.mode = modeNormal
		}
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
