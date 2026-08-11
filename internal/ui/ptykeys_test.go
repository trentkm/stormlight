package ui

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestKeyToBytes(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
		want []byte
	}{
		{"text passes through", tea.KeyPressMsg{Text: "héllo"}, []byte("héllo")},
		{"enter is CR", tea.KeyPressMsg{Code: tea.KeyEnter}, []byte{'\r'}},
		{"backspace is DEL", tea.KeyPressMsg{Code: tea.KeyBackspace}, []byte{0x7f}},
		{"escape", tea.KeyPressMsg{Code: tea.KeyEscape}, []byte{0x1b}},
		{"up arrow", tea.KeyPressMsg{Code: tea.KeyUp}, []byte("\x1b[A")},
		{"page down", tea.KeyPressMsg{Code: tea.KeyPgDown}, []byte("\x1b[6~")},
		{"shift+tab is backtab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, []byte("\x1b[Z")},
		{"ctrl+c is ETX", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, []byte{0x03}},
		{"ctrl+z is SUB", tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}, []byte{0x1a}},
		{"alt+b is ESC b", tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt}, []byte{0x1b, 'b'}},
		{"bare F1 is dropped", tea.KeyPressMsg{Code: tea.KeyF1}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := keyToBytes(testCase.msg)
			if !bytes.Equal(got, testCase.want) {
				t.Fatalf("keyToBytes(%q) = %q, want %q",
					testCase.msg.String(), got, testCase.want)
			}
		})
	}
}
