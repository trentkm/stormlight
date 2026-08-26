// Package fleet is several daemons behind one runtime.
//
// A dashboard that works two machines is still one dashboard: one roster,
// one selection, one set of keys. Nothing above this package should have
// to ask which daemon an agent is in, so the fan-out lives here, as a
// session.Runtime like any other. Members are the local daemon and one
// per configured host.
//
// Two rules shape everything below. A host that cannot be reached must
// cost nothing but its own absence — never a failed refresh, never a
// frame the dashboard spends waiting. And an agent's host is discovered
// by asking, never remembered: whichever daemon answers for an agent owns
// it, so ownership is rebuilt from every listing rather than persisted
// anywhere.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// retryAfter is how long a member that failed to connect is left alone.
// The dashboard refreshes on a timer, and an unreachable host would
// otherwise pay for an SSH attempt on every one of them.
const retryAfter = 30 * time.Second

// Member is one daemon in the fleet.
type Member struct {
	// Host is what the dashboard calls this machine; empty is this one.
	Host string
	// Connect builds the runtime. It is called lazily and again after a
	// failure, so a host that comes up later joins without a restart.
	Connect func() (session.Runtime, error)
}

type member struct {
	host    string
	connect func() (session.Runtime, error)

	mu       sync.Mutex
	runtime  session.Runtime
	failure  error
	failedAt time.Time
	// reaching is set while a dial is in flight, and settled is closed
	// when that dial finishes. Together they make one connection attempt
	// serve everyone who wants it: the refresh that started it and walked
	// away, and the dispatch that arrived mid-dial and must wait.
	reaching bool
	settled  chan struct{}
}

// errReaching is a member that has not answered yet — not a failure, and
// not an empty machine. It is the difference between "there is nothing
// there" and "nobody has asked yet", which is the whole reason a
// dashboard can say so.
var errReaching = errors.New("still being reached")

// Discover supplies a member for a host that was not passed to New.
//
// A host is known because something names it — a workspace on it, a
// dispatch aimed at it, an entry in the user's SSH configuration — not
// because it was configured. Configuration says how to reach a host
// differently from the default; it is not the list of them. A nil
// Discover means only the members given to New exist.
type Discover func(host string) Member

// Runtime fans one session.Runtime out over several daemons.
type Runtime struct {
	discover Discover

	// mu guards both the member list and the ownership cache. Members
	// arrive while the dashboard is running, the moment a host is first
	// named.
	mu      sync.RWMutex
	members []*member
	// owner maps an agent to the member that last answered for it. It is
	// a cache of the last listing, not a record: an agent that has moved
	// or vanished is corrected by the next one.
	owner map[string]*member
}

func New(discover Discover, members ...Member) *Runtime {
	fleet := &Runtime{discover: discover, owner: make(map[string]*member)}
	for _, m := range members {
		fleet.members = append(fleet.members, &member{host: m.Host, connect: m.Connect})
	}
	return fleet
}

// roster is the current member list, copied so a caller can range over it
// while another goroutine adds a host.
func (f *Runtime) roster() []*member {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return slices.Clone(f.members)
}

// Status is one member's reachability, for a dashboard that wants to say
// so rather than silently listing fewer agents.
type Status struct {
	Host string
	// Reaching is a connection in flight. It is not success and it is not
	// failure; it is the state every remote host passes through, and the
	// one a dashboard has until now had no way to draw.
	Reaching bool
	Error    error
}

func (f *Runtime) Status() []Status {
	members := f.roster()
	statuses := make([]Status, 0, len(members))
	for _, m := range members {
		m.mu.Lock()
		status := Status{Host: m.host, Reaching: m.reaching}
		if !m.reaching {
			status.Error = m.failure
		}
		m.mu.Unlock()
		statuses = append(statuses, status)
	}
	return statuses
}

// Reaching names the machines being connected to right now, so a caller
// can say "reaching devbox…" instead of "no agents".
func (f *Runtime) Reaching() []string {
	var hosts []string
	for _, status := range f.Status() {
		if status.Reaching && status.Host != "" {
			hosts = append(hosts, status.Host)
		}
	}
	return hosts
}

// resolve connects the member if it is not connected, honouring the retry
// window so an unreachable host is not dialled on every refresh.
//
// This is the blocking form, and it is for the things a human just asked
// for: a dispatch, an attach, a keystroke on its way to an agent. Those
// cannot be served by a machine that is not connected yet, so they wait
// for the dial — including one already in flight, whose answer they take
// rather than opening a second connection beside it.
func (m *member) resolve() (session.Runtime, error) {
	for {
		m.mu.Lock()
		if m.runtime != nil {
			runtime := m.runtime
			m.mu.Unlock()
			return runtime, nil
		}
		if m.reaching {
			settled := m.settled
			m.mu.Unlock()
			<-settled
			continue
		}
		if m.failure != nil && time.Since(m.failedAt) < m.retryWindow() {
			failure := m.failure
			m.mu.Unlock()
			return nil, failure
		}
		m.beginLocked()
		m.mu.Unlock()
		return m.dial()
	}
}

// listing is resolve for a poll rather than for a person.
//
// A refresh runs several times a second and redraws whatever answered;
// nothing about it is worth a machine's connection time. So a member that
// is not connected is dialled in the background and reports errReaching,
// and the roster is drawn from the machines that can answer now. The one
// exception is this machine, whose daemon is a unix socket away and is
// started by the very act of dialling it — backgrounding that would trade
// a millisecond for a whole poll interval of an empty dashboard.
func (m *member) listing() (session.Runtime, error) {
	if m.host == "" {
		return m.resolve()
	}
	m.mu.Lock()
	if m.runtime != nil {
		runtime := m.runtime
		m.mu.Unlock()
		return runtime, nil
	}
	if m.reaching {
		m.mu.Unlock()
		return nil, errReaching
	}
	if m.failure != nil && time.Since(m.failedAt) < m.retryWindow() {
		failure := m.failure
		m.mu.Unlock()
		return nil, failure
	}
	m.beginLocked()
	m.mu.Unlock()
	// Nothing here reads the outcome; the next refresh does.
	go func() { _, _ = m.dial() }()
	return nil, errReaching
}

// beginLocked claims the dial. The caller holds mu.
func (m *member) beginLocked() {
	m.reaching = true
	m.settled = make(chan struct{})
}

// dial connects, records what happened, and releases everyone waiting on
// the attempt.
func (m *member) dial() (session.Runtime, error) {
	runtime, err := m.connect()
	m.mu.Lock()
	if err != nil {
		m.failure = err
		m.failedAt = time.Now()
	} else {
		m.runtime = runtime
		m.failure = nil
	}
	m.reaching = false
	settled := m.settled
	m.settled = nil
	m.mu.Unlock()
	close(settled)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

// retryWindow is how long this member waits before trying again.
//
// The window exists because dialling costs something, and for a remote
// host it costs an SSH handshake — a sleeping laptop must not be dialled
// on every refresh. The local daemon costs a unix connect, and the
// runtime that connects to it will start one if none is listening, so
// there is nothing to protect against and everything to lose: a daemon
// that died would otherwise leave the dashboard dark for half a minute
// after it could have been back.
func (m *member) retryWindow() time.Duration {
	if m.host == "" {
		return 0
	}
	return retryAfter
}

// drop forgets a connection so the next call rebuilds it. A tunnel dies
// with the network it ran over, and every later call through it would
// fail the same way until something notices.
func (m *member) drop(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime = nil
	m.failure = err
	m.failedAt = time.Now()
}

// ListAgents is the poll: it reports what the fleet can answer for now
// and leaves anything still connecting to the next one.
func (f *Runtime) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	return f.listAgents(ctx, false)
}

// listAgents fans the listing out over every member. When wait is set,
// each member is dialled and waited for — which is what a question about
// one named agent needs, because "not on any host" is only true once
// every host has been asked.
func (f *Runtime) listAgents(ctx context.Context, wait bool) ([]agent.Agent, error) {
	type result struct {
		member *member
		agents []agent.Agent
		err    error
	}
	members := f.roster()
	results := make([]result, len(members))
	var listings sync.WaitGroup
	for index, m := range members {
		listings.Add(1)
		go func() {
			defer listings.Done()
			results[index] = result{member: m}
			connect := m.listing
			if wait {
				connect = m.resolve
			}
			runtime, err := connect()
			if err != nil {
				results[index].err = err
				return
			}
			agents, err := runtime.ListAgents(ctx)
			if err != nil {
				m.drop(err)
				results[index].err = err
				return
			}
			results[index].agents = agents
		}()
	}
	listings.Wait()

	owner := make(map[string]*member)
	var agents []agent.Agent
	var failures []error
	for _, item := range results {
		if errors.Is(item.err, errReaching) {
			// Nothing to report and nothing to worry about. The dial is
			// running; whatever is over there joins the next refresh.
			continue
		}
		if item.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", hostName(item.member.host), item.err))
			diagnostic.Logger().Warn("host unavailable",
				"host", hostName(item.member.host),
				"error", item.err,
			)
			continue
		}
		for _, listed := range item.agents {
			// An agent has one identity. Two members reporting the same
			// one are two names for the same daemon — a host that
			// resolves back to this machine, or two aliases for one box —
			// and the roster must show it once. The first member to
			// claim it wins, and this machine is always first.
			if _, taken := owner[listed.ID]; taken {
				diagnostic.Logger().Debug("host is another name for one already listed",
					"host", hostName(item.member.host),
					"agent_id", listed.ID,
				)
				continue
			}
			listed.Host = item.member.host
			// The workspace an agent is in is on the machine the agent
			// is on. Its context was resolved when it was dispatched and
			// records whatever was true then, so stamping it here is
			// what keeps one workspace from becoming two rows: the
			// catalog's copy is qualified with the host, and an agent's
			// stored copy would otherwise not be.
			listed.Workspace = listed.Workspace.OnHost(item.member.host)
			owner[listed.ID] = item.member
			agents = append(agents, listed)
		}
	}

	f.mu.Lock()
	f.owner = owner
	f.mu.Unlock()

	// A host being down is its own absence, not the dashboard's failure —
	// unless every host is, in which case there is nothing to show and
	// saying so beats an empty roster that looks like an idle morning.
	//
	// A host still being reached is neither, so it holds the verdict: a
	// dial in flight may yet produce agents, and calling that a failed
	// refresh would put an error card over a dashboard that is merely
	// loading. The refresh after it decides.
	if len(failures) == len(members) && len(failures) > 0 {
		return nil, failures[0]
	}
	return agents, nil
}

// memberFor finds the daemon that owns an agent. A miss is not an error:
// an agent dispatched a moment ago has not been listed yet, so the roster
// is refreshed once before giving up.
func (f *Runtime) memberFor(ctx context.Context, id string) (session.Runtime, error) {
	if members := f.roster(); len(members) == 1 {
		return members[0].resolve()
	}
	if runtime, ok := f.lookup(id); ok {
		return runtime, nil
	}
	// This one waits. An agent nobody has listed is the ordinary state of
	// a command line, and answering "no such agent" because a machine had
	// not finished connecting would be a lie with a keystroke behind it.
	if _, err := f.listAgents(ctx, true); err != nil {
		return nil, err
	}
	if runtime, ok := f.lookup(id); ok {
		return runtime, nil
	}
	return nil, fmt.Errorf("agent %q not found on any host", id)
}

func (f *Runtime) lookup(id string) (session.Runtime, bool) {
	f.mu.RLock()
	owner, ok := f.owner[id]
	f.mu.RUnlock()
	if !ok {
		// Prefixes are how a human names an agent on the command line,
		// and the dashboard passes them straight through.
		f.mu.RLock()
		for known, candidate := range f.owner {
			if len(id) > 0 && len(known) >= len(id) && known[:len(id)] == id {
				if owner != nil && owner != candidate {
					f.mu.RUnlock()
					return nil, false
				}
				owner = candidate
			}
		}
		f.mu.RUnlock()
		if owner == nil {
			return nil, false
		}
	}
	runtime, err := owner.resolve()
	if err != nil {
		return nil, false
	}
	return runtime, true
}

// hostFor is memberFor by host name rather than by agent: dispatch names
// the machine it means through the workspace it resolved.
//
// A host nobody has mentioned before joins the fleet here. Nothing else
// needs to have heard of it — naming it is what makes it real, and
// whether it answers is between it and ssh.
func (f *Runtime) hostFor(host string) (session.Runtime, error) {
	f.mu.Lock()
	for _, m := range f.members {
		if m.host == host {
			f.mu.Unlock()
			return m.resolve()
		}
	}
	if f.discover == nil {
		f.mu.Unlock()
		return nil, fmt.Errorf("no host named %q", hostName(host))
	}
	discovered := f.discover(host)
	joined := &member{host: host, connect: discovered.Connect}
	f.members = append(f.members, joined)
	f.mu.Unlock()
	return joined.resolve()
}

func hostName(host string) string {
	if host == "" {
		return "this machine"
	}
	return host
}

func (f *Runtime) Dispatch(
	ctx context.Context,
	request session.DispatchRequest,
) (agent.Agent, error) {
	// The workspace has already been resolved on the machine that owns
	// it, so it is the request's own statement of where it belongs.
	runtime, err := f.hostFor(request.Workspace.Host)
	if err != nil {
		return agent.Agent{}, err
	}
	dispatched, err := runtime.Dispatch(ctx, request)
	if err != nil {
		return agent.Agent{}, err
	}
	dispatched.Host = request.Workspace.Host
	return dispatched, nil
}

func (f *Runtime) Capture(ctx context.Context, id string, lines int) (string, error) {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return "", err
	}
	return runtime.Capture(ctx, id, lines)
}

func (f *Runtime) Attach(ctx context.Context, id string) (session.AttachResult, error) {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return session.AttachResult{}, err
	}
	return runtime.Attach(ctx, id)
}

func (f *Runtime) Send(ctx context.Context, id, message, from string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Send(ctx, id, message, from)
}

func (f *Runtime) SendCommand(ctx context.Context, id, command string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	sender, ok := runtime.(session.CommandSender)
	if !ok {
		return fmt.Errorf("runtime cannot send provider commands")
	}
	return sender.SendCommand(ctx, id, command)
}

func (f *Runtime) Interrupt(ctx context.Context, id string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Interrupt(ctx, id)
}

func (f *Runtime) Delete(ctx context.Context, id string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Delete(ctx, id)
}

func (f *Runtime) Rename(ctx context.Context, id, name string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Rename(ctx, id, name)
}

func (f *Runtime) Update(ctx context.Context, id string, update session.Update) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Update(ctx, id, update)
}

func (f *Runtime) SetWorkspace(ctx context.Context, id string, value workspace.Context) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.SetWorkspace(ctx, id, value)
}

// AttachTerminal forwards the streaming capability. A fleet declares it
// unconditionally — Go decides capability by type, not by membership — so
// a member that cannot stream says so here rather than at the seam.
func (f *Runtime) AttachTerminal(
	ctx context.Context,
	id string,
	cols, rows int,
) (session.TerminalStream, error) {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return nil, err
	}
	streamer, ok := runtime.(session.TerminalStreamer)
	if !ok {
		return nil, fmt.Errorf("runtime does not stream terminals")
	}
	return streamer.AttachTerminal(ctx, id, cols, rows)
}

// ReadAgentFile forwards the file-reading capability to the daemon that
// holds the agent, which is the one standing on the filesystem the path
// belongs to.
func (f *Runtime) ReadAgentFile(
	ctx context.Context,
	id, path string,
) (io.ReadCloser, error) {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return nil, err
	}
	reader, ok := runtime.(session.FileReader)
	if !ok {
		return nil, fmt.Errorf("runtime cannot read files")
	}
	return reader.ReadAgentFile(ctx, id, path)
}

// ReadHistory collects every machine's conversation log. A host that
// cannot answer costs its own history and not the browser: the
// conversations that can be listed still are.
func (f *Runtime) ReadHistory(ctx context.Context) (map[string][]byte, error) {
	logs := map[string][]byte{}
	for _, m := range f.roster() {
		runtime, err := m.resolve()
		if err != nil {
			continue
		}
		reader, ok := runtime.(session.HistoryReader)
		if !ok {
			continue
		}
		hostLogs, err := reader.ReadHistory(ctx)
		if err != nil {
			diagnostic.Logger().Warn("history unavailable",
				"host", hostName(m.host),
				"error", err,
			)
			continue
		}
		for host, log := range hostLogs {
			logs[host] = log
		}
	}
	return logs, nil
}

// StartOverlay runs the picker or the editor on the machine whose
// filesystem it is about to browse.
func (f *Runtime) StartOverlay(
	ctx context.Context,
	request session.OverlayRequest,
) (session.Overlay, error) {
	runtime, err := f.hostFor(request.Host)
	if err != nil {
		return nil, err
	}
	host, ok := runtime.(session.OverlayHost)
	if !ok {
		return nil, fmt.Errorf("runtime cannot host overlays")
	}
	return host.StartOverlay(ctx, request)
}
