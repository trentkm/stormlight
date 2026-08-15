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

// gridCell names one cell of the terminal grid.
type gridCell struct{ row, col int }

func (a gridCell) before(b gridCell) bool {
	return a.row < b.row || (a.row == b.row && a.col <= b.col)
}

// handleTerminalMouse is the left button over the terminal view. A click
// focuses the pane under the pointer and forwards to a program that asked
// for the mouse; a drag is Stormlight's selection — character-precise,
// tmux style — copied on release.
func (m Model) handleTerminalMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	widget, hasWidget := m.selectedPTY()
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.activePane = m.paneAt(mouse.X)
		cell, ok := m.terminalCellAt(mouse.X, mouse.Y)
		if !ok {
			m.ptySelecting = false
			return m, nil
		}
		m.ptySelecting = true
		m.ptySelDragged = false
		m.ptySelAnchor, m.ptySelHead = cell, cell
		return m, nil
	case tea.MouseMotionMsg:
		if !m.ptySelecting {
			return m, nil
		}
		if cell, ok := m.terminalCellAt(mouse.X, mouse.Y); ok {
			if cell != m.ptySelAnchor {
				m.ptySelDragged = true
			}
			m.ptySelHead = cell
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

// terminalCellAt maps screen coordinates to a grid cell for selection,
// clamping coordinates that leave the grid mid-drag — the way every
// terminal treats a drag past its edges.
func (m Model) terminalCellAt(x, y int) (gridCell, bool) {
	if col, row, ok := m.gridCellAt(x, y); ok {
		return gridCell{row: row, col: col}, true
	}
	gridWidth, gridHeight := m.ptyGridDimensions()
	width := max(1, m.width-1)
	if width < 72 {
		return gridCell{}, false
	}
	left := 0
	if !m.ptyZoom {
		workspaceWidth, agentWidth, _ := m.paneWidths(width)
		left = workspaceWidth + agentWidth + 1
	}
	if x < left {
		return gridCell{}, false
	}
	return gridCell{
		row: clamp(y-ptyGridTop, 0, gridHeight-1),
		col: clamp(x-left, 0, gridWidth-1),
	}, true
}

// selectionSpan orders the anchor and head.
func (m Model) selectionSpan() (gridCell, gridCell) {
	if m.ptySelAnchor.before(m.ptySelHead) {
		return m.ptySelAnchor, m.ptySelHead
	}
	return m.ptySelHead, m.ptySelAnchor
}

// copyTerminalSelectionCmd extracts the selected span as plain text —
// first line from the anchor column, middles whole, last line to the
// head column, trailing blanks trimmed per line.
func (m Model) copyTerminalSelectionCmd() tea.Cmd {
	widget, ok := m.selectedPTY()
	if !ok {
		return nil
	}
	start, end := m.selectionSpan()
	lines := widget.Text()
	if start.row >= len(lines) {
		return nil
	}
	end.row = min(end.row, len(lines)-1)
	selected := make([]string, 0, end.row-start.row+1)
	for row := start.row; row <= end.row; row++ {
		runes := []rune(lines[row])
		from, to := 0, len(runes)
		if row == start.row {
			from = min(start.col, len(runes))
		}
		if row == end.row {
			to = min(end.col+1, len(runes))
		}
		if from > to {
			from = to
		}
		selected = append(selected,
			strings.TrimRight(string(runes[from:to]), " "))
	}
	text := strings.Join(selected, "\n")
	count := len(selected)
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

// paintTerminalSelection reverses the video of the selected span over the
// rendered grid, cell-precise: the same surgery overlayCentered performs,
// aimed at a highlight instead of a modal.
func paintTerminalSelection(view string, start, end gridCell, gridWidth int) string {
	lines := strings.Split(view, "\n")
	for row := start.row; row <= end.row && row < len(lines); row++ {
		from, to := 0, gridWidth
		if row == start.row {
			from = start.col
		}
		if row == end.row {
			to = end.col + 1
		}
		line := lines[row]
		before := ansi.Cut(line, 0, from)
		mid := ansi.Strip(ansi.Cut(line, from, to))
		if width := to - from - len([]rune(ansi.Strip(ansi.Cut(line, from, to)))); width > 0 {
			mid += strings.Repeat(" ", width)
		}
		after := ansi.Cut(line, to, gridWidth)
		lines[row] = before +
			searchMatchSGR + mid + searchResetSGR +
			sgrStateAt(line, to) + after
	}
	return strings.Join(lines, "\n")
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
