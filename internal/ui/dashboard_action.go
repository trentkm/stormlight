package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/agent"
)

// DashboardActionRunner is the desktop-local half of an action plugin. The
// prepare phase ran on the agent's machine; this phase runs where the
// dashboard and the user's desktop applications live.
type DashboardActionRunner func(
	context.Context,
	agent.Agent,
	agent.DashboardActionRequest,
) error

type dashboardActionKey struct {
	agentID   string
	requestID string
}

func (m *Model) nextDashboardActionCmd(agents []agent.Agent) tea.Cmd {
	if m.runDashboardAction == nil || m.dashboardActionRunning {
		return nil
	}

	// Requests disappear after acknowledgement. Forget ids that are no
	// longer present so the map is bounded by the current roster rather than
	// by the lifetime of the dashboard.
	present := make(map[dashboardActionKey]bool)
	for _, managedAgent := range agents {
		if managedAgent.DashboardAction != nil &&
			managedAgent.DashboardAction.ID != "" {
			present[dashboardActionKey{
				agentID:   managedAgent.ID,
				requestID: managedAgent.DashboardAction.ID,
			}] = true
		}
	}
	for key := range m.dashboardActionSeen {
		if !present[key] {
			delete(m.dashboardActionSeen, key)
		}
	}

	for _, managedAgent := range agents {
		request := managedAgent.DashboardAction
		if request == nil || request.ID == "" {
			continue
		}
		key := dashboardActionKey{
			agentID:   managedAgent.ID,
			requestID: request.ID,
		}
		if m.dashboardActionSeen[key] {
			continue
		}
		copyOfRequest := *request
		copyOfRequest.Payload = append([]byte(nil), request.Payload...)
		m.dashboardActionSeen[key] = true
		m.dashboardActionRunning = true
		return dashboardActionCmd(
			m.backend,
			m.runDashboardAction,
			managedAgent,
			copyOfRequest,
		)
	}
	return nil
}

func dashboardActionCmd(
	backend Backend,
	runner DashboardActionRunner,
	managedAgent agent.Agent,
	request agent.DashboardActionRequest,
) tea.Cmd {
	return func() tea.Msg {
		runCtx, cancelRun := context.WithTimeout(
			context.Background(),
			30*time.Second,
		)
		runErr := runner(runCtx, managedAgent, request)
		cancelRun()

		ackCtx, cancelAck := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		ackErr := backend.AcknowledgeDashboardAction(
			ackCtx,
			managedAgent.ID,
			request.ID,
		)
		cancelAck()

		if runErr != nil {
			runErr = fmt.Errorf(
				"run dashboard action %q for %s: %w",
				request.Name,
				managedAgent.Name,
				runErr,
			)
		}
		if ackErr != nil {
			ackErr = fmt.Errorf("clear dashboard action: %w", ackErr)
		}
		return dashboardActionMsg{
			agentID:   managedAgent.ID,
			requestID: request.ID,
			action:    request.Name,
			err:       errors.Join(runErr, ackErr),
		}
	}
}
