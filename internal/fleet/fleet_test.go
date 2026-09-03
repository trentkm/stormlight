package fleet

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// stub is one daemon's worth of runtime: a roster it will report and a
// record of what was asked of it.
type stub struct {
	mu            sync.Mutex
	agents        []agent.Agent
	listErr       error
	sent          []string
	commands      []string
	deleted       []string
	launched      []session.DispatchRequest
	lists         int
	readPaths     []string
	attachErr     error
	attachErrors  map[string]error
	attachCalls   int
	attachEntered chan struct{}
	attachRelease chan struct{}
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

func (s *stub) SendCommand(_ context.Context, id, command string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, id+":"+command)
	return nil
}

func (s *stub) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stub) Capture(context.Context, string, int) (string, error) { return "", nil }

func (s *stub) ReadAgentFile(
	_ context.Context,
	_ string,
	path string,
) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readPaths = append(s.readPaths, path)
	return io.NopCloser(strings.NewReader("contents of " + path)), nil
}
func (s *stub) Attach(context.Context, string) (session.AttachResult, error) {
	return session.AttachResult{}, nil
}
func (s *stub) AttachTerminal(
	_ context.Context,
	id string,
	_ int,
	_ int,
) (session.TerminalStream, error) {
	s.mu.Lock()
	s.attachCalls++
	entered, release, err := s.attachEntered, s.attachRelease, s.attachErr
	if specific := s.attachErrors[id]; specific != nil {
		err = specific
	}
	s.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	return fleetTestTransport{}, nil
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

type fleetTestTransport struct{}

func (fleetTestTransport) Seed() pty.Message                      { return pty.Message{} }
func (fleetTestTransport) Output() <-chan pty.Message             { return make(chan pty.Message) }
func (fleetTestTransport) Write([]byte) error                     { return nil }
func (fleetTestTransport) Resize(context.Context, int, int) error { return nil }
func (fleetTestTransport) Close()                                 {}

// eventually polls the way the dashboard does. A remote member's dial
// runs in the background and the refresh that started it does not wait,
// so the state a test is after arrives on a later refresh rather than the
// first one — which is the whole point of the design and therefore has to
// be what the tests exercise.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	for range 500 {
		if check() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRosterMergesAndStampsTheHost: one dashboard, one roster, and every
// agent knowing which machine answered for it.
func TestRosterMergesAndStampsTheHost(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}, {ID: "ccc"}}}
	f := New(nil, reachable("", local), reachable("devbox", devbox))

	var agents []agent.Agent
	eventually(t, "every host to have answered", func() bool {
		var err error
		agents, err = f.ListAgents(context.Background())
		if err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		return len(agents) == 3
	})
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
	f := New(nil, reachable("", local), unreachable("devbox", errors.New("ssh: no route to host")))

	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("one host down must not fail the refresh: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "aaa" {
		t.Fatalf("the reachable host's agents must still be listed: %+v", agents)
	}

	// It is reported, though — silently listing fewer agents is how a
	// dashboard tells a comfortable lie. The report lands when the dial
	// gives up, not before: until then the honest word is "reaching".
	eventually(t, "the unreachable host to be reported", func() bool {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("one host down must not fail the refresh: %v", err)
		}
		for _, status := range f.Status() {
			if status.Host == "devbox" && status.Error != nil {
				return true
			}
		}
		return false
	})
}

// TestAHostBeingReachedIsNotAHostWithNoAgents: the first refresh after a
// launch has not connected to anything remote yet, and an empty roster is
// the wrong thing to say about a machine nobody has finished asking.
func TestAHostBeingReachedIsNotAHostWithNoAgents(t *testing.T) {
	release := make(chan struct{})
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(nil, reachable("", &stub{agents: []agent.Agent{{ID: "aaa"}}}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			<-release
			return devbox, nil
		}},
	)

	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("a dial in flight must not fail the refresh: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "aaa" {
		t.Fatalf("this machine's agents must not wait on a handshake: %+v", agents)
	}
	if reaching := f.Reaching(); len(reaching) != 1 || reaching[0] != "devbox" {
		t.Fatalf("the host being reached must say so: %v", reaching)
	}

	close(release)
	eventually(t, "the reached host to join the roster", func() bool {
		agents, err := f.ListAgents(context.Background())
		if err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		return len(agents) == 2
	})
	if reaching := f.Reaching(); len(reaching) != 0 {
		t.Fatalf("a connected host is not still being reached: %v", reaching)
	}
}

// TestOneDialServesEveryoneWaitingOnIt: a refresh starts the handshake
// and walks away; a dispatch aimed at the same machine a moment later
// must take that handshake's answer rather than opening a second
// connection beside it.
func TestOneDialServesEveryoneWaitingOnIt(t *testing.T) {
	release := make(chan struct{})
	var connects atomic.Int64
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(nil, reachable("", &stub{}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			connects.Add(1)
			<-release
			return devbox, nil
		}},
	)

	if _, err := f.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	sent := make(chan error, 1)
	go func() { sent <- f.Send(context.Background(), "bbb", "hello") }()
	// The send is now waiting on the dial the refresh started.
	close(release)
	if err := <-sent; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("one machine, one handshake: %d connects", got)
	}
}

func TestOneFailedTerminalAttachOpensTheCircuitForTheHost(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	devbox := &stub{
		agents:        []agent.Agent{{ID: "bbb"}, {ID: "ccc"}},
		attachErr:     errors.New("ssh: connection timed out"),
		attachEntered: entered,
		attachRelease: release,
	}
	f := New(nil, reachable("", &stub{}), reachable("devbox", devbox))
	eventually(t, "remote ownership to be known", func() bool {
		agents, err := f.ListAgents(context.Background())
		return err == nil && len(agents) == 2
	})

	results := make(chan error, 2)
	go func() {
		_, err := f.AttachTerminal(context.Background(), "bbb", 80, 24)
		results <- err
	}()
	<-entered
	go func() {
		_, err := f.AttachTerminal(context.Background(), "ccc", 80, 24)
		results <- err
	}()
	close(release)
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("the failed host attach unexpectedly succeeded")
		}
	}
	if devbox.attachCalls != 1 {
		t.Fatalf("one host failure caused %d terminal attempts", devbox.attachCalls)
	}

	if _, err := f.AttachTerminal(context.Background(), "bbb", 80, 24); err == nil {
		t.Fatal("the open circuit must reject another immediate retry")
	}
	if devbox.attachCalls != 1 {
		t.Fatalf("the open circuit retried immediately: %d attempts", devbox.attachCalls)
	}
}

func TestAnAgentFailureDoesNotOpenTheHostCircuit(t *testing.T) {
	devbox := &stub{
		agents: []agent.Agent{{ID: "bbb"}, {ID: "ccc"}},
		attachErrors: map[string]error{
			"bbb": errors.New(`agent "bbb" not found`),
		},
	}
	f := New(nil, reachable("", &stub{}), reachable("devbox", devbox))
	eventually(t, "remote ownership to be known", func() bool {
		agents, err := f.ListAgents(context.Background())
		return err == nil && len(agents) == 2
	})

	if _, err := f.AttachTerminal(context.Background(), "bbb", 80, 24); err == nil {
		t.Fatal("the missing agent unexpectedly attached")
	}
	stream, err := f.AttachTerminal(context.Background(), "ccc", 80, 24)
	if err != nil {
		t.Fatalf("another agent on the healthy host was blocked: %v", err)
	}
	stream.Close()
	if devbox.attachCalls != 2 {
		t.Fatalf("agent failure should not open host circuit: %d calls", devbox.attachCalls)
	}
}

// TestEveryHostDownIsAnError: an empty roster reads as a quiet morning.
// When nothing could be reached, say so.
func TestEveryHostDownIsAnError(t *testing.T) {
	f := New(nil, unreachable("", errors.New("no daemon")),
		unreachable("devbox", errors.New("ssh: no route to host")),
	)
	// The verdict waits for the last dial: while one is in flight the
	// refresh is loading, not failing, and an error card over a dashboard
	// that is merely connecting is its own kind of wrong.
	eventually(t, "every host to be known down", func() bool {
		_, err := f.ListAgents(context.Background())
		return err != nil
	})
}

// TestOperationsFollowTheAgentToItsHost: every id-addressed call has to
// land on the daemon that holds the agent, or a message meant for one
// machine is typed into another.
func TestOperationsFollowTheAgentToItsHost(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(nil, reachable("", local), reachable("devbox", devbox))

	if err := f.Send(context.Background(), "bbb", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(devbox.sent) != 1 || len(local.sent) != 0 {
		t.Fatalf("message went to the wrong machine: local=%v devbox=%v", local.sent, devbox.sent)
	}
	if err := f.SendCommand(
		context.Background(),
		"bbb",
		"/rename remote-agent",
	); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if len(devbox.commands) != 1 || len(local.commands) != 0 {
		t.Fatalf("command went to the wrong machine: local=%v devbox=%v",
			local.commands, devbox.commands)
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
	f := New(nil, reachable("", local), reachable("devbox", devbox))

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
	f := New(nil, reachable("", local), reachable("devbox", devbox))
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
	f := New(nil, reachable("", local), reachable("devbox", devbox))

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
	var attempts atomic.Int64
	f := New(nil, reachable("", &stub{}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			attempts.Add(1)
			return nil, errors.New("ssh: connection timed out")
		}},
	)
	eventually(t, "the first dial to fail", func() bool {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		return len(f.Reaching()) == 0 && attempts.Load() > 0
	})
	for range 5 {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("dialled %d times in a row; the retry window should hold it to 1", got)
	}
}

// TestALostConnectionIsRebuilt: a tunnel dies with the network it ran
// over, and every later call through it fails the same way until
// something drops it.
func TestALostConnectionIsRebuilt(t *testing.T) {
	broken := &stub{listErr: errors.New("broken pipe")}
	healthy := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	var connects atomic.Int64
	f := New(nil, reachable("", &stub{}),
		Member{Host: "devbox", Connect: func() (session.Runtime, error) {
			if connects.Add(1) == 1 {
				return broken, nil
			}
			return healthy, nil
		}},
	)

	// Reach past the dial, and past the listing through it that finds the
	// pipe broken.
	eventually(t, "the broken connection to be dropped", func() bool {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		f.members[1].mu.Lock()
		defer f.members[1].mu.Unlock()
		return f.members[1].failure != nil
	})
	// The retry window applies to the dropped connection too, so reach
	// past it the way the next refresh eventually does.
	f.members[1].mu.Lock()
	f.members[1].failedAt = f.members[1].failedAt.Add(-2 * retryAfter)
	f.members[1].mu.Unlock()

	var agents []agent.Agent
	eventually(t, "the recovered host to be back", func() bool {
		var err error
		agents, err = f.ListAgents(context.Background())
		if err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		return len(agents) == 1
	})
	if got := connects.Load(); got != 2 {
		t.Fatalf("a dead connection must be rebuilt, not reused: %d connects", got)
	}
	if agents[0].ID != "bbb" {
		t.Fatalf("the recovered host's agents should be back: %+v", agents)
	}
}

// TestAFleetOfOneIsJustTheRuntime: everyone who has configured no hosts
// pays for this package on every call, so the single-member path routes
// straight through without a listing.
func TestAFleetOfOneIsJustTheRuntime(t *testing.T) {
	local := &stub{}
	f := New(nil, reachable("", local))

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

// TestFileReadsFollowTheAgentToItsHost: a transcript path names a file on
// the agent's own machine, and reading it here would find nothing — or,
// worse, find something that happens to share the path.
func TestFileReadsFollowTheAgentToItsHost(t *testing.T) {
	local := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	devbox := &stub{agents: []agent.Agent{{ID: "bbb"}}}
	f := New(nil, reachable("", local), reachable("devbox", devbox))

	source, err := f.ReadAgentFile(context.Background(), "bbb", "/home/trent/t.jsonl")
	if err != nil {
		t.Fatalf("ReadAgentFile: %v", err)
	}
	defer source.Close()
	content, err := io.ReadAll(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "contents of /home/trent/t.jsonl" {
		t.Fatalf("content = %q", content)
	}
	if len(devbox.readPaths) != 1 || len(local.readPaths) != 0 {
		t.Fatalf("read went to the wrong machine: local=%v devbox=%v",
			local.readPaths, devbox.readPaths)
	}
}

// TestTheLocalDaemonIsRetriedImmediately: a daemon that died takes its
// agents with it, and the runtime will start a new one on the next
// connect. Holding the local member off for the remote retry window
// leaves the dashboard dark for half a minute after it could have been
// back — which is what a killed daemon looked like before this.
func TestTheLocalDaemonIsRetriedImmediately(t *testing.T) {
	healthy := &stub{agents: []agent.Agent{{ID: "aaa"}}}
	connects := 0
	f := New(nil, Member{Host: "", Connect: func() (session.Runtime, error) {
		connects++
		if connects == 1 {
			return nil, errors.New("dial unix: connection refused")
		}
		return healthy, nil
	}})

	if _, err := f.ListAgents(context.Background()); err == nil {
		t.Fatal("the first refresh should report the daemon down")
	}
	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("the very next refresh should reconnect: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "aaa" {
		t.Fatalf("agents = %+v", agents)
	}
}

// TestARemoteHostStillWaits: the window earns its keep where dialling
// costs an SSH handshake.
func TestARemoteHostStillWaits(t *testing.T) {
	var attempts atomic.Int64
	f := New(nil, reachable("", &stub{}), Member{
		Host: "devbox",
		Connect: func() (session.Runtime, error) {
			attempts.Add(1)
			return nil, errors.New("ssh: connection timed out")
		},
	})
	eventually(t, "the first dial to fail", func() bool {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
		return len(f.Reaching()) == 0 && attempts.Load() > 0
	})
	for range 3 {
		if _, err := f.ListAgents(context.Background()); err != nil {
			t.Fatalf("ListAgents: %v", err)
		}
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("dialled %d times; the window should hold it to 1", got)
	}
}

// TestAHostJoinsWhenSomethingNamesIt: a machine picked out of
// ~/.ssh/config, or named by a workspace added while the dashboard was
// running, has to work without a restart and without being configured.
// Configuration says how a host differs from its name — it is not the
// list of which hosts there are.
func TestAHostJoinsWhenSomethingNamesIt(t *testing.T) {
	discovered := &stub{}
	asked := []string{}
	f := New(
		func(host string) Member {
			asked = append(asked, host)
			return reachable(host, discovered)
		},
		reachable("", &stub{}),
	)

	_, err := f.Dispatch(context.Background(), session.DispatchRequest{
		Workspace: workspace.Context{Host: "devbox"},
	})
	if err != nil {
		t.Fatalf("Dispatch to an undeclared host: %v", err)
	}
	if len(discovered.launched) != 1 {
		t.Fatalf("the discovered host should have run it: %+v", discovered.launched)
	}
	if len(asked) != 1 || asked[0] != "devbox" {
		t.Fatalf("discovery asked for %v", asked)
	}

	// It joined: the next dispatch reuses it rather than discovering again,
	// and its agents appear in the roster.
	if _, err := f.Dispatch(context.Background(), session.DispatchRequest{
		Workspace: workspace.Context{Host: "devbox"},
	}); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if len(asked) != 1 {
		t.Fatalf("the host should have joined, not been rediscovered: %v", asked)
	}

	discovered.agents = []agent.Agent{{ID: "bbb"}}
	agents, err := f.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Host != "devbox" {
		t.Fatalf("a joined host belongs in the roster: %+v", agents)
	}
}

// TestWithoutDiscoveryAnUnknownHostIsStillAnError: the CLI paths that
// build a fleet of one should not quietly invent machines.
func TestWithoutDiscoveryAnUnknownHostIsStillAnError(t *testing.T) {
	f := New(nil, reachable("", &stub{}))
	_, err := f.Dispatch(context.Background(), session.DispatchRequest{
		Workspace: workspace.Context{Host: "devbox"},
	})
	if err == nil {
		t.Fatal("an unknown host must not resolve when nothing can supply it")
	}
}
