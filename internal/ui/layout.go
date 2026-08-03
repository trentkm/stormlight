package ui

// Pane arithmetic and the dashboard body composition.
// Split from model.go; see #34.

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
)

func (m Model) renderHeader() string {
	width := max(1, m.width-1)
	working := 0
	urgent := 0
	waiting := 0
	for _, managedAgent := range m.agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			working++
		}
		if !managedAgent.ProcessLive {
			continue
		}
		switch {
		case managedAgent.Attention.Urgent():
			urgent++
		case managedAgent.Attention == agent.AttentionWaiting:
			waiting++
		}
	}
	// No chrome: the wordmark's own gradient is the identity, floating on
	// the terminal background with the counters at the far edge.
	left := renderWordmark(m.shimmerPhaseOrRest())
	right := mutedStyle.Render(fmt.Sprintf("%d active", working))
	if waiting > 0 {
		right += "  " + lipgloss.NewStyle().Foreground(colorWaiting).
			Render(fmt.Sprintf("%d waiting", waiting))
	}
	if urgent > 0 {
		attentionLabel := fmt.Sprintf("%d need input", urgent)
		if urgent == 1 {
			attentionLabel = "1 needs input"
		}
		right += "  " + lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).
			Render(attentionLabel)
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + strings.Repeat(" ", gap) + right + " "
}

// bodyDimensions is the area modals and the dashboard share: the terminal
// minus the header, status, and hint chrome.
func (m Model) bodyDimensions() (int, int) {
	return max(1, m.width-1), max(1, m.height-4)
}

func (m Model) renderBody() string {
	width, contentHeight := m.bodyDimensions()
	dashboard := m.renderDashboardBody(width, contentHeight)
	switch m.mode {
	case modeDispatch:
		return overlayCentered(
			dashboard,
			m.renderDispatchModal(width, contentHeight),
			width,
			contentHeight,
		)
	case modeAddWorkspace:
		return overlayCentered(
			dashboard,
			m.renderAddWorkspaceModal(width, contentHeight),
			width,
			contentHeight,
		)
	case modeRename:
		return overlayCentered(
			dashboard,
			m.renderRenameModal(width, contentHeight),
			width,
			contentHeight,
		)
	case modeInfo:
		return overlayCentered(
			dashboard,
			m.renderInfoModal(width, contentHeight),
			width,
			contentHeight,
		)
	case modeHelp:
		return overlayCentered(
			dashboard,
			m.renderHelpModal(width, contentHeight),
			width,
			contentHeight,
		)
	}
	return dashboard
}

func (m Model) renderDashboardBody(width, contentHeight int) string {
	if width < 72 {
		return m.renderFocusedPane(width, contentHeight)
	}

	workspaceWidth, agentWidth, interactionWidth := m.paneWidths(width)

	dimWorkspaces, dimAgents, dimInteraction := m.paneDimmings(contentHeight)

	workspaces := m.renderPane(
		"Workspaces",
		"",
		// One extra column of slack keeps row text from touching the
		// hierarchy connector drawn in the pane's padding column.
		m.renderWorkspaces(max(1, workspaceWidth-3), contentHeight-1),
		workspaceWidth,
		contentHeight,
		m.activePane == paneWorkspaces,
		true,
		dimWorkspaces,
	)
	if workspaceRow, agentRow, ok := m.hierarchyConnectorRows(contentHeight); ok {
		workspaces = paintHierarchyConnector(
			workspaces,
			workspaceWidth,
			workspaceRow,
			agentRow,
		)
	}
	agents := m.renderPane(
		"Agents",
		m.selectedWorkspaceLabel(),
		m.renderAgents(max(1, agentWidth-2), contentHeight-1),
		agentWidth,
		contentHeight,
		m.activePane == paneAgents,
		true,
		dimAgents,
	)
	interaction := m.renderPane(
		"Spanreed",
		"",
		m.renderInteraction(
			max(1, interactionWidth-2),
			contentHeight-1,
		),
		interactionWidth,
		contentHeight,
		m.activePane == paneInteraction,
		false,
		dimInteraction,
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, workspaces, agents, interaction)
}

// paneDimmings decides how far each pane recedes. The lit path is the
// selection, not the focus: whichever pane holds the cursor, the two list
// panes stay dim except for their selected row, and the Spanreed never dims
// at all. A transcript is there to be read, and dimming it while the cursor
// is in the agent list only makes the reader tab over to finish the
// sentence. Focus is carried by the accent header rule and the single filled
// cursor row, which need no help from the body's brightness.
func (m Model) paneDimmings(contentHeight int) (
	workspaces, agents, interaction paneDimming,
) {
	workspaces = paneDimming{dim: true}
	if len(m.groups) > 0 {
		workspaces.keep = m.selectedRowRange(
			len(m.groups), m.workspaceCursor, contentHeight-1)
	}
	agents = paneDimming{dim: true}
	if list := m.agentsForSelectedWorkspace(); len(list) > 0 {
		agents.keep = m.selectedRowRange(
			len(list), m.agentCursor, contentHeight-1)
	}
	return workspaces, agents, paneDimming{}
}

func (m Model) hierarchyConnectorRows(contentHeight int) (int, int, bool) {
	if len(m.groups) == 0 || contentHeight < 2 {
		return 0, 0, false
	}
	agents := m.agentsForSelectedWorkspace()
	if len(agents) == 0 {
		return 0, 0, false
	}

	expanded := m.expandedRows()
	listHeight := contentHeight - 1
	workspaceCapacity := listRowCapacity(listHeight, expanded)
	workspaceStart, workspaceEnd := visibleRange(
		len(m.groups),
		m.workspaceCursor,
		workspaceCapacity,
	)
	agentCapacity := listRowCapacity(listHeight, expanded)
	agentStart, agentEnd := visibleRange(
		len(agents),
		m.agentCursor,
		agentCapacity,
	)
	if m.workspaceCursor < workspaceStart ||
		m.workspaceCursor >= workspaceEnd ||
		m.agentCursor < agentStart ||
		m.agentCursor >= agentEnd {
		return 0, 0, false
	}

	rowStep := 1
	if expanded {
		rowStep = 3
	}
	workspaceRow := 1 + (m.workspaceCursor-workspaceStart)*rowStep
	agentRow := 1 + (m.agentCursor-agentStart)*rowStep
	return workspaceRow, agentRow, true
}

func paintHierarchyConnector(
	paneContent string,
	width int,
	workspaceRow int,
	agentRow int,
) string {
	if width < 2 {
		return paneContent
	}
	lines := strings.Split(paneContent, "\n")
	if workspaceRow < 0 ||
		workspaceRow >= len(lines) ||
		agentRow < 0 ||
		agentRow >= len(lines) {
		return paneContent
	}

	// The connector lives in the padding column between the workspace text
	// and the pane divider, so the divider stays continuous and the gold
	// arc spans exactly its two endpoint rows — rounded caps, no spill.
	style := lipgloss.NewStyle().Foreground(colorWaiting)
	first := min(workspaceRow, agentRow)
	last := max(workspaceRow, agentRow)
	for row := first; row <= last; row++ {
		glyph := "│"
		switch {
		case workspaceRow == agentRow:
			glyph = "─"
		case row == workspaceRow && workspaceRow < agentRow:
			glyph = "╮"
		case row == workspaceRow:
			glyph = "╯"
		case row == agentRow && agentRow < workspaceRow:
			glyph = "╭"
		case row == agentRow:
			glyph = "╰"
		}
		lines[row] = replaceStyledCell(
			lines[row],
			width,
			width-2,
			glyph,
			style,
		)
	}
	return strings.Join(lines, "\n")
}

func replaceStyledCell(
	line string,
	width int,
	column int,
	value string,
	style lipgloss.Style,
) string {
	if width <= 0 || column < 0 || column >= width {
		return line
	}
	line = fitLine(line, width)
	before := ansi.Cut(line, 0, column)
	after := ansi.Cut(line, column+1, width)
	restore := ""
	if column+1 < width {
		restore = sgrStateAt(line, column+1)
	}
	return fitLine(before, column) +
		ansi.ResetStyle +
		style.Render(value) +
		ansi.ResetStyle +
		restore +
		fitLine(after, width-column-1)
}

func modalDimensions(
	availableWidth int,
	availableHeight int,
	preferredWidth int,
	preferredHeight int,
) (int, int) {
	widthMargin := 4
	heightMargin := 2
	if availableWidth < 28 {
		widthMargin = 0
	}
	if availableHeight < 12 {
		heightMargin = 0
	}
	width := min(preferredWidth, max(1, availableWidth-widthMargin))
	height := min(preferredHeight, max(1, availableHeight-heightMargin))
	return width, height
}

func renderModal(content string, width, height int) string {
	if width < 3 || height < 3 {
		return fitBlock(content, width, height)
	}
	innerWidth := width - 2
	innerHeight := height - 2
	content = fitBlock(content, innerWidth, innerHeight)
	return lipgloss.NewStyle().
		Width(innerWidth).
		Height(innerHeight).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorWaiting).
		Render(content)
}

func overlayCentered(background, foreground string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	backgroundLines := blockLines(background, width, height)
	foregroundLines := strings.Split(foreground, "\n")
	if len(foregroundLines) > height {
		foregroundLines = foregroundLines[:height]
	}
	foregroundWidth := 0
	for _, line := range foregroundLines {
		foregroundWidth = max(foregroundWidth, ansi.StringWidth(line))
	}
	foregroundWidth = min(foregroundWidth, width)
	if foregroundWidth == 0 || len(foregroundLines) == 0 {
		return strings.Join(backgroundLines, "\n")
	}

	left := max(0, (width-foregroundWidth)/2)
	top := max(0, (height-len(foregroundLines))/2)
	for index, foregroundLine := range foregroundLines {
		row := top + index
		if row >= len(backgroundLines) {
			break
		}
		foregroundLine = fitLine(foregroundLine, foregroundWidth)
		backgroundLine := backgroundLines[row]
		before := ansi.Cut(backgroundLine, 0, left)
		rightStart := left + foregroundWidth
		after := ansi.Cut(backgroundLine, rightStart, width)
		backgroundLines[row] = fitLine(before, left) +
			ansi.ResetStyle +
			foregroundLine +
			ansi.ResetStyle +
			sgrStateAt(backgroundLine, rightStart) +
			fitLine(after, width-rightStart)
	}
	return strings.Join(backgroundLines, "\n")
}

func fitBlock(content string, width, height int) string {
	return strings.Join(blockLines(content, width, height), "\n")
}

func blockLines(content string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return lines
}

func fitLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "")
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func sgrStateAt(value string, column int) string {
	if column <= 0 {
		return ""
	}
	var result strings.Builder
	var state byte
	width := 0
	for len(value) > 0 && width < column {
		sequence, cellWidth, consumed, nextState := ansi.DecodeSequence(
			value,
			state,
			nil,
		)
		if consumed <= 0 {
			break
		}
		if isSGRSequence(sequence) {
			result.WriteString(sequence)
		}
		width += cellWidth
		value = value[consumed:]
		state = nextState
	}
	return result.String()
}

func (m Model) renderFocusedPane(width, height int) string {
	switch m.activePane {
	case paneAgents:
		contextLabel := m.selectedWorkspaceLabel()
		if width < 72 {
			contextLabel = strings.TrimSpace(contextLabel + "  ›")
		}
		return m.renderPane(
			"Agents",
			contextLabel,
			m.renderAgents(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
			paneDimming{},
		)
	case paneInteraction:
		return m.renderPane(
			"Spanreed",
			"‹",
			m.renderInteraction(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
			paneDimming{},
		)
	default:
		return m.renderPane(
			"Workspaces",
			"Agents ›",
			m.renderWorkspaces(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
			paneDimming{},
		)
	}
}

// resizeColumns grows (>) or shrinks (<) the focused pane. The Spanreed
// has no width of its own to store — it takes what the lists leave — so
// its adjustments trade columns with the Agents pane. A press that the
// clamps would swallow reverts, keeping the stored adjustment honest, and
// the layout persists across relaunches.
func (m Model) resizeColumns(key string) (tea.Model, tea.Cmd) {
	width := max(1, m.width-1)
	if width < 72 {
		return m, nil
	}
	delta := 2
	if key == "<" {
		delta = -2
	}
	previous := m.columns
	beforeW, beforeA, beforeI := m.paneWidths(width)
	switch m.activePane {
	case paneWorkspaces:
		m.columns.WorkspaceAdjust += delta
	case paneAgents:
		m.columns.AgentAdjust += delta
	case paneInteraction:
		m.columns.AgentAdjust -= delta
	}
	afterW, afterA, afterI := m.paneWidths(width)
	if afterW == beforeW && afterA == beforeA && afterI == beforeI {
		m.columns = previous
		return m, nil
	}
	// Snap the stored adjustment to what the clamps actually granted, so a
	// press swallowed at a boundary never becomes debt the user has to
	// press their way back out of.
	baseW, baseA, _ := basePaneWidths(width)
	m.columns.WorkspaceAdjust = afterW - baseW
	m.columns.AgentAdjust = afterA - baseA
	saveColumnPrefs(m.columns)
	interactionWidth, contentHeight := m.interactionDimensions()
	m.interaction.Width = interactionWidth
	m.interaction.Height = contentHeight
	m.status = fmt.Sprintf("Columns %d · %d · %d", afterW, afterA, afterI)
	return m, tea.Batch(m.loadInteractionCmd(), m.syncAgentWindowsCmd())
}

// undimmedRows marks body lines that stay at full strength inside a
// dimmed pane: the selected path rows lighting the way to the focus.
type undimmedRows struct{ start, count int }

// paneDimming says how a pane's body recedes. The zero value never dims —
// panes whose whole content is meant to be read regardless of focus. A
// dimming pane falls to the terminal's faint attribute everywhere but keep.
type paneDimming struct {
	dim  bool
	keep undimmedRows
}

// selectedRowRange is the body-line range a list's selected entry occupies,
// or a zero range when the selection is scrolled out of view.
func (m Model) selectedRowRange(total, cursor, listHeight int) undimmedRows {
	expanded := m.expandedRows()
	capacity := listRowCapacity(listHeight, expanded)
	start, end := visibleRange(total, cursor, capacity)
	if cursor < start || cursor >= end {
		return undimmedRows{}
	}
	step, size := 1, 1
	if expanded {
		step, size = 3, 2
	}
	return undimmedRows{start: (cursor - start) * step, count: size}
}

func (m Model) renderPane(
	label string,
	contextLabel string,
	content string,
	width int,
	height int,
	active bool,
	borderRight bool,
	dimming paneDimming,
) string {
	innerWidth := max(1, width)
	style := lipgloss.NewStyle().Width(innerWidth).Height(height).MaxHeight(height)
	if borderRight {
		// Dividers are structure, not state: focus is shown by the header
		// underline and the single hot cursor row, never the frame.
		innerWidth = max(1, width-1)
		style = style.Width(innerWidth).
			BorderStyle(lipgloss.NormalBorder()).
			BorderRight(true).
			BorderForeground(colorBorder)
	}
	header := renderPaneHeader(label, contextLabel, innerWidth, active)
	if dimming.dim {
		content = dimPane(content, dimming.keep)
	}
	body := lipgloss.NewStyle().
		Width(innerWidth).
		MaxWidth(innerWidth).
		Render(content)
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

// dimPane lays the terminal's faint attribute over a list pane so its
// selected row is the only one at full strength. Colors survive — an amber
// attention marker still reads as amber, just recessed. Every SGR sequence
// inside the block re-arms the faint (a reset would otherwise cancel it)
// and drops bold, which defeats faint on most terminals. Rows inside keep
// stay untouched: the selected workspace and agent light the path to the
// transcript.
func dimPane(content string, keep undimmedRows) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		if keep.count > 0 && index >= keep.start && index < keep.start+keep.count {
			continue
		}
		line = sgrSequencePattern.ReplaceAllStringFunc(line, func(seq string) string {
			return "\x1b[" + dimParams(seq[2:len(seq)-1]) + "m"
		})
		lines[index] = "\x1b[2m" + line + "\x1b[22m"
	}
	return strings.Join(lines, "\n")
}

var sgrSequencePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// dimParams rewrites one SGR parameter list: standalone bold (1) is
// dropped and faint (2) is appended. Extended color introducers (38/48/58)
// consume their arguments verbatim, so a color component that happens to
// be 1 or 2 is never touched.
func dimParams(params string) string {
	if params == "" {
		return "0;2"
	}
	tokens := strings.Split(params, ";")
	out := make([]string, 0, len(tokens)+1)
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch token {
		case "38", "48", "58":
			out = append(out, token)
			if index+1 >= len(tokens) {
				break
			}
			consume := 0
			switch tokens[index+1] {
			case "2":
				consume = 4 // mode + r + g + b
			case "5":
				consume = 2 // mode + palette index
			}
			for step := 1; step <= consume && index+step < len(tokens); step++ {
				out = append(out, tokens[index+step])
			}
			index += consume
		case "1":
			// Bold defeats faint; the dim layer wins in inactive panes.
		default:
			out = append(out, token)
		}
	}
	return strings.Join(append(out, "2"), ";")
}

// renderPaneHeader underlines the entire header cell, padding included, so
// the header reads as a full-width ruled box top rather than floating text.
func renderPaneHeader(label, contextLabel string, width int, active bool) string {
	underlined := func(style lipgloss.Style) lipgloss.Style {
		return style.Underline(true)
	}
	fill := underlined(lipgloss.NewStyle().Foreground(colorBorder))

	left := underlined(mutedStyle.Copy().Bold(true)).
		Render(truncate(" "+label, width))
	if active {
		// The active pane's header rule renders in accent — the pane's
		// "selected tab" indicator. The rule alone carries the signal; no
		// rail, no extra chrome.
		fill = underlined(lipgloss.NewStyle().Foreground(colorAccent))
		left = underlined(titleStyle.Copy()).
			Render(truncate(" "+label, width))
	}

	remaining := width - lipgloss.Width(left) - 2
	if strings.TrimSpace(contextLabel) == "" || remaining < 4 {
		pad := max(0, width-lipgloss.Width(left))
		return left + fill.Render(strings.Repeat(" ", pad))
	}

	rightStyle := underlined(mutedStyle.Copy())
	if strings.ContainsAny(contextLabel, "‹›") {
		rightStyle = underlined(accentStyle.Copy())
	}
	right := rightStyle.Render(truncate(contextLabel, remaining))
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	tail := max(0, width-lipgloss.Width(left)-gap-lipgloss.Width(right))
	return left + fill.Render(strings.Repeat(" ", gap)) + right +
		fill.Render(strings.Repeat(" ", tail))
}

func (m Model) interactionDimensions() (int, int) {
	contentHeight := max(1, m.height-9)
	width := max(1, m.width-1)
	if width < 72 {
		return max(1, width-2), contentHeight
	}
	_, _, interactionWidth := m.paneWidths(width)
	return max(1, interactionWidth-2), contentHeight
}

// basePaneWidths splits a dashboard row between the three panes. The
// Spanreed transcript is the pane people actually read, so it takes
// everything the two list panes don't need.
func basePaneWidths(width int) (workspaceWidth, agentWidth, interactionWidth int) {
	workspaceWidth = clamp(width*20/100, 16, 26)
	agentWidth = clamp(width*28/100, 26, 40)
	interactionWidth = width - workspaceWidth - agentWidth
	if interactionWidth < 24 {
		deficit := 24 - interactionWidth
		agentWidth = max(22, agentWidth-deficit)
		interactionWidth = width - workspaceWidth - agentWidth
	}
	return workspaceWidth, agentWidth, interactionWidth
}

// paneWidths applies the user's < > adjustments on top of the built-in
// split, clamped so every pane stays usable at any terminal width.
func (m Model) paneWidths(width int) (workspaceWidth, agentWidth, interactionWidth int) {
	workspaceWidth, agentWidth, _ = basePaneWidths(width)
	workspaceWidth = clamp(
		workspaceWidth+m.columns.WorkspaceAdjust, 14, max(14, width*40/100))
	agentWidth = clamp(
		agentWidth+m.columns.AgentAdjust, 20, max(20, width*50/100))
	interactionWidth = width - workspaceWidth - agentWidth
	if interactionWidth < 24 {
		deficit := 24 - interactionWidth
		agentWidth = max(20, agentWidth-deficit)
		interactionWidth = width - workspaceWidth - agentWidth
	}
	if interactionWidth < 24 {
		deficit := 24 - interactionWidth
		workspaceWidth = max(14, workspaceWidth-deficit)
		interactionWidth = width - workspaceWidth - agentWidth
	}
	return workspaceWidth, agentWidth, interactionWidth
}

func (m Model) expandedRows() bool {
	return m.rowsExpanded
}

func (m Model) visibleRows() int {
	if m.activePane == paneInteraction && m.interaction.Height > 0 {
		return m.interaction.Height
	}
	return max(1, (m.height-3)/2)
}
