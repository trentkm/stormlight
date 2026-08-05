package ui

// The footer: gradient rules, status row, and key hints.
// Split from model.go; see #34.

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) renderFooter() string {
	width := max(1, m.width-1)
	inner := max(1, width-3)
	var content string
	if chord := m.chordHints(); chord != "" {
		content = chord
	} else {
		hints := m.commandHints()
		content = mutedStyle.Render(truncate(hints, inner))
		if m.err != nil {
			content = renderFooterStatus(inner, m.err.Error(), hints, errorStyle)
		} else if m.status != "Ready" {
			content = renderFooterStatus(inner, m.status, hints, successStyle)
		}
	}
	glint := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{
			Light: wordmarkStopsLight[1],
			Dark:  wordmarkStopsDark[1],
		}).
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
			Foreground(lipgloss.AdaptiveColor{
				Light: lerpHex(light, wordmarkCrest.Light, s*0.5),
				Dark:  lerpHex(dark, wordmarkCrest.Dark, s*0.5),
			}).
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
// the dashboard.
func bandColor(t float64, lit bool) lipgloss.AdaptiveColor {
	fade := 0.55
	if lit {
		fade = 0
	}
	return lipgloss.AdaptiveColor{
		Light: lerpHex(gradientStop(wordmarkStopsLight, t), colorBorder.Light, fade),
		Dark:  lerpHex(gradientStop(wordmarkStopsDark, t), colorBorder.Dark, fade),
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
		style := lipgloss.NewStyle().Foreground(bandColor(t, lit))
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
	parts = append(parts, titleStyle.Render(label))
	for _, option := range options {
		parts = append(parts,
			accentStyle.Render(option[0])+" "+mutedStyle.Render(option[1]))
	}
	parts = append(parts, mutedStyle.Render("Esc cancel"))
	return strings.Join(parts, "  ")
}

func renderFooterStatus(
	width int,
	status string,
	hints string,
	statusStyle lipgloss.Style,
) string {
	available := max(1, width)
	statusWidth := clamp(available/3, 8, 28)
	hintWidth := available - statusWidth - 2
	if hintWidth < 12 {
		return mutedStyle.Render(truncate(hints, available))
	}
	renderedStatus := statusStyle.Render(truncate(status, statusWidth))
	renderedHints := mutedStyle.Render(truncate(hints, hintWidth))
	return renderedStatus + "  " + renderedHints
}

func (m Model) commandHints() string {
	switch m.mode {
	case modeCompose:
		return "Enter send  Ctrl-j newline  Ctrl-v image  Esc cancel"
	case modeSearch:
		return "type to search  Enter keep  n/N move  Esc cancel"
	case modeDelete:
		if m.activePane == paneWorkspaces &&
			m.workspaceCursor >= 0 && m.workspaceCursor < len(m.groups) &&
			len(m.groups[m.workspaceCursor].agents) > 0 {
			return "X delete workspace and agents  Esc cancel"
		}
		return "x confirm  Esc cancel"
	case modeDispatch:
		switch m.formFocus {
		case dispatchProvider:
			hints := "j/k choose  Enter " + m.nextDispatchField() + "  m mode"
			if m.nvimPath != "" {
				hints += "  e Neovim"
			}
			return hints + "  Esc cancel"
		case dispatchDirectory:
			return "j/k location  Enter " + m.nextDispatchField() +
				"  m mode  e edit path  Esc cancel"
		case dispatchCustomPath:
			return "Enter choose  ↑/↓ pick  Backspace up  Tab " +
				m.nextDispatchField() + "  Esc cancel"
		case dispatchName:
			return "name this agent  Enter " + m.nextDispatchField() +
				"  Tab fields  Esc cancel"
		default:
			// Enter launches, so the newline key has to be spelled out
			// here — it is the one affordance in this field with nothing
			// on screen to suggest it. The name row keeps its own label
			// and cursor, and the line is only so wide.
			hints := "Enter launch  Ctrl-j newline"
			if m.nvimPath != "" {
				hints += "  Ctrl-o Neovim"
			}
			return hints + "  Esc cancel"
		}
	case modeAddWorkspace:
		return "j/k select  Enter add  e edit path  Esc cancel"
	case modeRename:
		return "Enter apply  Esc cancel"
	case modeMark:
		return "w in progress  a needs attention  c clear  Esc cancel"
	}
	rowMode := "z expand rows"
	if m.rowsExpanded {
		rowMode = "z compact rows"
	}
	if m.width < 72 {
		rowMode = ""
	}
	if m.activePane == paneInteraction {
		if selected, ok := m.selectedAgent(); ok &&
			selected.ProcessLive && selected.Attention.TerminalOwned() {
			return "Enter answer in terminal  h agents  j/k scroll  M seen"
		}
	}
	switch m.activePane {
	case paneAgents:
		return strings.TrimSpace(
			"h/l panes  j/k select  n new  m mark  M seen  , sort  " +
				rowMode + "  Enter open",
		)
	case paneInteraction:
		if m.search.query != "" {
			return "h agents  j/k scroll  n/N match  Esc clear  Enter open"
		}
		return strings.TrimSpace(
			"h agents  j/k scroll  i reply  / search  n new  " + rowMode + "  Enter open",
		)
	default:
		return strings.TrimSpace(
			"j/k select  l agents  n add  K info  , sort  " + rowMode + "  ? help  q quit",
		)
	}
}
