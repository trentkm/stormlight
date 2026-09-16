package action

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

// recordingStore is the agent document as the dispatcher sees it: it
// records claims and acknowledgements and answers claims as told.
type recordingStore struct {
	mu       sync.Mutex
	claimErr error
	claims   []string
	acks     []string
}

func (s *recordingStore) ClaimDashboardAction(_ context.Context, agentID, requestID, claimer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, agentID+"/"+requestID+" by "+claimer)
	return s.claimErr
}

func (s *recordingStore) AcknowledgeDashboardAction(_ context.Context, agentID, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acks = append(s.acks, agentID+"/"+requestID)
	return nil
}

func request(id string) agent.DashboardActionRequest {
	return agent.DashboardActionRequest{ID: id, Name: "review-diff", Payload: json.RawMessage(`{"id":"` + id + `"}`)}
}

func TestDispatcherProposesTheHeadOfTheFirstFreeQueue(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	dispatcher := NewDispatcher(&recordingStore{}, func(context.Context, agent.Agent, agent.DashboardActionRequest) error {
		return errors.New("not today")
	})
	dispatcher.now = func() time.Time { return now }

	held := request("held")
	held.ClaimedBy, held.ClaimedAt = "another-dashboard", now.Add(-time.Second)
	stale := request("stale")
	stale.ClaimedBy, stale.ClaimedAt = "a-dead-dashboard", now.Add(-agent.DashboardActionClaimTTL)
	roster := []agent.Agent{
		{ID: "quiet"},
		// The head is another dashboard's; the request behind it waits
		// its turn rather than jumping the queue.
		{ID: "busy", DashboardActions: []agent.DashboardActionRequest{held, request("behind-held")}},
		{ID: "abandoned", DashboardActions: []agent.DashboardActionRequest{stale}},
		{ID: "waiting", DashboardActions: []agent.DashboardActionRequest{request("fresh")}},
	}

	managedAgent, proposed, ok := dispatcher.Next(roster)
	if !ok || managedAgent.ID != "abandoned" || proposed.ID != "stale" {
		t.Fatalf("proposed %s/%s, want the stale claim on abandoned", managedAgent.ID, proposed.ID)
	}
	// One at a time: nothing more is proposed until Run has finished.
	if _, again, ok := dispatcher.Next(roster); ok {
		t.Fatalf("a second proposal %q while the first is in flight", again.ID)
	}
	if err := dispatcher.Run(context.Background(), managedAgent, proposed); err == nil ||
		!strings.Contains(err.Error(), "not today") {
		t.Fatalf("the handler's refusal did not come back: %v", err)
	}
	// With the claim fresh again (Run does not rewrite the roster; the
	// next poll will), the same roster proposes the next free head.
	roster[2].DashboardActions[0].ClaimedBy, roster[2].DashboardActions[0].ClaimedAt = dispatcher.claimer, now
	managedAgent, proposed, ok = dispatcher.Next(roster)
	if !ok || managedAgent.ID != "waiting" || proposed.ID != "fresh" {
		t.Fatalf("proposed %s/%s, want fresh on waiting", managedAgent.ID, proposed.ID)
	}
}

func TestDispatcherClaimsRunsAndRetires(t *testing.T) {
	store := &recordingStore{}
	var handled []string
	dispatcher := NewDispatcher(store, func(_ context.Context, managedAgent agent.Agent, request agent.DashboardActionRequest) error {
		handled = append(handled, managedAgent.Host+":"+managedAgent.ID+"/"+request.ID+" "+string(request.Payload))
		if request.ID == "failing" {
			return errors.New("plugin failed")
		}
		return nil
	})
	roster := []agent.Agent{{
		ID: "agent-one", Name: "review fixer", Host: "cloud",
		DashboardActions: []agent.DashboardActionRequest{request("first"), request("failing")},
	}}

	managedAgent, proposed, ok := dispatcher.Next(roster)
	if !ok || proposed.ID != "first" {
		t.Fatalf("proposed %q", proposed.ID)
	}
	if err := dispatcher.Run(context.Background(), managedAgent, proposed); err != nil {
		t.Fatal(err)
	}
	roster[0].DashboardActions = roster[0].DashboardActions[1:]
	managedAgent, proposed, ok = dispatcher.Next(roster)
	if !ok || proposed.ID != "failing" {
		t.Fatalf("proposed %q", proposed.ID)
	}
	err := dispatcher.Run(context.Background(), managedAgent, proposed)
	if err == nil || !strings.Contains(err.Error(), "plugin failed") {
		t.Fatalf("a failing plugin reported %v", err)
	}

	if len(handled) != 2 ||
		handled[0] != `cloud:agent-one/first {"id":"first"}` ||
		handled[1] != `cloud:agent-one/failing {"id":"failing"}` {
		t.Fatalf("handled = %q", handled)
	}
	claimer := dispatcher.claimer
	if len(store.claims) != 2 ||
		store.claims[0] != "agent-one/first by "+claimer ||
		store.claims[1] != "agent-one/failing by "+claimer {
		t.Fatalf("claims = %q", store.claims)
	}
	// Failure is retired too: a broken plugin is reported once, not
	// retried on every poll.
	if len(store.acks) != 2 || store.acks[0] != "agent-one/first" || store.acks[1] != "agent-one/failing" {
		t.Fatalf("acks = %q", store.acks)
	}
}

func TestDispatcherYieldsARequestAnotherDashboardTook(t *testing.T) {
	for _, refusal := range []error{agent.ErrDashboardActionHeld, agent.ErrDashboardActionGone} {
		store := &recordingStore{claimErr: refusal}
		ran := false
		dispatcher := NewDispatcher(store, func(context.Context, agent.Agent, agent.DashboardActionRequest) error {
			ran = true
			return nil
		})
		roster := []agent.Agent{{ID: "agent-one", DashboardActions: []agent.DashboardActionRequest{request("contested")}}}
		managedAgent, proposed, ok := dispatcher.Next(roster)
		if !ok {
			t.Fatal("nothing proposed")
		}
		if err := dispatcher.Run(context.Background(), managedAgent, proposed); err != nil {
			t.Fatalf("losing a claim to %v is not an error, got %v", refusal, err)
		}
		if ran {
			t.Fatalf("the handler ran on a request refused with %v", refusal)
		}
		if len(store.acks) != 0 {
			t.Fatalf("retired another dashboard's request: %q", store.acks)
		}
		// And the dispatcher is free again for the next poll.
		if _, _, ok := dispatcher.Next(roster); !ok {
			t.Fatal("still busy after yielding")
		}
	}
}

func TestDispatcherReportsAClaimThatCouldNotBeWritten(t *testing.T) {
	store := &recordingStore{claimErr: errors.New("daemon unreachable")}
	dispatcher := NewDispatcher(store, func(context.Context, agent.Agent, agent.DashboardActionRequest) error {
		t.Fatal("the handler ran without a claim")
		return nil
	})
	roster := []agent.Agent{{ID: "agent-one", DashboardActions: []agent.DashboardActionRequest{request("r")}}}
	managedAgent, proposed, _ := dispatcher.Next(roster)
	err := dispatcher.Run(context.Background(), managedAgent, proposed)
	if err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Fatalf("error = %v", err)
	}
}
