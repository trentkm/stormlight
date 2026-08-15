package pty

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// KeyToBytes translates Bubble Tea keys to the bytes terminal applications
// read: plain keys as themselves, navigation and function keys as xterm
// sequences, with the standard modifier parameter (1 + shift·1 + alt·2 +
// ctrl·4) for modified forms.
func KeyToBytes(msg tea.KeyPressMsg) []byte {
	if msg.Text != "" {
		return []byte(msg.Text)
	}
	key := msg.String()
	switch key {
	case "enter":
		return []byte{'\r'}
	case "backspace":
		return []byte{0x7f}
	case "ctrl+backspace":
		return []byte{0x08}
	case "alt+backspace":
		return []byte{0x1b, 0x7f}
	case "tab":
		return []byte{'\t'}
	case "shift+tab":
		return []byte("\x1b[Z")
	case "esc", "ctrl+[":
		return []byte{0x1b}
	case "space":
		return []byte{' '}
	case "shift+enter", "alt+enter":
		return []byte("\x1b\r")
	}

	modifier, base := splitChord(key)

	// CSI keys: unmodified short forms, xterm modifier parameter otherwise.
	if final, ok := map[string]byte{
		"up": 'A', "down": 'B', "right": 'C', "left": 'D',
		"home": 'H', "end": 'F',
	}[base]; ok {
		if modifier == 1 {
			return []byte{0x1b, '[', final}
		}
		return fmt.Appendf(nil, "\x1b[1;%d%c", modifier, final)
	}

	// Tilde keys.
	if code, ok := map[string]int{
		"insert": 2, "delete": 3, "pgup": 5, "pgdown": 6,
	}[base]; ok {
		if modifier == 1 {
			return fmt.Appendf(nil, "\x1b[%d~", code)
		}
		return fmt.Appendf(nil, "\x1b[%d;%d~", code, modifier)
	}

	// Function keys: SS3 for the first four, tilde codes beyond.
	if ss3, ok := map[string]byte{
		"f1": 'P', "f2": 'Q', "f3": 'R', "f4": 'S',
	}[base]; ok {
		if modifier == 1 {
			return []byte{0x1b, 'O', ss3}
		}
		return fmt.Appendf(nil, "\x1b[1;%d%c", modifier, ss3)
	}
	if code, ok := map[string]int{
		"f5": 15, "f6": 17, "f7": 18, "f8": 19,
		"f9": 20, "f10": 21, "f11": 23, "f12": 24,
	}[base]; ok {
		if modifier == 1 {
			return fmt.Appendf(nil, "\x1b[%d~", code)
		}
		return fmt.Appendf(nil, "\x1b[%d;%d~", code, modifier)
	}

	// Control and alt characters.
	if len(key) == 6 && key[:5] == "ctrl+" && key[5] >= 'a' && key[5] <= 'z' {
		return []byte{key[5] & 0x1f}
	}
	if len(key) == 5 && key[:4] == "alt+" && key[4] >= ' ' && key[4] < 0x7f {
		return []byte{0x1b, key[4]}
	}
	return nil
}

// splitChord separates a Bubble Tea key name into its xterm modifier
// parameter and base key.
func splitChord(key string) (int, string) {
	modifier := 1
	for {
		switch {
		case strings.HasPrefix(key, "shift+"):
			modifier += 1
			key = key[len("shift+"):]
		case strings.HasPrefix(key, "alt+"):
			modifier += 2
			key = key[len("alt+"):]
		case strings.HasPrefix(key, "ctrl+"):
			modifier += 4
			key = key[len("ctrl+"):]
		default:
			return modifier, key
		}
	}
}
