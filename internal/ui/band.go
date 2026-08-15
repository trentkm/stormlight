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

// segmentRow lays out one segment of the title band: an optional leading
// glyph cell, a label that yields first, a gap that absorbs what remains,
// and the meta at full width against the right edge.
func segmentRow(
	glyph string,
	lead int,
	label, meta string,
	band, text lipgloss.Style,
	width int,
) string {
	// Meta yields before it bursts the segment; the label yields to the
	// meta, down to nothing; the gap absorbs whatever remains. The three
	// budgets always sum to the width — a segment never overflows.
	meta = truncate(meta, max(1, width-4-lead))
	metaWidth := lipgloss.Width(meta)
	labelBudget := width - metaWidth - 4 - lead
	if labelBudget < 1 {
		label = ""
	} else {
		label = truncate(label, labelBudget)
	}
	gap := max(0, width-3-lead-lipgloss.Width(label)-metaWidth)
	return band.Render(" ") + glyph +
		text.Render(" "+label+strings.Repeat(" ", gap)+meta+" ")
}

// bandSegment is the terminal portion of the strip: the accent receded
// at rest, the full accent while the keyboard is inside — dimming and
// brightening in the same language as the roster's silver, while staying
// the portal's own blue. symbol, when present, is the status glyph.
func bandSegment(
	symbol string,
	symbolStyle *lipgloss.Style,
	label, meta string,
	focused bool,
	width int,
) string {
	band := lipgloss.NewStyle().Background(colorAccentDim())
	text := band.Foreground(colorText())
	glyph := ""
	lead := 0
	if symbol != "" {
		lead = 1
		glyph = symbolStyle.Background(colorAccentDim()).Render(symbol)
	}
	if focused {
		band = lipgloss.NewStyle().Background(colorAccent())
		text = band.Foreground(colorPortalInk())
		if symbol != "" {
			glyph = band.Foreground(colorPortalInk()).Bold(true).Render(symbol)
		}
	}
	return segmentRow(glyph, lead, label, meta, band, text, width)
}

// rosterSegment is the silvery half of the strip, shared by Workspaces
// and Agents. It holds one surface for the whole side — the lists below
// tell which of the two panes has the cursor — and recedes together when
// the keyboard crosses to the portal: lit silver means "you are on this
// side of the seam".
// Three levels: the selected pane's segment wears the full silver, its
// neighbor a half-step below, and both recede when the keyboard is in
// the portal.
func rosterSegment(label, meta string, selected, lit bool, width int) string {
	ground := colorBandMuted()
	switch {
	case !lit:
		ground = colorBandDim()
	case selected:
		ground = colorBand()
	}
	band := lipgloss.NewStyle().Background(ground)
	text := band.Foreground(colorPortalInk())
	return segmentRow("", 0, label, meta, band, text, width)
}

// rosterLit reports whether the keyboard is on the roster's side of the
// seam.
func (m Model) rosterLit() bool {
	return m.activePane == paneWorkspaces || m.activePane == paneAgents
}

// renderWorkspacesBar is the band's first segment: the pane's name and
// how many workspaces the catalog holds.
func (m Model) renderWorkspacesBar(meta string, width int) string {
	if meta == "" {
		meta = fmt.Sprintf("%d", len(m.catalogWorkspaces))
	}
	return rosterSegment("Workspaces", meta,
		m.activePane == paneWorkspaces, m.rosterLit(), width)
}

// renderAgentsBar is the middle segment: the pane's name and the size of
// the selected workspace's roster.
func (m Model) renderAgentsBar(meta string, width int) string {
	if meta == "" {
		meta = fmt.Sprintf("%d", len(m.agentsForSelectedWorkspace()))
	}
	return rosterSegment("Agents", meta,
		m.activePane == paneAgents, m.rosterLit(), width)
}

// renderTitleBand composes the strip as one string at full width — the
// band is continuous because it is one surface, not three headers meeting
// at seams. Each pane's segment is laid at its pane's width; the seam
// columns disappear under the shared ground.
func (m Model) renderTitleBand(workspaceWidth, agentWidth, interactionWidth int) string {
	// The body is a column narrower than the terminal; the strip is the
	// one row that runs to the true edge, like the header above it.
	interactionWidth += max(0, m.width-1-(workspaceWidth+agentWidth+interactionWidth)+1)
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
	// The seam column is the agents segment's last cell: the rule below
	// hangs flush from the roster tab's edge, where the light ends. While
	// the terminal is lit, that cell carries the rule's cap, so the
	// accent line runs unbroken from the floor to the header's edge.
	if m.terminalFocused() {
		cap := lipgloss.NewStyle().
			Background(colorBandDim()).
			Foreground(colorAccent()).
			Render("▕")
		return m.renderWorkspacesBar("", workspaceWidth) +
			m.renderAgentsBar("", agentWidth-1) + cap +
			right
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
