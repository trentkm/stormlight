package ui

// The PTY view: every agent keeps a live terminal (Stormlight's own widget
// over the runtime's transport) for its whole life, and the dashboard
// renders the selected one — selecting an agent switches terminals, it
// never starts one. `t` flips to the transcript reading view; the
// terminal is the default.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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

// KeyBindings are the seam chords: the keys the dashboard keeps while an
// agent's terminal holds the keyboard. Several chords may name one
// action; the first is what the hints display.
type KeyBindings struct {
	AgentsNext     []string
	AgentsPrevious []string
	QueueNext      []string
	QueuePrevious  []string
	Zoom           []string
}

// defaultKeyBindings are alt chords: the keys hosted TUIs leave alone.
// A desk where the window manager owns alt (AeroSpace and kin) rebinds
// here — [keys] in config.toml.
func defaultKeyBindings() KeyBindings {
	return KeyBindings{
		AgentsNext:     []string{"alt+j", "alt+down"},
		AgentsPrevious: []string{"alt+k", "alt+up"},
		QueueNext:      []string{"alt+n"},
		QueuePrevious:  []string{"alt+p"},
		Zoom:           []string{"alt+z"},
	}
}

// fillKeyDefaults completes a partially configured set: an action the
// config leaves empty keeps its default chords.
func fillKeyDefaults(keys KeyBindings) KeyBindings {
	defaults := defaultKeyBindings()
	if len(keys.AgentsNext) == 0 {
		keys.AgentsNext = defaults.AgentsNext
	}
	if len(keys.AgentsPrevious) == 0 {
		keys.AgentsPrevious = defaults.AgentsPrevious
	}
	if len(keys.QueueNext) == 0 {
		keys.QueueNext = defaults.QueueNext
	}
	if len(keys.QueuePrevious) == 0 {
		keys.QueuePrevious = defaults.QueuePrevious
	}
	if len(keys.Zoom) == 0 {
		keys.Zoom = defaults.Zoom
	}
	return keys
}

// chordName is what the hint row calls a chord: the binding's own name,
// verbatim — alt everywhere, because that is what the key is called in
// every config file and on every keyboard but Apple's, and one name is
// worth more than two dialects.
func chordName(chord string) string {
	return chord
}

// chordPair compresses a next/previous pair for the hints: chords that
// differ only in their final key read as one entry ("ctrl+option+j/k").
func chordPair(next, previous []string) string {
	if len(next) == 0 || len(previous) == 0 {
		return ""
	}
	a, b := chordName(next[0]), chordName(previous[0])
	cutA, cutB := strings.LastIndex(a, "+"), strings.LastIndex(b, "+")
	if cutA > 0 && cutA == cutB && a[:cutA] == b[:cutB] {
		return a + "/" + b[cutB+1:]
	}
	return a + " " + b
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

// armPTYWait keeps exactly one listener on the terminal herd's shared
// frame gate, and re-asserts which terminal is on screen — only visible
// terminals knock on that gate. The armed flag is the dedup: doubling
// the listeners would double the redraw messages.
func (m *Model) armPTYWait() tea.Cmd {
	if m.ptyManager == nil || (!m.ptyEnabled && m.overlay == nil) {
		return nil
	}
	m.ptyManager.SetVisible(m.selectedAgentID())
	if m.ptyWaiting {
		return nil
	}
	m.ptyWaiting = true
	return m.ptyManager.Wait()
}

// handlePTYFrame is the gate's wake-up: the listener that produced it is
// spent, the render pass that follows repaints whatever changed, and one
// replacement listener re-arms.
func (m Model) handlePTYFrame(pty.FrameMsg) (tea.Model, tea.Cmd) {
	m.ptyWaiting = false
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

// jumpQueue moves the selection to the next agent waiting on a human —
// oldest first, wrapping — from either side of the seam. Flat cycling
// walks the roster; this walks the inbox.
func (m Model) jumpQueue(step agent.QueueStep) (tea.Model, tea.Cmd) {
	queue := agent.Queue(m.agents)
	next, ok := agent.StepInQueue(queue, m.selectedAgentID(), step)
	if !ok {
		m.status = "Nobody is waiting on you"
		return m, nil
	}
	m.rebuildGroups(next.Workspace.ID, next.ID)
	return m, m.interactionFollowCmd()
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
		m.status = "Ready"
		return m.loadInteractionCmd()
	}
	m.ptyEnabled = true
	m.activePane = paneInteraction
	m.status = "Ready"
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
	if m.ptySelecting && m.ptySelDragged {
		start, end := m.selectionSpan()
		gridWidth, _ := m.ptyGridDimensions()
		grid = paintTerminalSelection(grid, start, end, gridWidth)
	}
	return grid
}

func (m Model) terminalHints() string {
	if widget, ok := m.selectedPTY(); ok && widget.Scrolled() > 0 {
		return fmt.Sprintf("scrolled %d lines up — wheel down to follow", widget.Scrolled())
	}
	return fmt.Sprintf(
		"ctrl+space out  %s agents  %s queue  %s zoom",
		chordPair(m.keys.AgentsNext, m.keys.AgentsPrevious),
		chordPair(m.keys.QueueNext, m.keys.QueuePrevious),
		chordName(m.keys.Zoom[0]))
}

// renderTerminalBar is the window bar over the terminal grid, and the only
// dashboard chrome the terminal view keeps: the agent's status glyph and
// name, the heading's meta line in words, and the terminal's real
// dimensions, on a filled band spanning the pane. The band flips to accent
// while the keyboard feeds the terminal — focus is painted, not implied —
// and zoomed it carries the roster position, because alt+j/k still cycle
// agents with the roster out of sight.
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
	return bandSegment(symbol, &statusStyle, label, dims,
		m.terminalFocused(), width)
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
	if m.overlay != nil {
		return m.overlayCursor()
	}
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

// bodyTop is the screen row the body block starts on, under the one-row
// dashboard header; overlayCursor anchors the popup's cursor to it.
const bodyTop = 1
