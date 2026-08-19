package ui

// Pane arithmetic and the dashboard body composition.
// Split from model.go; see #34.

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
)

func (m Model) renderHeader() string {
	width := max(1, m.width-1)
	stats := agent.Count(m.agents)
	// No chrome: the wordmark's own gradient is the identity, floating on
	// the terminal background with the counters at the far edge.
	left := renderWordmark(m.shimmerPhaseOrRest())
	// The counters speak the rows' own language: same glyphs, same colors as
	// statusVisual paints down the agent list, so the header doubles as the
	// legend for everything below it.
	counts := []string{
		renderHeaderCount("●", colorWorking(), false,
			fmt.Sprintf("%d working", stats.Working)),
	}
	if stats.Waiting > 0 {
		counts = append(counts, renderHeaderCount("○", colorWaiting(), false,
			fmt.Sprintf("%d waiting", stats.Waiting)))
	}
	if stats.Urgent > 0 {
		attentionLabel := fmt.Sprintf("%d need input", stats.Urgent)
		if stats.Urgent == 1 {
			attentionLabel = "1 needs input"
		}
		counts = append(counts,
			renderHeaderCount("!", colorWaiting(), true, attentionLabel))
	}
	right := strings.Join(counts, mutedStyle().Render("  ·  "))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + strings.Repeat(" ", gap) + right + " "
}

// renderHeaderCount pairs a count with the glyph the agent rows use for that
// state. The glyph carries the color and the words stay muted, except for the
// input tier, which is the one thing in the header worth raising a voice over.
func renderHeaderCount(symbol string, color color.Color, loud bool, label string) string {
	symbolStyle := lipgloss.NewStyle().Foreground(color)
	labelStyle := mutedStyle()
	if loud {
		symbolStyle = symbolStyle.Bold(true)
		labelStyle = lipgloss.NewStyle().Foreground(color).Bold(true)
	}
	return symbolStyle.Render(symbol) + " " + labelStyle.Render(label)
}

// bodyDimensions is the area modals and the dashboard share: the terminal
// minus the header row and the footer's rule and hints — three rows of
// chrome, so the body's floor meets the footer and the footer meets the
// terminal's bottom edge.
func (m Model) bodyDimensions() (int, int) {
	return max(1, m.width-1), max(1, m.height-3)
}

func (m Model) renderBody() string {
	width, contentHeight := m.bodyDimensions()
	body := m.renderModeBody(width, contentHeight)
	if m.overlay != nil {
		// The floating program draws over everything, the mode's own
		// modal included — Yazi opened from the dispatch form floats
		// above the form it will answer.
		body = overlayCentered(
			body,
			m.renderOverlayPopup(),
			width,
			contentHeight,
		)
	}
	// The failure card sits above even that: whatever is on screen, a
	// thing that just went wrong is the most important thing on it.
	return overlayBottomLeft(
		body, m.renderAlertCard(width, contentHeight), width, contentHeight, 1,
	)
}

func (m Model) renderModeBody(width, contentHeight int) string {
	dashboard := m.renderDashboardBody(width, contentHeight)
	// A modal centers in what the failure card leaves, not under it. The
	// card is anchored to the foot of the body and the two would otherwise
	// want the same rows — and the card would win, covering the form's
	// last lines while the only key it named cancelled that form.
	region := m.modalRegion(width, contentHeight)
	var modal string
	switch m.mode {
	case modeDispatch:
		modal = m.renderDispatchModal(width, region)
	case modeAddWorkspace:
		modal = m.renderAddWorkspaceModal(width, region)
	case modeRename:
		modal = m.renderRenameModal(width, region)
	case modeMark:
		modal = m.renderMarkModal(width, region)
	case modeInfo:
		modal = m.renderInfoModal(width, region)
	case modeHelp:
		modal = m.renderHelpModal(width, region)
	case modeHistory:
		modal = m.renderHistoryModal(width, region)
	case modeAlert:
		modal = m.renderAlertModal(width, region)
	default:
		return dashboard
	}
	return overlayCenteredIn(dashboard, modal, width, contentHeight, region)
}

func (m Model) renderDashboardBody(width, contentHeight int) string {
	if width < 72 {
		return m.renderFocusedPane(width, contentHeight)
	}
	if m.ptyZoom && m.ptyEnabled {
		if selected, ok := m.selectedAgent(); ok {
			// Zoom collapses the sidebars rather than entering a separate
			// mode: the same pane pipeline, the portal spanning the full
			// frame, the header's counts still overhead.
			return m.renderPane(
				"",
				"",
				m.renderInteraction(width, contentHeight-1),
				paneFrame{
					width:  width,
					height: contentHeight,
					active: true,
					header: func(w int) string {
						return m.renderTerminalBar(selected, w)
					},
				},
			)
		}
	}

	workspaceWidth, agentWidth, interactionWidth := m.paneWidths(width)
	listHeight := contentHeight - 2

	dimWorkspaces, dimAgents, dimInteraction := m.paneDimmings(contentHeight)

	// A hairline divides Workspaces from Agents, with air on both sides of
	// it — the treatment yazi gives its miller columns, and the reason two
	// adjacent scrolling lists read as columns rather than as a grid. The
	// rule was never the problem; a rule pressed flush against both lists
	// was. It stays subordinate to the plane seam by every means available:
	// light where that one is heavy, and painted in the header band's own
	// faded gradient where that one takes the accent.
	workspaces := m.renderPane(
		"Workspaces",
		"",
		// Past the margin: a blank column, the connector, another blank,
		// then the rule. The arc reaches for the agents without crowding
		// the divider, and the divider keeps air on both sides.
		m.renderWorkspaces(max(1, workspaceWidth-5), listHeight),
		paneFrame{
			width:   workspaceWidth,
			height:  contentHeight,
			active:  m.activePane == paneWorkspaces,
			margins: paneMargins{top: true, left: true},
			dimming: dimWorkspaces,
			dimSeam: m.terminalFocused(),
			header: func(w int) string {
				return m.renderWorkspacesBar("", w)
			},
		},
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
		"",
		m.renderAgents(max(1, agentWidth-2), listHeight),
		paneFrame{
			width:   agentWidth,
			height:  contentHeight,
			active:  m.activePane == paneAgents,
			edges:   paneEdges{right: seamPlane},
			margins: paneMargins{top: true, left: true},
			dimming: dimAgents,
			dimSeam: m.terminalFocused(),
			header: func(w int) string {
				return m.renderAgentsBar("", w)
			},
		},
	)
	interactionFrame := paneFrame{
		width:   interactionWidth,
		height:  contentHeight,
		active:  m.activePane == paneInteraction,
		edges:   paneEdges{gutter: true},
		margins: paneMargins{top: true},
		dimming: dimInteraction,
	}
	if selected, ok := m.selectedAgent(); ok && m.ptyEnabled {
		// Terminal view: the window bar IS the pane's title row, and the
		// top margin goes with it — the portal starts on the frame's
		// second screen row.
		interactionFrame.header = func(w int) string {
			return m.renderTerminalBar(selected, w)
		}
		interactionFrame.margins = paneMargins{}
	} else {
		// No terminal to name: the band still runs to the right edge —
		// quiet over the empty portal, labeled in the transcript view.
		label := ""
		if _, ok := m.selectedAgent(); ok && !m.ptyEnabled {
			label = "Transcript"
		}
		interactionFrame.header = func(w int) string {
			return m.renderQuietBar(label, "", w)
		}
	}
	// The pane has no name row: with an agent the window bar is the title,
	// and without one the empty state speaks for itself. "Spanreed" is a
	// name for the docs, not the screen.
	interaction := m.renderPane(
		"",
		"",
		m.renderInteraction(
			max(1, interactionWidth-3),
			listHeight,
		),
		interactionFrame,
	)
	// No rule between Workspaces and Agents: the hierarchy connector and
	// the shared band already say the two lists are one catalog; the gap
	// column breathes instead of dividing.
	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		workspaces,
		agents,
		interaction,
	)
	// The strip is painted over the joined row as one piece: continuous
	// to the right edge, the seam columns under the shared ground.
	return paintTitleBand(body,
		m.renderTitleBand(workspaceWidth, agentWidth, interactionWidth))
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
			len(m.groups), m.workspaceCursor, contentHeight-2)
	}
	agents = paneDimming{dim: true}
	if list := m.agentsForSelectedWorkspace(); len(list) > 0 {
		agents.keep = m.selectedRowRange(
			len(list), m.agentCursor, contentHeight-2)
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
	listHeight := contentHeight - 2
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
	// Two rows of chrome above the first list row: the header band and the
	// blank row the inset opens with.
	workspaceRow := 2 + (m.workspaceCursor-workspaceStart)*rowStep
	agentRow := 2 + (m.agentCursor-agentStart)*rowStep
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

	// The connector lives in the column just inside the rule, so the arc
	// reaches toward the agent it names without crowding the divider,
	// spanning exactly its two endpoint rows with rounded caps. It wears
	// the strip's silver: the arc states a selection relationship, and
	// selection is one word everywhere.
	style := lipgloss.NewStyle().Foreground(colorBand())
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
			width-3,
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

// Modals curve their corners. Square corners are what the dashboard's own
// rules and seams are drawn with, so an overlay that shares them reads as
// another region of the layout; the arc says this floats above it.
func renderModal(content string, width, height int) string {
	if width < 3 || height < 3 {
		return fitBlock(content, width, height)
	}
	innerWidth := width - 2
	innerHeight := height - 2
	content = fitBlock(content, innerWidth, innerHeight)
	// Width and Height are the modal's outside edges: v2 sizes a box the way
	// CSS border-box does, counting the frame within the number rather than
	// adding it on top. The content is still fitted to the inside.
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorWaiting()).
		Render(content)
}

func overlayCentered(background, foreground string, width, height int) string {
	return overlayCenteredIn(background, foreground, width, height, height)
}

// overlayCenteredIn centers a block horizontally across the background and
// vertically within its top region rows — the whole height, unless
// something else has claimed the rows below it.
func overlayCenteredIn(
	background, foreground string,
	width, height, region int,
) string {
	return overlayComposite(background, foreground, width, height,
		func(foregroundWidth, foregroundHeight int) (int, int) {
			return max(0, (width-foregroundWidth)/2),
				max(0, (min(region, height)-foregroundHeight)/2)
		})
}

// overlayBottomLeft floats a block against the foot of the body, indented
// from the left edge — where a message about what just happened belongs,
// near the footer that answers for it, and out of the way of the rows the
// cursor is working in.
func overlayBottomLeft(
	background, foreground string,
	width, height, indent int,
) string {
	return overlayComposite(background, foreground, width, height,
		func(foregroundWidth, foregroundHeight int) (int, int) {
			return clamp(indent, 0, max(0, width-foregroundWidth)),
				max(0, height-foregroundHeight)
		})
}

// overlayComposite paints foreground over background at the position the
// placer picks for its measured size. Nothing reflows: the background keeps
// its dimensions and the block covers what it covers.
func overlayComposite(
	background, foreground string,
	width, height int,
	place func(foregroundWidth, foregroundHeight int) (int, int),
) string {
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

	left, top := place(foregroundWidth, len(foregroundLines))
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
			paneFrame{
				width:  width,
				height: height,
				active: true,
				header: func(w int) string {
					return m.renderAgentsBar(contextLabel, w)
				},
			},
		)
	case paneInteraction:
		frame := paneFrame{
			width:  width,
			height: height,
			active: true,
		}
		if selected, ok := m.selectedAgent(); ok && m.ptyEnabled {
			frame.header = func(w int) string {
				return m.renderTerminalBar(selected, w)
			}
		} else {
			frame.header = func(w int) string {
				return m.renderQuietBar("", "‹", w)
			}
		}
		return m.renderPane(
			"",
			"‹",
			m.renderInteraction(max(1, width-2), height-1),
			frame,
		)
	default:
		return m.renderPane(
			"Workspaces",
			"Agents ›",
			m.renderWorkspaces(max(1, width-2), height-1),
			paneFrame{
				width:  width,
				height: height,
				active: true,
				header: func(w int) string {
					return m.renderWorkspacesBar("Agents ›", w)
				},
			},
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
	m.interaction.SetWidth(interactionWidth)
	m.interaction.SetHeight(contentHeight)
	if m.ptyEnabled {
		return m, m.ensurePTYCmd()
	}
	return m, tea.Batch(m.loadInteractionCmd(), m.ensurePTYCmd())
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

// paneSeam is the rule down a pane's right edge. Not every boundary divides
// the same kind of thing, and the rule says which is which.
type paneSeam int

const (
	// seamNone ends a pane at the terminal's edge — nothing to divide.
	seamNone paneSeam = iota
	// seamColumn separates two views of one hierarchy: workspaces and the
	// agents inside the selected one. A hairline, because crossing it is
	// just moving down the tree.
	seamColumn
	// seamPlane separates the catalog from the Spanreed. Left of it is a
	// file explorer — a tree you browse. Right of it is the control plane
	// where an agent is read and driven. The rule goes heavy and the pane
	// beyond it opens with a column of air (see paneEdges.gutter), so the
	// two halves read as different surfaces rather than a third column.
	seamPlane
)

// border returns the box-drawing rule a seam paints down the pane edge.
func (s paneSeam) border() lipgloss.Border {
	border := lipgloss.NormalBorder()
	if s == seamPlane {
		// The right one-eighth block hugs its cell's right edge — the
		// same pixel where the band's light ends above it — so the rule
		// is flush with the agents tab instead of floating at the
		// center of its column the way a box-drawing vertical does.
		border.Right = "▕"
	}
	return border
}

// paneEdges describes both sides of a pane's frame. A plane seam is drawn in
// two halves by two panes: the pane on its left owns the heavy rule, the pane
// on its right owns the column of air that follows it.
type paneEdges struct {
	right  paneSeam
	gutter bool
}

// paneMargins is the air a pane keeps inside its own frame: a blank row
// under the header band, and a column between the frame and the text.
type paneMargins struct {
	top  bool
	left bool
}

// paneFrame is everything about a pane that isn't its text.
type paneFrame struct {
	width   int
	height  int
	active  bool
	edges   paneEdges
	margins paneMargins
	dimming paneDimming
	// header, when set, replaces the pane's title row entirely — the
	// terminal view mounts its window bar there, so the portal starts on
	// the frame's very first row. Called with the pane's content width.
	header func(width int) string
	// dimSeam flips the pane's right seam to the accent while the
	// Spanreed terminal holds the keyboard: the rule joins the portal's
	// lit segment instead of receding.
	dimSeam bool
}

func (m Model) renderPane(
	label string,
	contextLabel string,
	content string,
	frame paneFrame,
) string {
	width, height := frame.width, frame.height
	edges, dimming := frame.edges, frame.dimming
	innerWidth := max(1, width)
	style := lipgloss.NewStyle().Width(innerWidth).Height(height).MaxHeight(height)
	if edges.right != seamNone {
		// Dividers are structure, not state: a seam looks the same whichever
		// pane holds the cursor, so the accent on the plane seam reads as a
		// permanent fixture rather than a focus signal. Focus stays with the
		// header underline and the single hot cursor row.
		//
		// The style keeps the pane's full width because v2 counts the seam
		// inside it; innerWidth is what is left for content once the seam
		// has taken its column.
		innerWidth = max(1, width-1)
		// The plane seam wears the band's icy blue while the keyboard is
		// on the roster's side, and flips to the accent when it crosses
		// into the portal — the rule and the terminal's lit segment are
		// one light, telling the same story from top to bottom.
		seamForeground := colorBand()
		if frame.dimSeam {
			seamForeground = colorAccent()
		}
		style = style.Width(max(1, width)).
			BorderStyle(edges.right.border()).
			BorderRight(true).
			BorderForeground(seamForeground)
	}
	// The gutter is the far half of a plane seam, so it comes out of this
	// pane's own columns: every body row starts one cell in, leaving the
	// heavy rule with air on its far side.
	contentWidth := innerWidth
	if edges.gutter {
		contentWidth = max(1, innerWidth-1)
		style = style.PaddingLeft(1)
	}
	header := frame.header(contentWidth)
	if dimming.dim {
		content = dimPane(content, dimming.keep)
	}
	bodyStyle := lipgloss.NewStyle().Width(contentWidth).MaxWidth(contentWidth)
	if frame.margins.top {
		// Every pane opens a row below the band, so its content sits inside
		// the frame rather than pressed against it. The blank row is
		// prepended after dimming, which keeps the undimmed range indexed
		// against the rows themselves.
		content = "\n" + content
	}
	if frame.margins.left {
		bodyStyle = bodyStyle.PaddingLeft(1)
	}
	return style.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, bodyStyle.Render(content)),
	)
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

func (m Model) interactionDimensions() (int, int) {
	contentHeight := max(1, m.height-9)
	width := max(1, m.width-1)
	if width < 72 {
		return max(1, width-2), contentHeight
	}
	_, _, interactionWidth := m.paneWidths(width)
	// One column goes to the plane seam's gutter, two to the pane's own
	// slack; renderDashboardBody splits it the same way.
	return max(1, interactionWidth-3), contentHeight
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
	if m.activePane == paneInteraction && m.interaction.Height() > 0 {
		return m.interaction.Height()
	}
	return max(1, (m.height-3)/2)
}
