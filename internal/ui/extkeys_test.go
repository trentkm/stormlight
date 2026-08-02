package ui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// rawCSI mimics Bubble Tea's unexported unknownCSISequenceMsg: a named
// []byte carrying an unrecognized escape sequence. rawKeySequence works
// on the reflected kind, so this stand-in exercises the same path.
type rawCSI []byte

func TestDecodeModifiedKeysTranslatesShiftEnter(t *testing.T) {
	for _, sequence := range []string{
		"\x1b[27;2;13~", // xterm modifyOtherKeys form
		"\x1b[13;2u",    // CSI-u form (tmux extended-keys-format csi-u)
	} {
		msg := DecodeModifiedKeys(nil, rawCSI(sequence))
		if msg != chordShiftEnter {
			t.Fatalf("decode(%q) = %#v, want shift+enter chord", sequence, msg)
		}
	}
}

func TestDecodeModifiedKeysKeepsKeyMsgStrings(t *testing.T) {
	cases := map[string]string{
		"\x1b[27;5;99~":  "ctrl+c", // xterm re-encoding of a plain control chord
		"\x1b[99;5u":     "ctrl+c",
		"\x1b[27;1;13~":  "enter",
		"\x1b[13;3u":     "alt+enter",
		"\x1b[27;2;9~":   "shift+tab",
		"\x1b[27;2;65~":  "A",
		"\x1b[97;3u":     "alt+a",
		"\x1b[27;5;32~":  "ctrl+@", // ctrl+space
		"\x1b[27;3;127~": "alt+backspace",
	}
	for sequence, want := range cases {
		msg := DecodeModifiedKeys(nil, rawCSI(sequence))
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			t.Fatalf("decode(%q) = %#v, want tea.KeyMsg", sequence, msg)
		}
		if key.String() != want {
			t.Fatalf("decode(%q) = %q, want %q", sequence, key.String(), want)
		}
	}
}

func TestDecodeModifiedKeysDropsKeyReleases(t *testing.T) {
	if msg := DecodeModifiedKeys(nil, rawCSI("\x1b[13;2:3u")); msg != nil {
		t.Fatalf("release event decoded to %#v, want nil", msg)
	}
}

func TestDecodeModifiedKeysPassesUnrelatedMessagesThrough(t *testing.T) {
	unrelated := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyEnter},
		rawCSI("\x1b[?1049h"),   // not a key report
		rawCSI("\x1b[27;5;48~"), // ctrl+0: no v1 representation
		rawCSI("plain bytes"),
		tickMsg{},
		nil,
	}
	for _, msg := range unrelated {
		result := DecodeModifiedKeys(nil, msg)
		if !reflect.DeepEqual(result, msg) {
			t.Fatalf("message %#v was rewritten to %#v", msg, result)
		}
	}
}

func TestShiftEnterChordInsertsComposerNewline(t *testing.T) {
	model := NewModel(stubBackend{})
	model.mode = modeCompose
	model.sendInput.Focus()
	model.sendInput.SetValue("first")

	updated, _ := model.Update(chordShiftEnter)
	model = updated.(Model)
	if got := model.sendInput.Value(); got != "first\n" {
		t.Fatalf("composer value = %q, want %q", got, "first\n")
	}

	// Outside the composer the chord must be inert.
	model.mode = modeNormal
	updated, _ = model.Update(chordShiftEnter)
	model = updated.(Model)
	if got := model.sendInput.Value(); got != "first\n" {
		t.Fatalf("chord outside compose changed composer to %q", got)
	}
}
