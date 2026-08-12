package ui

// Keyboard routing for the PTY Spanreed: while the pane holds focus the
// keyboard belongs to the agent's terminal, byte for byte. ctrl+q hands it
// back to the dashboard.

import (
	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/oathgate"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
)

// updateTerminalKey is every keypress while the Spanreed terminal holds
// focus. A focused terminal must receive letters, so the dashboard's keys
// here are modifier chords the hosted TUIs don't bind — the tmux-prefix
// insight without the prefix. Everything else goes to the terminal, byte
// for byte; typing into an agent's terminal is the strongest possible form
// of having seen its result, so attention clears on the way through.
func (m Model) updateTerminalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+q", "ctrl+space", "ctrl+@":
		// The seam key: one step out, from anywhere inside. Zoom collapses
		// with it, so it always lands back on the roster.
		m.ptyZoom = false
		m.activePane = paneAgents
		m.status = "Ready"
		return m, m.ensurePTYCmd()
	case "alt+j", "alt+down":
		// Switch agents without stepping out: the portal swaps terminals
		// under the keyboard and the bar re-labels. The roster's cursor is
		// the target — plain moveSelection would scroll this terminal.
		m.moveSelectionIn(paneAgents, 1)
		return m, m.interactionFollowCmd()
	case "alt+k", "alt+up":
		m.moveSelectionIn(paneAgents, -1)
		return m, m.interactionFollowCmd()
	case "alt+z":
		m.ptyZoom = !m.ptyZoom
		return m, m.ensurePTYCmd()
	case "alt+t":
		m.ptyZoom = false
		return m, tea.Batch(m.togglePTY(), m.ensurePTYCmd())
	}
	widget, ok := m.selectedPTY()
	if !ok {
		m.activePane = paneAgents
		return m, nil
	}
	data := oathgate.KeyToBytes(msg)
	if len(data) == 0 {
		return m, nil
	}
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

func writeTerminalCmd(widget oathgate.Model, data []byte) tea.Cmd {
	return func() tea.Msg {
		if err := widget.Write(data); err != nil {
			diagnostic.Logger().Warn("terminal write failed", "error", err)
		}
		return nil
	}
}
