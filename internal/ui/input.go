package ui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type lineInput struct {
	value       []rune
	cursor      int
	width       int
	placeholder string
	focused     bool
}

func newLineInput(placeholder string) lineInput {
	return lineInput{
		width:       40,
		placeholder: placeholder,
	}
}

func (i *lineInput) SetValue(value string) {
	i.value = []rune(value)
	i.cursor = len(i.value)
}

func (i lineInput) Value() string {
	return string(i.value)
}

func (i *lineInput) Focus() {
	i.focused = true
}

func (i *lineInput) Blur() {
	i.focused = false
}

func (i *lineInput) SetWidth(width int) {
	i.width = max(1, width)
}

func (i lineInput) Update(msg tea.KeyMsg) lineInput {
	if !i.focused {
		return i
	}

	switch msg.String() {
	case "left":
		if i.cursor > 0 {
			i.cursor--
		}
	case "right":
		if i.cursor < len(i.value) {
			i.cursor++
		}
	case "home", "ctrl+a":
		i.cursor = 0
	case "end", "ctrl+e":
		i.cursor = len(i.value)
	case "backspace", "ctrl+h":
		if i.cursor > 0 {
			i.value = append(i.value[:i.cursor-1], i.value[i.cursor:]...)
			i.cursor--
		}
	case "delete":
		if i.cursor < len(i.value) {
			i.value = append(i.value[:i.cursor], i.value[i.cursor+1:]...)
		}
	case "ctrl+u":
		i.value = append([]rune(nil), i.value[i.cursor:]...)
		i.cursor = 0
	case "ctrl+k":
		i.value = append([]rune(nil), i.value[:i.cursor]...)
	case "ctrl+w":
		start := i.cursor
		for start > 0 && unicode.IsSpace(i.value[start-1]) {
			start--
		}
		for start > 0 && !unicode.IsSpace(i.value[start-1]) {
			start--
		}
		i.value = append(i.value[:start], i.value[i.cursor:]...)
		i.cursor = start
	default:
		runes := msg.Runes
		if msg.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			i.value = append(i.value[:i.cursor], append(runes, i.value[i.cursor:]...)...)
			i.cursor += len(runes)
		}
	}
	return i
}

func (i lineInput) View() string {
	if len(i.value) == 0 {
		placeholder := mutedStyle.Render(ansi.Truncate(i.placeholder, i.width, "…"))
		if !i.focused {
			return placeholder
		}
		cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
		content := cursor + " " + ansi.Truncate(
			placeholder,
			max(1, i.width-lipgloss.Width(cursor)-1),
			"…",
		)
		return content + strings.Repeat(" ", max(0, i.width-lipgloss.Width(content)))
	}

	before := string(i.value[:i.cursor])
	after := string(i.value[i.cursor:])
	cursor := ""
	if i.focused {
		cursor = lipgloss.NewStyle().Reverse(true).Render(" ")
	}
	rendered := before + cursor + after
	if lipgloss.Width(rendered) <= i.width {
		return rendered + strings.Repeat(" ", max(0, i.width-lipgloss.Width(rendered)))
	}

	available := max(1, i.width-lipgloss.Width(cursor))
	leftWidth := available * 2 / 3
	rightWidth := available - leftWidth
	left := ansi.TruncateLeft(before, leftWidth, "…")
	right := ansi.Truncate(after, rightWidth, "…")
	return left + cursor + right
}
