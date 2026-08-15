package ui

// The title band: one strip across the top of the body, a segment per
// pane, notched by the pane seams. The terminal's window bar started it;
// the roster panes joined so the dashboard speaks one heading language.
// The focused pane's segment takes the accent — the band doubles as the
// dashboard's tab bar, and "where is the keyboard" has one answer in one
// place.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// bandSegment renders one segment of the title band. The label yields
// first, the gap absorbs what remains, and the meta keeps its full width
// at the right edge. symbol, when present, leads the segment in its own
// style — the terminal's status glyph.
func bandSegment(
	symbol string,
	symbolStyle *lipgloss.Style,
	label, meta string,
	focused bool,
	width int,
) string {
	band := lipgloss.NewStyle().Background(colorBand())
	text := band.Foreground(colorPortalInk())
	glyph := ""
	lead := 0
	if symbol != "" {
		lead = 1
		glyph = symbolStyle.Background(colorBand()).Render(symbol)
	}
	if focused {
		band = lipgloss.NewStyle().Background(colorAccent())
		text = band.Foreground(colorPortalInk())
		if symbol != "" {
			glyph = band.Foreground(colorPortalInk()).Bold(true).Render(symbol)
		}
	}
	metaWidth := lipgloss.Width(meta)
	label = truncate(label, max(1, width-metaWidth-4-lead))
	gap := max(1, width-3-lead-lipgloss.Width(label)-metaWidth)
	return band.Render(" ") + glyph +
		text.Render(" "+label+strings.Repeat(" ", gap)+meta+" ")
}

// renderWorkspacesBar is the band's first segment: the pane's name and
// how many workspaces the catalog holds.
func (m Model) renderWorkspacesBar(meta string, width int) string {
	if meta == "" {
		meta = fmt.Sprintf("%d", len(m.catalogWorkspaces))
	}
	return bandSegment("", nil, "Workspaces", meta,
		m.mode == modeNormal && m.activePane == paneWorkspaces, width)
}

// renderAgentsBar is the middle segment: the pane's name and the size of
// the selected workspace's roster.
func (m Model) renderAgentsBar(meta string, width int) string {
	if meta == "" {
		meta = fmt.Sprintf("%d", len(m.agentsForSelectedWorkspace()))
	}
	return bandSegment("", nil, "Agents", meta,
		m.mode == modeNormal && m.activePane == paneAgents, width)
}

// renderTitleBand composes the strip as one string at full width — the
// band is continuous because it is one surface, not three headers meeting
// at seams. Each pane's segment is laid at its pane's width; the seam
// columns disappear under the shared ground.
func (m Model) renderTitleBand(workspaceWidth, agentWidth, interactionWidth int) string {
	right := ""
	if selected, ok := m.selectedAgent(); ok && m.ptyEnabled {
		right = m.renderTerminalBar(selected, interactionWidth)
	} else {
		label := ""
		if _, ok := m.selectedAgent(); ok && !m.ptyEnabled {
			label = "Transcript"
		}
		right = m.renderQuietBar(label, "", interactionWidth)
	}
	return m.renderWorkspacesBar("", workspaceWidth) +
		m.renderAgentsBar("", agentWidth) +
		right
}

// paintTitleBand replaces the body's first row with the composed strip.
func paintTitleBand(body, band string) string {
	rows := strings.SplitN(body, "\n", 2)
	if len(rows) < 2 {
		return band
	}
	return band + "\n" + rows[1]
}

// renderQuietBar continues the band where the terminal segment would be
// when there is no terminal to name: the empty portal, the transcript
// reading view. The strip stays whole; the label says which view this is.
func (m Model) renderQuietBar(label, meta string, width int) string {
	return bandSegment("", nil, label, meta,
		m.mode == modeNormal && m.activePane == paneInteraction, width)
}
