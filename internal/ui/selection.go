package ui

// Mouse handling: wheel scroll and drag-to-copy selection.
// Split from model.go; see #34.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// interactionContentTop is the screen row where the transcript viewport
// starts: header, pane title, then the agent heading's three rows.
const interactionContentTop = 5

// handleMouse drives the transcript's two mouse behaviors: the wheel
// scrolls it (positionally, without moving keyboard focus), and
// press-drag-release highlights lines and copies them. Both stay live
// while composing or searching — reading back is most needed mid-reply.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	scrollable := m.mode == modeNormal ||
		m.mode == modeCompose ||
		m.mode == modeSearch
	if !m.ready || !scrollable {
		return m, nil
	}
	mouse := msg.Mouse()
	if mouse.Button == tea.MouseLeft && m.mode == modeNormal {
		if m.ptyEnabled {
			return m.handleTerminalMouse(msg)
		}
		return m.handleSelectionMouse(msg)
	}
	// A wheel tick is its own message type, so asking for one is the whole
	// filter: under the v1 action enum this had to reject every non-press
	// event first, because a wheel arrived as a press carrying a wheel
	// button.
	if _, ok := msg.(tea.MouseWheelMsg); !ok {
		return m, nil
	}
	direction := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		direction = -1
	case tea.MouseWheelDown:
		direction = 1
	default:
		return m, nil
	}
	// Only the transcript wheels: scrolling a list would move its
	// selection, and losing the selected agent to a stray wheel tick is
	// worse than no scroll at all.
	if m.paneAt(mouse.X) != paneInteraction {
		return m, nil
	}
	if m.ptyEnabled {
		widget, ok := m.selectedPTY()
		if !ok {
			return m, nil
		}
		if widget.MouseReporting() {
			// The hosted program asked for the mouse: the wheel is its —
			// Claude Code scrolls its own transcript this way — so the
			// event forwards as SGR mouse instead of moving the replica.
			if col, row, ok := m.gridCellAt(mouse.X, mouse.Y); ok {
				button := 64
				if direction > 0 {
					button = 65
				}
				return m, writeTerminalCmd(widget, []byte(fmt.Sprintf(
					"\x1b[<%d;%d;%dM", button, col+1, row+1)))
			}
			return m, nil
		}
		// Wheel deltas coalesce through the widget, so a trackpad burst
		// lands as one update; the scheduled flush returns through
		// Update's message forwarding.
		return m, widget.QueueScroll(-direction * 3)
	}
	m.moveSelectionIn(paneInteraction, direction*3)
	return m, nil
}

// gridCellAt maps screen coordinates to a cell of the terminal grid,
// mirroring ptyCursor's origin math: zoomed the grid owns the body,
// docked it starts past the sidebars and the gutter.
func (m Model) gridCellAt(x, y int) (int, int, bool) {
	width := max(1, m.width-1)
	if width < 72 {
		return 0, 0, false
	}
	left := 0
	if !m.ptyZoom {
		workspaceWidth, agentWidth, _ := m.paneWidths(width)
		left = workspaceWidth + agentWidth + 1
	}
	col, row := x-left, y-ptyGridTop
	gridWidth, gridHeight := m.ptyGridDimensions()
	if col < 0 || col >= gridWidth || row < 0 || row >= gridHeight {
		return 0, 0, false
	}
	return col, row, true
}

// handleTerminalMouse is the left button over the terminal view. A click
// focuses the pane under the pointer and forwards to a program that asked
// for the mouse; a drag is Stormlight's line selection, copied on release
// — the same gesture the transcript speaks.
func (m Model) handleTerminalMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	widget, hasWidget := m.selectedPTY()
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.activePane = m.paneAt(mouse.X)
		row, ok := m.terminalRowAt(mouse.X, mouse.Y)
		if !ok {
			m.ptySelecting = false
			return m, nil
		}
		m.ptySelecting = true
		m.ptySelDragged = false
		m.ptySelAnchor, m.ptySelHead = row, row
		return m, nil
	case tea.MouseMotionMsg:
		if !m.ptySelecting {
			return m, nil
		}
		if row, ok := m.terminalRowAt(mouse.X, mouse.Y); ok {
			if row != m.ptySelAnchor {
				m.ptySelDragged = true
			}
			m.ptySelHead = row
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if !m.ptySelecting {
			return m, nil
		}
		m.ptySelecting = false
		if m.ptySelDragged {
			command := m.copyTerminalSelectionCmd()
			m.ptySelDragged = false
			return m, command
		}
		// A plain click belongs to a program that wants the mouse.
		if hasWidget && widget.MouseReporting() {
			if col, row, ok := m.gridCellAt(mouse.X, mouse.Y); ok {
				press := fmt.Sprintf("\x1b[<0;%d;%dM", col+1, row+1)
				release := fmt.Sprintf("\x1b[<0;%d;%dm", col+1, row+1)
				return m, writeTerminalCmd(widget, []byte(press+release))
			}
		}
		return m, nil
	}
	return m, nil
}

// terminalRowAt maps screen coordinates to a grid row for selection,
// clamping the column so a drag past the edges keeps its row.
func (m Model) terminalRowAt(x, y int) (int, bool) {
	_, gridHeight := m.ptyGridDimensions()
	_, row, ok := m.gridCellAt(x, y)
	if ok {
		return row, true
	}
	// Off to the side but vertically inside: clamp to the grid, the way
	// every terminal treats a drag that leaves the window.
	row = clamp(y-ptyGridTop, 0, gridHeight-1)
	if m.paneAt(x) == paneInteraction && y >= ptyGridTop &&
		y < ptyGridTop+gridHeight {
		return row, true
	}
	return 0, false
}

// copyTerminalSelectionCmd extracts the selected grid rows as plain text
// and hands them to the clipboard.
func (m Model) copyTerminalSelectionCmd() tea.Cmd {
	widget, ok := m.selectedPTY()
	if !ok {
		return nil
	}
	start, end := m.ptySelAnchor, m.ptySelHead
	if start > end {
		start, end = end, start
	}
	lines := widget.Text()
	if start >= len(lines) {
		return nil
	}
	end = min(end, len(lines)-1)
	text := strings.Join(lines[start:end+1], "\n")
	count := end - start + 1
	return func() tea.Msg {
		if err := copyToClipboard(text); err != nil {
			return actionMsg{status: "Action failed", err: err}
		}
		label := "line"
		if count != 1 {
			label = "lines"
		}
		return actionMsg{status: fmt.Sprintf("Copied %d %s", count, label)}
	}
}

// handleSelectionMouse turns press-drag-release over the transcript into a
// line-wise selection: the drag highlights, the release copies the lines
// to the tmux buffer and system clipboard.
// The three stages of a drag are three message types in v2 rather than
// three values of an action enum, so the state machine switches on the
// message itself; the coordinates come from the Mouse each one carries.
func (m Model) handleSelectionMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if m.paneAt(mouse.X) != paneInteraction {
			m.selectionActive = false
			return m, nil
		}
		line, ok := m.transcriptLineAt(mouse.Y)
		if !ok {
			m.selectionActive = false
			return m, nil
		}
		m.selectionActive = true
		m.selectionDragging = true
		m.selectionAnchor = line
		m.selectionHead = line
		return m, nil
	case tea.MouseMotionMsg:
		if !m.selectionDragging {
			return m, nil
		}
		if line, ok := m.transcriptLineAt(mouse.Y); ok {
			m.selectionHead = line
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if !m.selectionDragging {
			return m, nil
		}
		m.selectionDragging = false
		command := m.copySelectionCmd()
		// The copy is the point; once it's made the highlight has done
		// its job, like tmux's own mouse selection.
		m.selectionActive = false
		return m, command
	}
	return m, nil
}

// transcriptLineAt maps a screen row to a content line of the transcript,
// clamped to the transcript's bounds.
func (m Model) transcriptLineAt(y int) (int, bool) {
	if _, ok := m.selectedAgent(); !ok || m.interactionContent == "" {
		return 0, false
	}
	row := y - interactionContentTop
	if row < 0 {
		row = 0
	}
	if row >= m.interaction.Height() {
		row = max(0, m.interaction.Height()-1)
	}
	total := m.interaction.TotalLineCount()
	if total == 0 {
		return 0, false
	}
	return clamp(m.interaction.YOffset()+row, 0, total-1), true
}

// paintTranscriptSelection reverses the video of the visible rows inside
// the selected line range.
func paintTranscriptSelection(view string, offset, start, end int) string {
	lines := strings.Split(view, "\n")
	for index := range lines {
		global := offset + index
		if global < start || global > end {
			continue
		}
		lines[index] = searchMatchSGR + ansi.Strip(lines[index]) + searchResetSGR
	}
	return strings.Join(lines, "\n")
}

func (m Model) selectionRange() (int, int) {
	if m.selectionAnchor <= m.selectionHead {
		return m.selectionAnchor, m.selectionHead
	}
	return m.selectionHead, m.selectionAnchor
}

// copySelectionCmd extracts the highlighted lines and hands them to the
// clipboard: the tmux buffer plus the system clipboard through tmux's
// OSC 52 (-w) when inside tmux, pbcopy/xclip otherwise.
func (m Model) copySelectionCmd() tea.Cmd {
	start, end := m.selectionRange()
	lines := strings.Split(ansi.Strip(m.interactionContent), "\n")
	if start >= len(lines) {
		return nil
	}
	end = min(end, len(lines)-1)
	selected := make([]string, 0, end-start+1)
	for _, line := range lines[start : end+1] {
		selected = append(selected, strings.TrimRight(line, " "))
	}
	text := strings.Join(selected, "\n")
	count := end - start + 1
	return func() tea.Msg {
		if err := copyToClipboard(text); err != nil {
			return actionMsg{status: "Action failed", err: err}
		}
		label := "line"
		if count != 1 {
			label = "lines"
		}
		return actionMsg{status: fmt.Sprintf("Copied %d %s", count, label)}
	}
}

// copyToClipboard prefers tmux: load-buffer -w fills the tmux paste buffer
// and forwards to the system clipboard via OSC 52 in one step.
func copyToClipboard(text string) error {
	if os.Getenv("TMUX") != "" {
		command := exec.Command("tmux", "load-buffer", "-w", "-")
		command.Stdin = strings.NewReader(text)
		if err := command.Run(); err == nil {
			return nil
		}
	}
	for _, candidate := range [][]string{
		{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"},
	} {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		command := exec.Command(candidate[0], candidate[1:]...)
		command.Stdin = strings.NewReader(text)
		return command.Run()
	}
	return fmt.Errorf("no clipboard tool found (tmux, pbcopy, wl-copy, xclip)")
}

// paneAt maps a screen column to the dashboard pane rendered there.
func (m Model) paneAt(x int) pane {
	width := max(1, m.width-1)
	if width < 72 {
		return m.activePane
	}
	if m.ptyZoom && m.ptyEnabled {
		// Zoomed, the portal is the whole body: every column is its.
		return paneInteraction
	}
	workspaceWidth, agentWidth, _ := m.paneWidths(width)
	switch {
	case x < workspaceWidth:
		return paneWorkspaces
	case x < workspaceWidth+agentWidth:
		return paneAgents
	default:
		return paneInteraction
	}
}
