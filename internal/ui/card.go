package ui

// Cards: what a roster row becomes when the rows are expanded.
//
// A compact row is one line, and the panes are lists of them. Expanded,
// each row is a card — its title line and one line of detail, inside a
// thin rounded border — stacked with the borders as their separation.
// The border carries the selection, and nothing else does. The cursor's
// card is framed heavy — ┏━━┓, the one bold frame on the screen, so
// which column the keyboard is in is legible from across the room, the
// way the compact rows' ▌ is heavy where ▏ is light. The path's other
// card, the selection remembered in the pane the cursor has left, keeps
// the thin rounded frame but lit a shade behind, border and title both,
// so the eye finds the cursor without that card falling out of the
// path. Every other card sits thin in the quiet border color with its
// title muted, and a row awaiting its delete confirmation is framed in
// red. No card is filled: the compact list paints its cursor row's
// background, and that fill inside a frame read as a smear rather than
// a cursor.
//
// The detail line says the one thing the title does not. For a
// workspace that is where it is: its path, with its machine when it is
// not this one — resolver kinds, checkout labels and component names
// used to share the line and said nothing the path does not. For an
// agent it is what it is doing: its latest summary, else its task;
// where it is belongs to the workspace card above it.

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

// cardFrame is the frame a card is drawn in: its color and weight.
type cardFrame struct {
	ink   color.Color
	heavy bool
}

// cardGrade is a card's frame and title style for its selection state,
// decided together so the two never disagree about where the cursor
// is: heavy and red for a delete confirmation; heavy in the band's full
// silver with full ink on the cursor's row; thin, a shade behind each,
// on the selection remembered in the pane the cursor has left; thin in
// the quiet border color with muted ink off the path.
func cardGrade(selected, focused, danger bool) (cardFrame, lipgloss.Style) {
	switch {
	case danger:
		return cardFrame{ink: colorFailed(), heavy: true},
			lipgloss.NewStyle().Foreground(colorFailed()).Bold(true)
	case focused:
		return cardFrame{ink: colorBand(), heavy: true}, titleStyle()
	case selected:
		return cardFrame{ink: colorBandSoft()},
			lipgloss.NewStyle().Foreground(colorTextSoft()).Bold(true)
	}
	return cardFrame{ink: colorBorder()}, mutedStyle()
}

// The two weights of frame. The heavy one has square corners: box
// drawing has no heavy rounded ones.
var (
	thinFrame  = [6]string{"╭", "─", "╮", "│", "╰", "╯"}
	heavyFrame = [6]string{"┏", "━", "┓", "┃", "┗", "┛"}
)

// renderCard frames two lines, already rendered at the card's inner
// width, in a thin rounded border. A pane narrower than the frame's
// own five columns gets the frame cut to the pane rather than one that
// spills past it.
func renderCard(top, bottom string, width int, frame cardFrame) string {
	glyphs := thinFrame
	if frame.heavy {
		glyphs = heavyFrame
	}
	inner := cardInnerWidth(width)
	edge := lipgloss.NewStyle().Foreground(frame.ink)
	rule := strings.Repeat(glyphs[1], inner+2)
	line := func(content string) string {
		content = ansi.Truncate(content, inner, "")
		if short := inner - lipgloss.Width(content); short > 0 {
			content += strings.Repeat(" ", short)
		}
		return edge.Render(glyphs[3]+" ") + content + edge.Render(" "+glyphs[3])
	}
	rows := []string{
		edge.Render(glyphs[0] + rule + glyphs[2]),
		line(top),
		line(bottom),
		edge.Render(glyphs[4] + rule + glyphs[5]),
	}
	if width < inner+cardInset {
		for index, row := range rows {
			rows[index] = ansi.Truncate(row, max(1, width), "")
		}
	}
	return strings.Join(rows, "\n")
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
