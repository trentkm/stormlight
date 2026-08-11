package ui

// Focus mode for the PTY Spanreed: every keypress becomes bytes on the
// agent's terminal, until ctrl+q hands the keyboard back to the dashboard.

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/trentkm/stormlight/internal/diagnostic"
)

func (m Model) updatePTYFocus(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+q" {
		m.mode = modeNormal
		m.status = "Terminal view"
		return m, nil
	}
	if m.pty == nil {
		m.mode = modeNormal
		return m, nil
	}
	data := keyToBytes(msg)
	if len(data) == 0 {
		return m, nil
	}
	// Typing while scrolled back reads as "take me to the action".
	m.pty.ScrollToBottom()
	return m, sendRawCmd(m.backend, m.pty.AgentID, data)
}

func sendRawCmd(backend Backend, id string, data []byte) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := backend.SendRaw(ctx, id, data); err != nil {
			diagnostic.Logger().Warn("pty send failed", "agent_id", id, "error", err)
		}
		return nil
	}
}

// keyToBytes translates a Bubble Tea key into the bytes a terminal
// application expects to read. Text input passes through verbatim; the
// table covers the control and navigation keys agents actually use.
// F-keys and CSI-u modifier encodings are out of the spike's scope.
func keyToBytes(msg tea.KeyPressMsg) []byte {
	if msg.Text != "" {
		return []byte(msg.Text)
	}
	switch key := msg.String(); key {
	case "enter":
		return []byte{'\r'}
	case "backspace":
		return []byte{0x7f}
	case "tab":
		return []byte{'\t'}
	case "shift+tab":
		return []byte("\x1b[Z")
	case "esc", "ctrl+[":
		return []byte{0x1b}
	case "space":
		return []byte{' '}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdown":
		return []byte("\x1b[6~")
	case "delete":
		return []byte("\x1b[3~")
	case "shift+enter", "alt+enter":
		// Providers read both as "newline without submitting".
		return []byte("\x1b\r")
	default:
		if len(key) == 6 && key[:5] == "ctrl+" {
			letter := key[5]
			if letter >= 'a' && letter <= 'z' {
				return []byte{letter & 0x1f}
			}
		}
		if len(key) == 5 && key[:4] == "alt+" {
			character := key[4]
			if character >= ' ' && character < 0x7f {
				return []byte{0x1b, character}
			}
		}
		return nil
	}
}
