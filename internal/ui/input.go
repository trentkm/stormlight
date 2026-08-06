package ui

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

func newTaskInput(placeholder string) textarea.Model {
	input := textarea.New()
	input.Placeholder = placeholder
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.MaxHeight = 1000
	input.SetWidth(40)
	input.SetHeight(4)

	// v2 gathers the focused and blurred variants into one Styles value and
	// gives the cursor its own, so the pair is set together rather than
	// through separate fields.
	state := textarea.StyleState{
		Base:        lipgloss.NewStyle().Foreground(colorText()),
		CursorLine:  lipgloss.NewStyle().Foreground(colorText()),
		EndOfBuffer: lipgloss.NewStyle().Foreground(colorText()),
		Placeholder: lipgloss.NewStyle().Foreground(colorMuted()),
		Prompt:      lipgloss.NewStyle().Foreground(colorAccent()),
		Text:        lipgloss.NewStyle().Foreground(colorText()),
	}
	input.SetStyles(textarea.Styles{
		Focused: state,
		Blurred: state,
		// The cursor is now described rather than styled: v2 drives the
		// terminal's real cursor, so it takes a color and a shape instead
		// of a reversed lipgloss style.
		Cursor: textarea.CursorStyle{Color: colorText()},
	})
	return input
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

func (i lineInput) Update(msg tea.KeyPressMsg) lineInput {
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
		// Text is non-empty exactly when the key produced printable
		// characters, which folds v1's separate rune and space cases into
		// one: space arrives as Text " " like any other character.
		if msg.Text != "" {
			runes := []rune(msg.Text)
			i.value = append(i.value[:i.cursor], append(runes, i.value[i.cursor:]...)...)
			i.cursor += len(runes)
		}
	}
	return i
}

// Insert drops pasted text in at the cursor, flattened to one line.
func (i *lineInput) Insert(text string) {
	if !i.focused {
		return
	}
	runes := flattenPaste(text)
	if len(runes) == 0 {
		return
	}
	i.value = append(i.value[:i.cursor], append(runes, i.value[i.cursor:]...)...)
	i.cursor += len(runes)
}

func (i lineInput) View() string {
	if len(i.value) == 0 {
		placeholder := mutedStyle().Render(ansi.Truncate(i.placeholder, i.width, "…"))
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
