package ui

// The PTY Spanreed: every agent keeps a live terminal (an oathgate widget
// over the runtime's transport) for its whole life, and the Spanreed
// renders the selected one — selecting an agent switches terminals, it
// never starts one. `t` flips to the transcript reading view; the
// terminal is the default.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/oathgate"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/ptyview"
)

// ptyEnsuredMsg reports that the terminal herd was reconciled against the
// roster; the interesting work (arming the selected agent's frame wait)
// happens in the handler.
type ptyEnsuredMsg struct{}

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

// ptyGridDimensions is every terminal box's size: the Spanreed content
// area minus the hint row.
func (m Model) ptyGridDimensions() (int, int) {
	width, height := m.interactionDimensions()
	return width, max(2, height-1)
}

// selectedPTY is the widget behind the Spanreed right now; ok is false
// while the selected agent's terminal is still opening (or nothing is
// selected).
func (m Model) selectedPTY() (oathgate.Model, bool) {
	if m.ptyManager == nil {
		return oathgate.Model{}, false
	}
	return m.ptyManager.Widget(m.selectedAgentID())
}

// ensurePTYCmd reconciles the terminal herd against the roster. It runs on
// every dashboard refresh; when nothing changed it is a map diff and no
// transport calls, so the fast cadence stays cheap.
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
		for _, resize := range manager.Ensure(ctx, ids, width, height) {
			resize()
		}
		return ptyEnsuredMsg{}
	}
}

// armPTYWait starts (at most) one frame listener for the selected agent's
// widget. The armed set is the dedup: doubling the listeners would double
// the redraw messages.
func (m *Model) armPTYWait() tea.Cmd {
	if !m.ptyEnabled {
		return nil
	}
	widget, ok := m.selectedPTY()
	if !ok || m.ptyArmed[widget.ID()] {
		return nil
	}
	m.ptyArmed[widget.ID()] = true
	return widget.Init()
}

// handlePTYFrame routes a widget's wake-up: the listener that produced it
// is spent, and only the selected agent's widget earns a replacement.
func (m Model) handlePTYFrame(msg oathgate.FrameMsg) (tea.Model, tea.Cmd) {
	delete(m.ptyArmed, msg.ID)
	if !m.ptyEnabled || m.ptyManager == nil {
		return m, nil
	}
	agentID, ok := m.ptyManager.AgentForWidget(msg.ID)
	if !ok || agentID != m.selectedAgentID() {
		return m, nil
	}
	return m, m.armPTYWait()
}

// closeAllPTYCmd is the quit path: no transport outlives the dashboard.
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
		for _, resize := range manager.ResizeAll(ctx) {
			resize()
		}
		return nil
	}
}

// interactionFollowCmd is what selection movement returns: a transcript
// reload in transcript view; in terminal view just a listener for the
// newly selected agent's frames — its terminal is already running.
func (m *Model) interactionFollowCmd() tea.Cmd {
	if !m.ptyEnabled {
		return m.loadInteractionCmd()
	}
	return m.armPTYWait()
}

// togglePTY flips between the live terminal and the transcript reading
// view. Purely a view change: the terminals stream on either way.
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
// heading, then the widget where the transcript viewport would be, then
// one hint row where the composer affordances live.
func (m Model) renderPTYInteraction(managedAgent agent.Agent, width, height int) string {
	heading := m.renderInteractionHeading(managedAgent, width)

	grid := mutedStyle().Render("Starting terminal...")
	scrolled := 0
	if widget, ok := m.ptyManager.Widget(managedAgent.ID); ok {
		grid = widget.View()
		scrolled = widget.Scrolled()
	}

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
// the terminal holds focus. The widget answers box-relative — a nested
// component cannot know where it sits — and the measured origin does the
// rest.
func (m Model) ptyCursor() *tea.Cursor {
	if !m.terminalFocused() {
		return nil
	}
	widget, ok := m.selectedPTY()
	if !ok {
		return nil
	}
	x, y, visible := widget.Cursor()
	if !visible {
		return nil
	}
	width := max(1, m.width-1)
	if width < 72 {
		// The narrow single-pane layout draws no Spanreed grid to anchor
		// a cursor to.
		return nil
	}
	workspaceWidth, agentWidth, _ := m.paneWidths(width)
	// Origin constants are measured, not derived: a MARK parked at a known
	// emulator cell calibrated the grid's screen origin. Recalibrate the
	// same way if the pane chrome changes.
	return tea.NewCursor(workspaceWidth+agentWidth+1+x, ptyGridTop+y)
}

// ptyGridTop is the screen row of the terminal grid's first cell: header,
// pane title, the heading's three rows, and the pane's top margin.
const ptyGridTop = 6
