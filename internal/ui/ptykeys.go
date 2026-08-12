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
// focus. Typing into an agent's terminal is the strongest possible form of
// having seen its result, so attention clears on the way through.
func (m Model) updateTerminalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+q" {
		m.activePane = paneAgents
		m.status = "Ready"
		return m, nil
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
