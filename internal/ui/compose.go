package ui

// The Spanreed reply composer.
// Split from model.go; see #34.

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m Model) updateCompose(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		return m.closeComposer(), nil
	case "backspace", "ctrl+h":
		// Backspace deletes backwards until there is nothing left to
		// delete, and then it deletes the composer: an empty reply box is
		// the last thing standing between the cursor and the transcript.
		// Held-down backspace therefore walks out of a reply the same way
		// it walked into it, without the hand leaving for Esc.
		if m.sendInput.Value() == "" {
			return m.closeComposer(), nil
		}
	case "ctrl+j":
		return m.insertComposerNewline()
	case "ctrl+v":
		// Terminal paste cannot carry image bytes into a TUI, so Ctrl-v
		// bridges: read the clipboard image, park it in a temp file, and
		// insert the path — coding agents read images referenced by path.
		// (Text paste still arrives through the terminal's own paste.)
		path, err := pasteClipboardImage()
		if err != nil {
			m.complain(err)
			return m, nil
		}
		m.clearComplaint(modeCompose)
		m.insertComposerToken(path)
		m.syncComposerSize()
		return m, nil
	case "enter":
		selected, ok := m.selectedAgent()
		if !ok {
			m.mode = modeNormal
			return m, nil
		}
		if selected.ProcessLive && selected.Attention.TerminalOwned() {
			// The agent is showing a prompt or picker; a pasted message
			// would type into it and Enter would pick whatever is
			// highlighted. Answer where the prompt lives.
			m.complain(fmt.Errorf(
				"agent is showing a prompt — Esc, then Enter opens its terminal"))
			return m, nil
		}
		text := strings.TrimSpace(m.sendInput.Value())
		if text == "" {
			m.complain(fmt.Errorf("message cannot be empty"))
			return m, nil
		}
		// The composer stays open for the next message; one i enters the
		// conversation, Esc leaves it. Which is why the send has to
		// retract the objection it disproves — the box outlives it.
		m.clearComplaint(modeCompose)
		m.sendInput.SetValue("")
		m.syncComposerSize()
		return m, actionCmd("Message sent", func(ctx context.Context) error {
			return m.backend.Send(ctx, selected.ID, text)
		})
	}
	var cmd tea.Cmd
	m.sendInput, cmd = m.sendInput.Update(msg)
	m.syncComposerSize()
	return m, cmd
}

// closeComposer leaves the reply box and hands the keyboard back to normal
// mode. The value is left as it is: nothing that closes the composer discards
// a draft, and re-entering with i finds the message where it was.
func (m Model) closeComposer() Model {
	m.mode = modeNormal
	m.sendInput.Blur()
	return m
}

// insertComposerNewline breaks the line under the cursor (ctrl+j).
// Shift+Enter was scrapped: it needs modifyOtherKeys to hold across the
// whole relay to the agent, and it silently submits instead of inserting
// a newline anywhere it doesn't. Ctrl+j works everywhere.
func (m Model) insertComposerNewline() (tea.Model, tea.Cmd) {
	insertTextareaNewline(&m.sendInput)
	m.syncComposerSize()
	return m, nil
}

// insertTextareaNewline breaks the line under the cursor. The newline is
// handed to the textarea as the Enter it binds internally rather than
// written in behind its back: only its own Update repositions the scroll
// viewport, so an inserted rune leaves the view a row behind the cursor
// until the next keystroke drags it along.
func insertTextareaNewline(input *textarea.Model) {
	*input, _ = input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// syncComposerSize keeps the persisted reply textarea sized to the Spanreed
// pane. Sizing only a render-time copy is not enough: the textarea's
// internal wrap and scroll state evolve while typing, so it must live at
// the real dimensions.
func (m *Model) syncComposerSize() {
	// interactionDimensions already returns the pane's content width (the
	// same value renderInteraction receives) — no further adjustment.
	inner, _ := m.interactionDimensions()
	inner = max(1, inner)
	previous := m.sendInput.Height()
	m.sendInput.SetWidth(inner)
	height := composerHeight(m.sendInput.Value(), inner)
	m.sendInput.SetHeight(height)
	if height > previous {
		resetTextareaScroll(&m.sendInput)
	}
}

// resetTextareaScroll clears a scroll offset a grown box no longer needs.
// The keystroke that grew the content was processed while the box was still
// a row short, which scrolled the textarea's viewport — and nothing scrolls
// it back once the box catches up, since its repositioning only ever chases
// the cursor. Rebuilding the value resets the scroll; the cursor is put back
// where it was.
func resetTextareaScroll(input *textarea.Model) {
	row := input.Line()
	info := input.LineInfo()
	column := info.StartColumn + info.ColumnOffset
	input.SetValue(input.Value())
	for input.Line() > row {
		input.CursorUp()
	}
	input.SetCursorColumn(column)
}

// insertComposerToken inserts text into the reply at the cursor, padded
// with spaces so it stays its own token instead of fusing with what the
// user already typed — pasted file paths must survive shell-style parsing.
func (m *Model) insertComposerToken(token string) {
	lines := strings.Split(m.sendInput.Value(), "\n")
	row := m.sendInput.Line()
	info := m.sendInput.LineInfo()
	column := info.StartColumn + info.ColumnOffset
	var before, after rune
	if row >= 0 && row < len(lines) {
		line := []rune(lines[row])
		if column > 0 && column <= len(line) {
			before = line[column-1]
		}
		if column >= 0 && column < len(line) {
			after = line[column]
		}
	}
	m.sendInput.InsertString(padToken(token, before, after))
}

// padToken surrounds an inserted token with spaces as needed: before and
// after are the runes adjacent to the cursor, zero at a line boundary. A
// trailing space is always ensured so the user can keep typing.
func padToken(token string, before, after rune) string {
	if before != 0 && !unicode.IsSpace(before) {
		token = " " + token
	}
	if after == 0 || !unicode.IsSpace(after) {
		token += " "
	}
	return token
}

// composerHeight sizes the reply box to its wrapped content: the textarea
// wraps at its width minus the two-column prompt. One row minimum, six
// before the box scrolls internally. The height MUST match the textarea's
// own layout exactly — its scroll viewport is shared between model copies,
// and an undersized box wedges it scrolled down permanently.
func composerHeight(value string, width int) int {
	wrapWidth := max(1, width-2)
	total := 0
	for _, logical := range strings.Split(value, "\n") {
		total += textareaWrapCount([]rune(logical), wrapWidth)
	}
	return clamp(total, 1, 6)
}

// textareaWrapCount is a faithful port of bubbles/textarea's internal
// wrap(), reduced to counting soft lines. Note the closing `>=`: a line
// exactly filling the width spills its final word onto a new row.
func textareaWrapCount(runes []rune, width int) int {
	lines := [][]rune{{}}
	word := []rune{}
	row := 0
	spaces := 0

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if lipgloss.Width(string(lines[row]))+lipgloss.Width(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			}
			spaces = 0
			word = nil
		} else {
			lastCharLen := lipgloss.Width(string(word[len(word)-1]))
			if lipgloss.Width(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if lipgloss.Width(string(lines[row]))+lipgloss.Width(string(word))+spaces >= width {
		lines = append(lines, []rune{})
	}
	return len(lines)
}
