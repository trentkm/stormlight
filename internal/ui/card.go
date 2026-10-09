package ui

// Cards: what a roster row becomes when the rows are expanded.
//
// A compact row is one line, and the panes are lists of them. Expanded,
// each row is a card — its title line and one line of detail, inside a
// thin rounded border — stacked with the borders as their separation.
// The border carries the selection: silver on the row the cursor is on,
// dimmer silver on a selection remembered in a pane the cursor has
// left, the quiet border color otherwise, and the danger color on a
// row awaiting its delete confirmation. The cursor row keeps the filled
// background inside its border, as it has in the compact list.
//
// The detail line is the path. Resolver kinds, checkout labels, and
// component names used to share it, and none of them said anything the
// path does not: where this is, is the one thing worth a second line.

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cardRows is a card's height: the border, two lines, the border.
const cardRows = 4

// cardInset is what the border and its padding take from the width.
const cardInset = 4

// cardInnerWidth is the room a card's lines have at a pane width.
func cardInnerWidth(width int) int {
	return max(1, width-cardInset)
}

// cardBorderFor is the border's color for a row's selection state.
func cardBorderFor(selected, focused, danger bool) color.Color {
	switch {
	case danger:
		return colorFailed()
	case focused:
		return colorBand()
	case selected:
		return colorBandMuted()
	}
	return colorBorder()
}

// renderCard frames two lines, already rendered at the card's inner
// width, in a thin rounded border. background, when set, fills the
// padding beside the lines so a filled row reads as one surface edge to
// edge inside the frame; the lines themselves carry their own.
func renderCard(top, bottom string, width int, border, background color.Color) string {
	inner := cardInnerWidth(width)
	edge := lipgloss.NewStyle().Foreground(border)
	pad := lipgloss.NewStyle()
	if background != nil {
		pad = pad.Background(background)
	}
	rule := strings.Repeat("─", inner+2)
	line := func(content string) string {
		content = ansi.Truncate(content, inner, "")
		if short := inner - lipgloss.Width(content); short > 0 {
			content += pad.Render(strings.Repeat(" ", short))
		}
		return edge.Render("│") + pad.Render(" ") + content + pad.Render(" ") + edge.Render("│")
	}
	return strings.Join([]string{
		edge.Render("╭" + rule + "╮"),
		line(top),
		line(bottom),
		edge.Render("╰" + rule + "╯"),
	}, "\n")
}

// cardDetail joins a card's detail tokens with the path last. The path
// is the point of the line, so it comes first in every sense: tokens
// are given least important last and are dropped from the end until
// the whole path fits, and a path that cannot fit even alone is cut
// from the left so its tail — the part that tells two paths apart —
// survives.
func cardDetail(tokens []string, path string, width int) string {
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != "" {
			kept = append(kept, token)
		}
	}
	if path == "" {
		return truncate(strings.Join(kept, metaSeparator), width)
	}
	lead := func() string {
		if len(kept) == 0 {
			return ""
		}
		return strings.Join(kept, metaSeparator) + metaSeparator
	}
	for len(kept) > 0 && width-lipgloss.Width(lead()) < lipgloss.Width(path) {
		kept = kept[:len(kept)-1]
	}
	return lead() + truncatePathTail(path, max(1, width-lipgloss.Width(lead())))
}
