package fleet

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// stub is one daemon's worth of runtime: a roster it will report and a
// record of what was asked of it.
type stub struct {
	mu       sync.Mutex
	agents   []agent.Agent
	listErr  error
	sent     []string
	deleted  []string
	launched []session.DispatchRequest
	lists    int
}

func (s *stub) ListAgents(context.Context) ([]agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]agent.Agent(nil), s.agents...), nil
}

func (s *stub) Dispatch(
	_ context.Context,
	request session.DispatchRequest,
) (agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launched = append(s.launched, request)
	return agent.Agent{ID: "new", Workspace: request.Workspace}, nil
}

func (s *stub) Send(_ context.Context, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, id+":"+message)
	return nil
}

func (s *stub) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stub) Capture(context.Context, string, int) (string, error) { return "", nil }
func (s *stub) Attach(context.Context, string) (session.AttachResult, error) {
	return session.AttachResult{}, nil
}
func (s *stub) Interrupt(context.Context, string) error              { return nil }
func (s *stub) Rename(context.Context, string, string) error         { return nil }
func (s *stub) Update(context.Context, string, session.Update) error { return nil }
func (s *stub) SetWorkspace(context.Context, string, workspace.Context) error {
	return nil
}

func reachable(host string, runtime session.Runtime) Member {
	return Member{Host: host, Connect: func() (session.Runtime, error) { return runtime, nil }}
}

func unreachable(host string, err error) Member {
	return Member{Host: host, Connect: func() (session.Runtime, error) { return nil, err }}
}

// TestRosterMergesAndStampsTheHost: one dashboard, one roster, and every
// agent knowing which machine answered for it.
func TestRosterMergesAndStampsTheHost(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}, {ID: "ccc"}}}
	f := New(reachable("", local), reachable("devbox", devbox))

	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("want every host's agents, got %+v", agents)
	}
	hosts := map[string]string{}
	for _, listed := range agents {
		hosts[listed.ID] = listed.Host
	}
	if hosts["aaa"] != "" || hosts["bbb"] != "devbox" || hosts["ccc"] != "devbox" {
		t.Fatalf("host not stamped from the daemon that answered: %v", hosts)
	}
}

// TestAnUnreachableHostIsItsOwnAbsence: a laptop that closed, a VPN that
// dropped, a box that is asleep. None of them are the dashboard's
// failure, and none of them may hide the agents that are still there.
func TestAnUnreachableHostIsItsOwnAbsence(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	f := New(reachable("", local), unreachable("devbox", errors.New("ssh: no route to host")))

	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("one host down must not fail the refresh: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "aaa" {
		t.Fatalf("the reachable host's agents must still be listed: %+v", agents)
	}

	// It is reported, though — silently listing fewer agents is how a
	// dashboard tells a comfortable lie.
	var reported bool
	for _, status := range f.Status() {
		if status.Host == "devbox" && status.Error != nil {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("an unreachable host must be visible as unreachable: %+v", f.Status())
	}
}

// TestEveryHostDownIsAnError: an empty roster reads as a quiet morning.
// When nothing could be reached, say so.
func TestEveryHostDownIsAnError(t *testing.T) {
	f := New(
		unreachable("", errors.New("no daemon")),
		unreachable("devbox", errors.New("ssh: no route to host")),
	)
	if _, err := f.ListAgents(context.Background()); err == nil {
		t.Fatal("no reachable host must not look like no agents")
	}
}

// TestOperationsFollowTheAgentToItsHost: every id-addressed call has to
// land on the daemon that holds the agent, or a message meant for one
// machine is typed into another.
func TestOperationsFollowTheAgentToItsHost(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(reachable("", local), reachable("devbox", devbox))

	if err := f.Send(context.Background(), "bbb", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(devbox.sent) != 1 || len(local.sent) != 0 {
		t.Fatalf("message went to the wrong machine: local=%v devbox=%v", local.sent, devbox.sent)
	}
	if err := f.Delete(context.Background(), "aaa"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(local.deleted) != 1 || len(devbox.deleted) != 0 {
		t.Fatalf("deletion went to the wrong machine: local=%v devbox=%v",
			local.deleted, devbox.deleted)
	}
}

// TestAnAgentNobodyHasListedIsStillFound: dispatch returns an id before
// any refresh has run, and the very next keystroke may address it.
func TestAnAgentNobodyHasListedIsStillFound(t *testing.T) {
	local := &stub{}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(reachable("", local), reachable("devbox", devbox))

	// No ListAgents has been called, so ownership is unknown.
	if err := f.Send(context.Background(), "bbb", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(devbox.sent) != 1 {
		t.Fatalf("the fleet should have gone looking: %v", devbox.sent)
	}
	if _, err := f.Capture(context.Background(), "nobody", 10); err == nil {
		t.Fatal("an id no host holds must be an error, not a silent no-op")
	}
}

// TestAPrefixNamesAnAgentAcrossHosts: a human types the first few
// characters of an id, and the dashboard passes that straight through.
func TestAPrefixNamesAnAgentAcrossHosts(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa11111"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb22222"}}}
	f := New(reachable("", local), reachable("devbox", devbox))
	if _, err := f.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}

	if err := f.Send(context.Background(), "bbb2", "hello"); err != nil {
		t.Fatalf("Send by prefix: %v", err)
	}
	if len(devbox.sent) != 1 {
		t.Fatalf("prefix did not reach the right host: %v", devbox.sent)
	}
}

// TestDispatchFollowsTheWorkspace: the workspace was resolved on the
// machine that owns it, so it already says where the agent belongs.
func TestDispatchFollowsTheWorkspace(t *testing.T) {
	local := &stub{}
	devbox := &stub{}
	f := New(reachable("", local), reachable("devbox", devbox))

	dispatched, err := f.Dispatch(context.Background(), session.DispatchRequest{
		Cwd:       "/srv/api",
		Workspace: workspace.Context{Host: "devbox", ID: "devbox:git:/srv/api/.git"},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(devbox.launched) != 1 || len(local.launched) != 0 {
		t.Fatalf("dispatch landed on the wrong machine")
	}
	if dispatched.Host != "devbox" {
		t.Fatalf("a dispatched agent knows its host: %+v", dispatched)
	}

	_, err = f.Dispatch(context.Background(), session.DispatchRequest{
		Workspace: workspace.Context{Host: "nowhere"},
	})
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("an unknown host must be named in the error: %v", err)
	}
}

// TestAFailedHostIsNotDialledEveryRefresh: the dashboard refreshes on a
// timer, and an SSH attempt per refresh against a sleeping laptop is a
// stall the user feels on every frame.
func TestAFailedHostIsNotDialledEveryRefresh(t *testing.T) {
	attempts := 0
	f := New(
		reachable("", &stub{}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			attempts++
			return nil, errors.New("ssh: connection timed out")
		}},
	)
	for range 5 {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
	}
	if attempts != 1 {
		t.Fatalf("dialled %d times in a row; the retry window should hold it to 1", attempts)
	}
}

// TestALostConnectionIsRebuilt: a tunnel dies with the network it ran
// over, and every later call through it fails the same way until
// something drops it.
func TestALostConnectionIsRebuilt(t *testing.T) {
	broken := &stub{listErr: errors.New("broken pipe")}
	healthy := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	connects := 0
	f := New(
		reachable("", &stub{}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			connects++
			if connects == 1 {
				return broken, nil
			}
			return healthy, nil
		}},
	)

	if _, err := f.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	// The retry window applies to the dropped connection too, so reach
	// past it the way the next refresh eventually does.
	f.members[1].failedAt = f.members[1].failedAt.Add(-2 * retryAfter)

	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if connects != 2 {
		t.Fatalf("a dead connection must be rebuilt, not reused: %d connects", connects)
	}
	if len(agents) != 1 || agents[0].ID != "bbb" {
		t.Fatalf("the recovered host's agents should be back: %+v", agents)
	}
}

// TestAFleetOfOneIsJustTheRuntime: everyone who has configured no hosts
// pays for this package on every call, so the single-member path routes
// straight through without a listing.
func TestAFleetOfOneIsJustTheRuntime(t *testing.T) {
	local := &stub{}
	f := New(reachable("", local))

	if err := f.Send(context.Background(), "aaa", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if local.lists != 0 {
		t.Fatalf("a fleet of one should not have to look anything up: %d listings", local.lists)
	}
	if len(local.sent) != 1 {
		t.Fatalf("the message never arrived: %v", local.sent)
	}
}
