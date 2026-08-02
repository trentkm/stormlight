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

// renderFooterRule tints the divider with the wordmark's sapphire→ice
// gradient, dimmed toward the border grey — the footer's quiet echo of the
// header identity.
func renderFooterRule(width int) string {
	var out strings.Builder
	for index := 0; index < width; index++ {
		t := 0.0
		if width > 1 {
			t = float64(index) / float64(width-1)
		}
		out.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{
				Light: lerpHex(gradientStop(wordmarkStopsLight, t), "#AAB3B9", 0.55),
				Dark:  lerpHex(gradientStop(wordmarkStopsDark, t), "#59636B", 0.55),
			}).
			Render("─"))
	}
	return out.String()
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
			hints := "j/k choose  Enter task"
			if m.chooseDispatchDirectory {
				hints = "j/k choose  Enter location"
			}
			hints += "  m mode"
			if m.nvimPath != "" {
				hints += "  e Neovim"
			}
			return hints + "  Esc cancel"
		case dispatchDirectory:
			return "j/k location  Enter choose  m mode  e edit path  Esc cancel"
		case dispatchCustomPath:
			return "type to filter  Enter choose  ↑/↓ pick  Backspace up  Esc cancel"
		default:
			hints := "Enter launch"
			if m.nvimPath != "" {
				hints += "  Ctrl-o Neovim"
			}
			return hints + "  Esc cancel"
		}
	case modeAddWorkspace:
		return "j/k select  Enter add  e edit path  Esc cancel"
	case modeRename:
		return "Enter apply  Esc cancel"
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
			"h/l panes  j/k select  n new  M seen  , sort  " + rowMode + "  Enter open",
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
