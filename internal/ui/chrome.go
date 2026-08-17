package ui

// The footer: gradient rules and key hints.
// Split from model.go; see #34.

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/theme"
)

func (m Model) renderFooter() string {
	width := max(1, m.width-1)
	inner := max(1, width-3)
	var content string
	if chord := m.chordHints(); chord != "" {
		content = chord
	} else {
		hintStyle := mutedStyle()
		switch {
		case m.terminalFocused():
			// Inside the portal the footer carries the terminal's
			// controls; it wears the portal's light rather than fading
			// to furniture gray.
			hintStyle = lipgloss.NewStyle().Foreground(colorWorking())
		case m.mode == modeNormal && m.rosterLit():
			// On the roster's side the hints wear the strip's silver —
			// the footer matches whichever side of the seam is lit.
			hintStyle = lipgloss.NewStyle().Foreground(colorBand())
		}
		hints := renderHints(m.commandHints(), hintStyle)
		content = ansi.Truncate(hints, inner, "…")
		if m.err != nil {
			content = renderFooterError(inner, m.err.Error(), hints)
		}
	}
	glint := lipgloss.NewStyle().
		Foreground(theme.Color(theme.Pair{
			Light: wordmarkStopsLight[1],
			Dark:  wordmarkStopsDark[1],
		})).
		Render("✦ ")
	return lipgloss.NewStyle().Width(width).MaxHeight(2).Render(
		renderFooterRule(width) + "\n " + glint + content,
	)
}

// renderComposerRule draws a symmetric rule: sapphire at both edges rising
// through the wordmark gradient to the crest at the center.
func renderComposerRule(width int) string {
	var out strings.Builder
	for index := 0; index < width; index++ {
		t := 0.0
		if width > 1 {
			t = float64(index) / float64(width-1)
		}
		s := 1 - math.Abs(2*t-1)
		dark := gradientStop(wordmarkStopsDark, s)
		light := gradientStop(wordmarkStopsLight, s)
		out.WriteString(lipgloss.NewStyle().
			Foreground(theme.Color(theme.Pair{
				Light: lerpHex(light, wordmarkCrest.Light, s*0.5),
				Dark:  lerpHex(dark, wordmarkCrest.Dark, s*0.5),
			})).
			Render("─"))
	}
	return out.String()
}

// The dashboard is framed by one band of light: the wordmark's sapphire→ice
// gradient, run across the full terminal width and held back toward the
// border grey. The pane headers ride the top of it and the footer rule the
// bottom, so the frame reads as a single drawn material rather than three
// decorations that happen to share a screen. The segment the cursor is in
// rises to full strength — focus is where the band brightens, not a color
// swapped out for another.
//
// bandColor is that gradient sampled at t, the fraction of the way across
// the dashboard. It returns an unresolved pair rather than a painted color:
// the band is palette data, blended toward the border grey, and only the
// thing that draws with it knows which background it is drawing on.
func bandColor(t float64, lit bool) theme.Pair {
	fade := 0.55
	if lit {
		fade = 0
	}
	return theme.Pair{
		Light: lerpHex(gradientStop(wordmarkStopsLight, t), theme.Border.Light, fade),
		Dark:  lerpHex(gradientStop(wordmarkStopsDark, t), theme.Border.Dark, fade),
	}
}

// renderBandRun draws count cells of the band. The run knows where it sits in
// the dashboard (start of total columns) rather than restarting the gradient
// per pane, which is what keeps the header rules and the footer rule reading
// as one continuous sweep. The header draws its band as underlined blanks so
// it can share a row with the labels; the footer, having a row to itself,
// draws glyphs.
func renderBandRun(glyph string, start, count, total int, lit, underlined bool) string {
	var out strings.Builder
	for index := 0; index < count; index++ {
		t := 0.0
		if total > 1 {
			t = float64(clamp(start+index, 0, total-1)) / float64(total-1)
		}
		style := lipgloss.NewStyle().Foreground(theme.Color(bandColor(t, lit)))
		if underlined {
			style = style.Underline(true)
		}
		out.WriteString(style.Render(glyph))
	}
	return out.String()
}

func renderFooterRule(width int) string {
	return renderBandRun("─", 0, width, width, false, false)
}

func (m Model) chordHints() string {
	var label string
	var options [][2]string
	switch m.normalPrefix {
	case ",":
		label = "Sort:"
		options = [][2]string{{"a", "attention"}, {"n", "name"}, {"c", "newest"}}
	case "g":
		label = "Go:"
		options = [][2]string{{"g", "top"}}
	default:
		return ""
	}
	parts := make([]string, 0, len(options)+2)
	parts = append(parts, titleStyle().Render(label))
	for _, option := range options {
		parts = append(parts,
			accentStyle().Render(option[0])+" "+mutedStyle().Render(option[1]))
	}
	parts = append(parts, mutedStyle().Render("Esc cancel"))
	return strings.Join(parts, "  ")
}

// renderHints paints each hint as one unit and sets a muted dot between
// neighbors, so the row reads as separate key–action pairs instead of one
// run-on line. The items arrive unstyled; the caller picks the ink.
func renderHints(items []string, style lipgloss.Style) string {
	separator := mutedStyle().Render(" · ")
	rendered := make([]string, len(items))
	for index, item := range items {
		rendered[index] = style.Render(item)
	}
	return strings.Join(rendered, separator)
}

// renderFooterError lets an error talk over the left end of the hint row.
// Errors are the one message that still shares the row — everything else
// the footer used to announce either shows where it happens or rides in
// the hints — and the hints keep their own ink beside it rather than
// fading to gray.
func renderFooterError(width int, message, hints string) string {
	available := max(1, width)
	messageWidth := clamp(available/3, 8, 28)
	hintWidth := available - messageWidth - 2
	if hintWidth < 12 {
		return errorStyle().Render(truncate(message, available))
	}
	return errorStyle().Render(truncate(message, messageWidth)) +
		"  " + ansi.Truncate(hints, hintWidth, "…")
}

// searchMatchLabel is the live position among search matches; it rides in
// the hint row because the search has no other place to answer "which of
// how many".
func (m Model) searchMatchLabel() string {
	if len(m.search.matches) == 0 {
		return "no match"
	}
	return fmt.Sprintf("match %d/%d", m.search.index+1, len(m.search.matches))
}

func (m Model) commandHints() []string {
	switch m.mode {
	case modeCompose:
		return []string{"Enter send", "Ctrl-j newline", "Ctrl-v image", "Esc cancel"}
	case modeSearch:
		hints := []string{"type to search", "Enter keep", "n/N move", "Esc cancel"}
		if m.search.query != "" {
			hints = append([]string{m.searchMatchLabel()}, hints[1:]...)
		}
		return hints
	case modeDelete:
		// The confirmation names its victim here: the hint row is the
		// prompt, so what x is about to delete has to be spelled out.
		if m.activePane == paneWorkspaces &&
			m.workspaceCursor >= 0 && m.workspaceCursor < len(m.groups) {
			if count := len(m.groups[m.workspaceCursor].agents); count > 0 {
				return []string{
					fmt.Sprintf("X delete %s and %d agent(s)",
						m.selectedWorkspaceLabel(), count),
					"Esc cancel",
				}
			}
			return []string{
				"x remove " + m.selectedWorkspaceLabel(), "Esc cancel",
			}
		}
		if selected, ok := m.selectedAgent(); ok {
			return []string{
				"x delete " + agentDisplayTitle(selected), "Esc cancel",
			}
		}
		return []string{"x confirm", "Esc cancel"}
	case modeDispatch:
		switch m.formFocus {
		case dispatchProvider:
			hints := []string{"j/k choose", "Enter " + m.nextDispatchField(), "m mode"}
			if m.nvimPath != "" {
				hints = append(hints, "e Neovim")
			}
			return append(hints, "Esc cancel")
		case dispatchDirectory:
			return []string{
				"j/k location", "Enter " + m.nextDispatchField(),
				"m mode", "e edit path", "Esc cancel",
			}
		case dispatchCustomPath:
			return []string{
				"Enter choose", "↑/↓ pick", "Backspace up",
				"Tab " + m.nextDispatchField(), "Esc cancel",
			}
		case dispatchName:
			return []string{
				"name this agent", "Enter " + m.nextDispatchField(),
				"Tab fields", "Esc cancel",
			}
		default:
			// Enter launches, so the newline key has to be spelled out
			// here — it is the one affordance in this field with nothing
			// on screen to suggest it. The name row keeps its own label
			// and cursor, and the line is only so wide.
			hints := []string{"Enter launch", "Ctrl-j newline"}
			if m.nvimPath != "" {
				hints = append(hints, "Ctrl-o Neovim")
			}
			return append(hints, "Esc cancel")
		}
	case modeAddWorkspace:
		return []string{"j/k select", "Enter add", "e edit path", "Esc cancel"}
	case modeRename:
		return []string{"Enter apply", "Esc cancel"}
	case modeMark:
		return []string{"w in progress", "a needs attention", "c clear", "Esc cancel"}
	}
	rowMode := "z expand rows"
	if m.rowsExpanded {
		rowMode = "z compact rows"
	}
	if m.width < 72 {
		rowMode = ""
	}
	if m.terminalFocused() {
		return m.terminalHints()
	}
	if m.activePane == paneInteraction {
		if selected, ok := m.selectedAgent(); ok &&
			selected.ProcessLive && selected.Attention.TerminalOwned() {
			return []string{"Enter answer in terminal", "h agents", "j/k scroll", "M seen"}
		}
	}
	appendCompact := func(hints []string, tail ...string) []string {
		if rowMode != "" {
			hints = append(hints, rowMode)
		}
		return append(hints, tail...)
	}
	switch m.activePane {
	case paneAgents:
		return appendCompact(
			[]string{"h/l panes", "j/k select", "n new", "m mark", "M seen", ", sort"},
			"Enter open",
		)
	case paneInteraction:
		if m.search.query != "" {
			return []string{
				"h agents", "j/k scroll", "n/N " + m.searchMatchLabel(),
				"Esc clear", "Enter open",
			}
		}
		return appendCompact(
			[]string{"h agents", "j/k scroll", "i reply", "/ search", "n new"},
			"Enter open",
		)
	default:
		return appendCompact(
			[]string{"j/k select", "l agents", "n add", "K info", ", sort"},
			"? help", "q quit",
		)
	}
}
