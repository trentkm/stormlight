package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/trentkm/stormlight/internal/agent"
)

// ZedDiffOpener is the desktop-local half of a managed agent's request.
// host is empty for this machine and an SSH alias for a remote agent.
type ZedDiffOpener func(context.Context, string, []string) error

func (m *Model) nextZedDiffCmd(agents []agent.Agent) tea.Cmd {
	if m.openZedDiff == nil || m.zedDiffRunning {
		return nil
	}

	// Requests disappear after acknowledgement. Forget ids that are no
	// longer present so the map is bounded by the current roster rather than
	// by the lifetime of the dashboard.
	present := make(map[string]bool)
	for _, managedAgent := range agents {
		if managedAgent.ZedDiff != nil && managedAgent.ZedDiff.ID != "" {
			present[managedAgent.ZedDiff.ID] = true
		}
	}
	for id := range m.zedDiffSeen {
		if !present[id] {
			delete(m.zedDiffSeen, id)
		}
	}

	for _, managedAgent := range agents {
		request := managedAgent.ZedDiff
		if request == nil || request.ID == "" || m.zedDiffSeen[request.ID] {
			continue
		}
		copyOfRequest := *request
		copyOfRequest.Paths = append([]string(nil), request.Paths...)
		m.zedDiffSeen[request.ID] = true
		m.zedDiffRunning = true
		return zedDiffCmd(
			m.backend,
			m.openZedDiff,
			managedAgent,
			copyOfRequest,
		)
	}
	return nil
}

func zedDiffCmd(
	backend Backend,
	opener ZedDiffOpener,
	managedAgent agent.Agent,
	request agent.ZedDiffRequest,
) tea.Cmd {
	return func() tea.Msg {
		openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
		openErr := opener(openCtx, managedAgent.Host, request.Paths)
		cancelOpen()

		ackCtx, cancelAck := context.WithTimeout(context.Background(), 10*time.Second)
		ackErr := backend.AcknowledgeZedDiff(
			ackCtx,
			managedAgent.ID,
			request.ID,
		)
		cancelAck()

		if openErr != nil {
			openErr = fmt.Errorf("open %s in Zed: %w", managedAgent.Name, openErr)
		}
		if ackErr != nil {
			ackErr = fmt.Errorf("clear Zed request: %w", ackErr)
		}
		return zedDiffMsg{
			agentID:   managedAgent.ID,
			requestID: request.ID,
			err:       errors.Join(openErr, ackErr),
		}
	}
}
