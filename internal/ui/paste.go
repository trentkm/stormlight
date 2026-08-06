package ui

// Bracketed paste.
//
// Under Bubble Tea v1 a paste arrived as an ordinary key message carrying
// runes, so it rode the same path as typing and every input got it for free.
// v2 gives paste its own message — tea.PasteMsg, delivered between
// PasteStartMsg and PasteEndMsg — and nothing here handled it, so Cmd-V into
// the task box went nowhere. Paste is therefore routed the way keys are: by
// mode, to whatever input that mode has focused. The two textareas take the
// message itself (bubbles knows how to insert a multi-line paste and how to
// keep its own wrap and scroll state honest); the single-line fields take
// flattened text.

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

func (m Model) updatePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeDispatch:
		return m.pasteIntoDispatch(msg)
	case modeCompose:
		var cmd tea.Cmd
		m.sendInput, cmd = m.sendInput.Update(msg)
		m.syncComposerSize()
		return m, cmd
	case modeSearch:
		m.search.input.Insert(msg.Content)
		m.applySearchQuery()
		return m, nil
	case modeAddWorkspace:
		if m.formFocus == dispatchCustomPath {
			m.pathNav.insert(msg.Content)
		}
		return m, nil
	case modeRename:
		m.renameInput.Insert(msg.Content)
		return m, nil
	}
	return m, nil
}

// pasteIntoDispatch drops the paste into the new-agent form's focused field
// and re-sizes the task box after, for the same reason the key path does: a
// paste rewraps the composer, and the textarea has to be told.
func (m Model) pasteIntoDispatch(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.formFocus {
	case dispatchTask:
		m.taskInput, cmd = m.taskInput.Update(msg)
	case dispatchName:
		m.nameInput.Insert(msg.Content)
	case dispatchCustomPath:
		m.pathNav.insert(msg.Content)
	}
	m.syncTaskComposerSize()
	return m, cmd
}

// flattenPaste folds pasted text onto one line for the single-line fields.
// Line breaks and tabs become single spaces and everything else unprintable
// is dropped: a path copied out of a wrapped terminal arrives as a path
// rather than as a newline the field cannot hold or escape debris it would
// happily store and later pass to a shell.
func flattenPaste(text string) []rune {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []rune
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			out = append(out, ' ')
		case unicode.IsPrint(r):
			out = append(out, r)
		}
	}
	return out
}
