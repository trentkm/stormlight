package ui

// Keyboard routing for Stormlight's PTY view: while the pane holds focus the
// keyboard belongs to the agent's terminal, byte for byte. The docked view's
// left arrow and the seam chords hand it back to the dashboard.

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/pty"
	"slices"
)

// updateTerminalKey is every keypress while the Spanreed terminal holds
// focus. A focused terminal must receive letters, so the dashboard's keys
// here are modifier chords the hosted TUIs don't bind — the tmux-prefix
// insight without the prefix. Every alt chord has a ctrl+alt alias:
// tiling window managers (AeroSpace) commonly own plain alt+hjkl at the
// OS level, and ctrl+alt is claimed by nobody — not the WM, not the
// hosted TUIs. Everything else goes to the terminal, byte
// for byte; typing into an agent's terminal is the strongest possible form
// of having seen its result, so attention clears on the way through.
func (m Model) updateTerminalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "left" && m.dockedLeftExits():
		// Docked, left follows the visible pane hierarchy back to the roster.
		// A draft or the zoomed view keeps it in the hosted terminal for
		// cursor movement.
		m.activePane = paneAgents
		return m, nil
	case key == "ctrl+q" || key == "ctrl+space" || key == "ctrl+@":
		// The seam key: one step out, from anywhere inside. Zoom collapses
		// with it, so it always lands back on the roster.
		m.ptyZoom = false
		m.activePane = paneAgents
		return m, m.ensurePTYCmd()
	case slices.Contains(m.keys.AgentsNext, key):
		// Switch agents without stepping out: the portal swaps terminals
		// under the keyboard and the bar re-labels. The roster's cursor is
		// the target — plain moveSelection would scroll this terminal.
		m.moveSelectionIn(paneAgents, 1)
		return m, m.interactionFollowCmd()
	case slices.Contains(m.keys.AgentsPrevious, key):
		m.moveSelectionIn(paneAgents, -1)
		return m, m.interactionFollowCmd()
	case slices.Contains(m.keys.QueueNext, key):
		// The attention inbox, oldest first: cycle to whoever has waited
		// longest, wherever their workspace is.
		return m.jumpQueue(agent.QueueForward)
	case slices.Contains(m.keys.QueuePrevious, key):
		return m.jumpQueue(agent.QueueBack)
	case slices.Contains(m.keys.Zoom, key):
		m.ptyZoom = !m.ptyZoom
		return m, m.ensurePTYCmd()
	}
	widget, ok := m.selectedPTY()
	if !ok {
		m.activePane = paneAgents
		return m, nil
	}
	data := pty.KeyToBytes(msg)
	if len(data) == 0 {
		return m, nil
	}
	m.trackTerminalKey(msg)
	// Typing while scrolled back reads as "take me to the action".
	widget.ScrollToBottom()
	send := writeTerminalCmd(widget, data)
	if selected, ok := m.selectedAgent(); ok &&
		selected.ProcessLive &&
		(selected.Attention == agent.AttentionWaiting ||
			selected.EffectiveMark() == agent.MarkAttention) {
		m.markAttentionSeen(selected.ID)
		return m, tea.Batch(send, clearAttentionCmd(m.backend, selected.ID))
	}
	return m, send
}

type terminalInputState struct {
	agentID        string
	length, cursor int
}

func (m Model) dockedLeftExits() bool {
	return !m.ptyZoom &&
		(m.ptyInput.agentID != m.selectedAgentID() || m.ptyInput.cursor == 0)
}

func (m *Model) terminalInput() *terminalInputState {
	agentID := m.selectedAgentID()
	if m.ptyInput.agentID != agentID {
		m.ptyInput = terminalInputState{agentID: agentID}
	}
	return &m.ptyInput
}

func (m *Model) trackTerminalPaste(content string) {
	input := m.terminalInput()
	count := utf8.RuneCountInString(content)
	input.length += count
	input.cursor += count
}

// trackTerminalKey mirrors only edit operations needed to answer whether a
// draft exists. Unknown editing chords conservatively keep the draft active,
// which is preferable to stealing Left from text the provider owns.
func (m *Model) trackTerminalKey(msg tea.KeyPressMsg) {
	input := m.terminalInput()
	key := msg.String()
	switch {
	case msg.Text != "":
		count := utf8.RuneCountInString(msg.Text)
		input.length += count
		input.cursor += count
	case key == "left":
		input.cursor = max(0, input.cursor-1)
	case key == "right":
		input.cursor = min(input.length, input.cursor+1)
	case key == "home" || key == "ctrl+a":
		input.cursor = 0
	case key == "end" || key == "ctrl+e":
		input.cursor = input.length
	case key == "backspace" || key == "ctrl+backspace":
		if input.cursor > 0 {
			input.length--
			input.cursor--
		}
	case key == "delete":
		if input.cursor < input.length {
			input.length--
		}
	case key == "ctrl+u":
		input.length -= input.cursor
		input.cursor = 0
	case key == "ctrl+k":
		input.length = input.cursor
	case key == "enter" || key == "ctrl+c":
		input.length = 0
		input.cursor = 0
	}
}

func writeTerminalCmd(widget pty.Model, data []byte) tea.Cmd {
	return func() tea.Msg {
		if err := widget.Write(data); err != nil {
			diagnostic.Logger().Warn("terminal write failed", "error", err)
		}
		return nil
	}
}
