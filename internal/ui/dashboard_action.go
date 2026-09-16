package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/action"
	"github.com/trentkm/stormlight/internal/agent"
)

// DashboardActionRunner is the desktop-local half of an action plugin. The
// prepare phase ran on the agent's machine; this phase runs where the
// dashboard and the user's desktop applications live.
type DashboardActionRunner = action.Handler

// nextDashboardActionCmd asks the dispatcher whether this roster holds a
// request for this dashboard to run, and runs it off the update loop. The
// dispatcher owns the rules — one at a time, claimed before run, retired
// after — so the TUI and the web server behave the same.
func (m *Model) nextDashboardActionCmd(agents []agent.Agent) tea.Cmd {
	if m.actions == nil {
		return nil
	}
	managedAgent, request, ok := m.actions.Next(agents)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return dashboardActionMsg{
			agentID:   managedAgent.ID,
			requestID: request.ID,
			action:    request.Name,
			err:       m.actions.Run(context.Background(), managedAgent, request),
		}
	}
}
