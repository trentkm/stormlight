package ui

// Cards: what a roster row becomes when the rows are expanded.
//
// A compact row is one line, and the panes are lists of them. Expanded,
// each row is a card — its title line and one line of detail, inside a
// thin rounded border — stacked with the borders as their separation.
// The border carries the selection, and nothing else does: the two
// cards on the path, the chosen workspace and the chosen agent in it,
// are framed in the strip's bright silver with their titles in full
// ink, every other card sits in the quiet border color with its title
// muted, and a row awaiting its delete confirmation is framed in red.
// No card is filled. The compact list paints its cursor row's
// background, and that fill inside a frame read as a smear rather than
// a cursor; the band at the top of the panes already says which side
// of the seam the keyboard is on.
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

// cardBorderFor is the border's color for a row's selection state: on
// the path — the cursor row, or the selection remembered in the pane
// the cursor has left — it is lit.
func cardBorderFor(selected, focused, danger bool) color.Color {
	switch {
	case danger:
		return colorFailed()
	case selected || focused:
		return colorBand()
	}
	return colorBorder()
}

// renderCard frames two lines, already rendered at the card's inner
// width, in a thin rounded border.
func renderCard(top, bottom string, width int, border color.Color) string {
	inner := cardInnerWidth(width)
	edge := lipgloss.NewStyle().Foreground(border)
	rule := strings.Repeat("─", inner+2)
	line := func(content string) string {
		content = ansi.Truncate(content, inner, "")
		if short := inner - lipgloss.Width(content); short > 0 {
			content += strings.Repeat(" ", short)
		}
		return edge.Render("│ ") + content + edge.Render(" │")
	}
	return strings.Join([]string{
		edge.Render("╭" + rule + "╮"),
		line(top),
		line(bottom),
		edge.Render("╰" + rule + "╯"),
	}, "\n")
}

// shortenPath fits a path into a width by giving up what matters least
// first: the parents' spelling (fish style, each to its first rune:
// /Volumes/repos/stormlight → /V/r/stormlight), then the parents
// themselves from the left, whole (…/stormlight) — never a segment cut
// in half, which reads as a different name — and only then the name
// itself, cut from the left.
func shortenPath(path string, width int) string {
	if width <= 0 || path == "" {
		return ""
	}
	if lipgloss.Width(path) <= width {
		return path
	}
	segments := strings.Split(path, "/")
	abbreviated := make([]string, len(segments))
	for index, segment := range segments {
		abbreviated[index] = segment
		if index < len(segments)-1 && segment != "~" {
			if runes := []rune(segment); len(runes) > 1 {
				abbreviated[index] = string(runes[:1])
			}
		}
	}
	if joined := strings.Join(abbreviated, "/"); lipgloss.Width(joined) <= width {
		return joined
	}
	for from := 1; from < len(segments); from++ {
		if tail := "…/" + strings.Join(segments[from:], "/"); lipgloss.Width(tail) <= width {
			return tail
		}
	}
	return truncatePathTail(segments[len(segments)-1], width)
}

// cardDetail joins a card's detail tokens with the path last. The path
// is the point of the line, so it comes first in every sense: tokens
// are given least important last and are dropped from the end until
// the whole path fits, and a path that cannot fit even alone is
// shortened — see shortenPath.
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
	return lead() + shortenPath(path, max(1, width-lipgloss.Width(lead())))
}
