package ui

// The PTY view: every agent keeps a live terminal (Stormlight's own widget
// over the runtime's transport) for its whole life, and the dashboard
// renders the selected one — selecting an agent switches terminals, it
// never starts one. `t` flips to the transcript reading view; the
// terminal is the default.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
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

// altKeyName is what the hint row calls the modifier: macOS keyboards
// label it option, everywhere else it is alt. The chord itself is the
// same key either way.
func altKeyName() string {
	if runtime.GOOS == "darwin" {
		return "option"
	}
	return "alt"
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

// ptyGridDimensions is every terminal box's size. The window bar is mounted
// as the pane's title row; the terminal takes the rest of the body. Its
// controls live in the dashboard's existing blue footer, not in a second
// strip under the terminal. Zoomed, the sidebars collapse and the grid takes
// the full body width.
func (m Model) ptyGridDimensions() (int, int) {
	width, bodyHeight := m.bodyDimensions()
	gridHeight := max(2, bodyHeight-1)
	if width < 72 {
		return max(1, width-2), gridHeight
	}
	if m.ptyZoom {
		return width, gridHeight
	}
	_, _, interactionWidth := m.paneWidths(width)
	return max(1, interactionWidth-3), gridHeight
}

// selectedPTY is the widget behind the terminal pane right now; ok is false
// while the selected agent's terminal is still opening (or nothing is
// selected).
func (m Model) selectedPTY() (pty.Model, bool) {
	if m.ptyManager == nil {
		return pty.Model{}, false
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
func (m Model) handlePTYFrame(msg pty.FrameMsg) (tea.Model, tea.Cmd) {
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

// renderPTYInteraction is renderInteraction's live-terminal branch. The
// window bar is mounted as the pane's title row (see renderDashboardBody),
// while the dashboard's footer carries the terminal controls.
func (m Model) renderPTYInteraction(managedAgent agent.Agent, _, _ int) string {
	grid := mutedStyle().Render("Starting terminal...")
	if widget, ok := m.ptyManager.Widget(managedAgent.ID); ok {
		grid = widget.View()
	}
	return grid
}

func (m Model) terminalHints() string {
	if widget, ok := m.selectedPTY(); ok && widget.Scrolled() > 0 {
		return fmt.Sprintf("scrolled %d lines up — wheel down to follow", widget.Scrolled())
	}
	alt := altKeyName()
	return fmt.Sprintf("ctrl+space out  %s+j/k agents  %s+z zoom  %s+t transcript",
		alt, alt, alt)
}

// renderTerminalBar is the window bar over the terminal grid, and the only
// dashboard chrome the terminal view keeps: the agent's status glyph and
// name, the heading's meta line in words, and the terminal's real
// dimensions, on a filled band spanning the pane. The hint row below the
// grid is the focus signal; the band stays one steady surface. Zoomed it
// carries the roster position, because alt+j/k still cycle agents with the
// roster out of sight.
func (m Model) renderTerminalBar(managedAgent agent.Agent, width int) string {
	symbol, statusStyle := statusVisual(managedAgent)
	dims := ""
	if widget, ok := m.ptyManager.Widget(managedAgent.ID); ok {
		cols, rows := widget.TerminalSize()
		dims = fmt.Sprintf("%d×%d", cols, rows)
	}
	if m.ptyZoom {
		if list := m.agentsForSelectedWorkspace(); len(list) > 1 {
			dims = fmt.Sprintf("‹ %d/%d ›  %s", m.agentCursor+1, len(list), dims)
		}
	}
	label := agentDisplayTitle(managedAgent)
	if meta := terminalBarMeta(managedAgent); meta != "" {
		label += "  ·  " + meta
	}
	// " ● " + label + gap + dims + " " fills the width exactly; the label
	// yields first, the gap absorbs what remains.
	dimsWidth := lipgloss.Width(dims)
	label = truncate(label, max(1, width-dimsWidth-5))
	gap := max(1, width-4-lipgloss.Width(label)-dimsWidth)
	band := lipgloss.NewStyle().Background(colorSelect())
	glyph := statusStyle.Background(colorSelect()).Render(symbol)
	text := band.Foreground(colorText())
	return band.Render(" ") + glyph +
		text.Render(" "+label+strings.Repeat(" ", gap)+dims+" ")
}

// terminalBarMeta is the heading's meta line as bar words: the derived
// tokens, with attention states talking over them exactly as the
// transcript heading's meta does. The bar draws the status glyph itself,
// so the state speaks through its label alone.
func terminalBarMeta(managedAgent agent.Agent) string {
	switch {
	case managedAgent.EffectiveMark() == agent.MarkAttention:
		return "Marked needs attention"
	case managedAgent.EffectiveMark() == agent.MarkWorking:
		// The human said it is still going; nothing derived talks over
		// that. The tokens below carry the "marked in progress" label.
	case managedAgent.ProcessLive && managedAgent.Attention.Urgent():
		if managedAgent.Attention.TerminalOwned() {
			return "Needs " + string(managedAgent.Attention) + " — Enter opens the terminal"
		}
		return "Needs " + string(managedAgent.Attention) + " — i to reply"
	case managedAgent.ProcessLive && managedAgent.Attention == agent.AttentionWaiting:
		return "Unseen result"
	}
	tokens := interactionMetaTokens(managedAgent)
	parts := make([]string, 0, len(tokens))
	for index, token := range tokens {
		text := token.plain
		if index == 0 {
			text = agentStateLabel(managedAgent)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " · ")
}

// terminalFocused reports whether the keyboard currently belongs to the
// embedded terminal.
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
	if m.ptyZoom {
		// The zoomed pane has no sidebars, seams, or gutters: the grid's
		// first column is the screen's.
		return tea.NewCursor(x, ptyGridTop+y)
	}
	workspaceWidth, agentWidth, _ := m.paneWidths(width)
	// Origin constants are measured, not derived: a MARK parked at a known
	// emulator cell calibrated the grid's screen origin. Recalibrate the
	// same way if the pane chrome changes.
	return tea.NewCursor(workspaceWidth+agentWidth+1+x, ptyGridTop+y)
}

// ptyGridTop is the screen row of the terminal grid's first cell: the
// dashboard header, then the window bar mounted as the pane's title row.
const ptyGridTop = 2
