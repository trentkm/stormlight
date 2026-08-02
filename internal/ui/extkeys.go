package ui

import (
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Terminals collapse modified chords like shift+enter into their plain
// forms unless the running program asks for an enhanced keyboard
// protocol, and Bubble Tea v1 never asks. Stormlight therefore requests
// xterm's modifyOtherKeys reporting itself around the program run and
// decodes the escape sequences that come back. tmux honours the request
// when its server has `extended-keys on` and re-encodes chords for the
// pane in either xterm (CSI 27;mods;code~) or CSI-u (CSI code;modsu)
// form, so both encodings are decoded here. Bubble Tea surfaces the
// sequences as its unexported unknownCSISequenceMsg, which
// DecodeModifiedKeys intercepts via tea.WithFilter.

const (
	// EnableModifiedKeys asks the terminal to report modified keys as
	// escape sequences (xterm modifyOtherKeys mode 2; mode 1 excludes
	// well-known keys such as enter, which is the whole point here).
	EnableModifiedKeys = "\x1b[>4;2m"
	// DisableModifiedKeys restores default key reporting on exit so the
	// shell that inherits the terminal sees ordinary keys again.
	DisableModifiedKeys = "\x1b[>4;0m"
)

// modifiedKeyMsg carries a decoded chord that Bubble Tea v1 cannot
// represent as a tea.KeyMsg. Its value matches the strings Update
// handlers already switch on.
type modifiedKeyMsg string

// chordShiftEnter is the one such chord Stormlight uses: the composer's
// insert-newline key.
const chordShiftEnter = modifiedKeyMsg("shift+enter")

// Modifier bits within the escape sequence parameter, which encodes the
// held modifiers as 1+bits.
const (
	modShift = 1 << iota
	modAlt
	modCtrl
)

// DecodeModifiedKeys is a tea.WithFilter hook that translates the
// escape sequences produced under modifyOtherKeys back into key
// messages. Chords with a Bubble Tea v1 representation become ordinary
// tea.KeyMsg values (so ctrl+c still quits a terminal that re-encodes
// it); shift+enter becomes a modifiedKeyMsg. Anything unrecognized
// passes through untouched.
func DecodeModifiedKeys(_ tea.Model, msg tea.Msg) tea.Msg {
	sequence, ok := rawKeySequence(msg)
	if !ok {
		return msg
	}
	if decoded, ok := decodeModifiedKey(sequence); ok {
		return decoded
	}
	return msg
}

// rawKeySequence extracts the bytes of Bubble Tea's unexported
// unknownCSISequenceMsg. The type is a plain named []byte, so
// reflection on the kind is the only way to reach it from outside the
// bubbletea package.
func rawKeySequence(msg tea.Msg) (string, bool) {
	value := reflect.ValueOf(msg)
	if !value.IsValid() || value.Kind() != reflect.Slice ||
		value.Type().Elem().Kind() != reflect.Uint8 {
		return "", false
	}
	sequence := string(value.Bytes())
	if !strings.HasPrefix(sequence, "\x1b[") {
		return "", false
	}
	return sequence, true
}

// decodeModifiedKey parses one modifyOtherKeys or CSI-u key report.
// The second result is true when the sequence was recognized, in which
// case the first result is the replacement message (nil to drop the
// event, e.g. for key releases).
func decodeModifiedKey(sequence string) (tea.Msg, bool) {
	if len(sequence) < 4 {
		return nil, false
	}
	body := sequence[2 : len(sequence)-1]
	code, mods, event := 0, 1, 0
	switch sequence[len(sequence)-1] {
	case '~': // xterm form: CSI 27 ; mods ; code ~
		parts := strings.Split(body, ";")
		if len(parts) != 3 || parts[0] != "27" {
			return nil, false
		}
		mods = atoiField(parts[1])
		code = atoiField(parts[2])
	case 'u': // CSI-u form: CSI code[:alternates] [; mods[:event]] u
		parts := strings.Split(body, ";")
		if len(parts) > 2 {
			return nil, false
		}
		code = atoiField(strings.SplitN(parts[0], ":", 2)[0])
		if len(parts) == 2 {
			modField := strings.SplitN(parts[1], ":", 2)
			mods = atoiField(modField[0])
			if len(modField) == 2 {
				event = atoiField(modField[1])
			}
		}
	default:
		return nil, false
	}
	if code <= 0 || mods < 1 {
		return nil, false
	}
	if event == 3 {
		// Key release (kitty-style event reporting): never a keystroke.
		return nil, true
	}

	bits := mods - 1
	shift := bits&modShift != 0
	alt := bits&modAlt != 0
	ctrl := bits&modCtrl != 0
	switch {
	case code == '\r' && shift:
		return chordShiftEnter, true
	case code == '\r':
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: alt}, true
	case code == '\t' && shift:
		return tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt}, true
	case code == '\t':
		return tea.KeyMsg{Type: tea.KeyTab, Alt: alt}, true
	case code == 0x1b:
		return tea.KeyMsg{Type: tea.KeyEscape, Alt: alt}, true
	case code == 0x08 || code == 0x7f:
		return tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt}, true
	case code == ' ' && ctrl:
		return tea.KeyMsg{Type: tea.KeyCtrlAt, Alt: alt}, true
	case code == ' ':
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}, Alt: alt}, true
	case ctrl && isLetter(code):
		lower := code | 0x20
		return tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(lower-'a'), Alt: alt}, true
	case !ctrl && code > ' ' && code != 0x7f && utf8.ValidRune(rune(code)):
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(code)}, Alt: alt}, true
	}
	return nil, false
}

func atoiField(field string) int {
	value, err := strconv.Atoi(field)
	if err != nil {
		return 0
	}
	return value
}

func isLetter(code int) bool {
	return (code >= 'a' && code <= 'z') || (code >= 'A' && code <= 'Z')
}
