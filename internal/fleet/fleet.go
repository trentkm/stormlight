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
	"fmt"
	"io"
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
}

// Runtime fans one session.Runtime out over several daemons.
type Runtime struct {
	members []*member

	// owner maps an agent to the member that last answered for it. It is
	// a cache of the last listing, not a record: an agent that has moved
	// or vanished is corrected by the next one.
	mu    sync.RWMutex
	owner map[string]*member
}

func New(members ...Member) *Runtime {
	fleet := &Runtime{owner: make(map[string]*member)}
	for _, m := range members {
		fleet.members = append(fleet.members, &member{host: m.Host, connect: m.Connect})
	}
	return fleet
}

// Status is one member's reachability, for a dashboard that wants to say
// so rather than silently listing fewer agents.
type Status struct {
	Host  string
	Error error
}

func (f *Runtime) Status() []Status {
	statuses := make([]Status, 0, len(f.members))
	for _, m := range f.members {
		m.mu.Lock()
		statuses = append(statuses, Status{Host: m.host, Error: m.failure})
		m.mu.Unlock()
	}
	return statuses
}

// resolve connects the member if it is not connected, honouring the retry
// window so an unreachable host is not dialled on every refresh.
func (m *member) resolve() (session.Runtime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtime != nil {
		return m.runtime, nil
	}
	if m.failure != nil && time.Since(m.failedAt) < m.retryWindow() {
		return nil, m.failure
	}
	runtime, err := m.connect()
	if err != nil {
		m.failure = err
		m.failedAt = time.Now()
		return nil, err
	}
	m.runtime = runtime
	m.failure = nil
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

func (f *Runtime) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	type result struct {
		member *member
		agents []agent.Agent
		err    error
	}
	results := make([]result, len(f.members))
	var wait sync.WaitGroup
	for index, m := range f.members {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = result{member: m}
			runtime, err := m.resolve()
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
	wait.Wait()

	owner := make(map[string]*member)
	var agents []agent.Agent
	var failures []error
	for _, item := range results {
		if item.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", hostName(item.member.host), item.err))
			diagnostic.Logger().Warn("host unavailable",
				"host", hostName(item.member.host),
				"error", item.err,
			)
			continue
		}
		for _, listed := range item.agents {
			listed.Host = item.member.host
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
	if len(failures) == len(f.members) && len(failures) > 0 {
		return nil, failures[0]
	}
	return agents, nil
}

// memberFor finds the daemon that owns an agent. A miss is not an error:
// an agent dispatched a moment ago has not been listed yet, so the roster
// is refreshed once before giving up.
func (f *Runtime) memberFor(ctx context.Context, id string) (session.Runtime, error) {
	if len(f.members) == 1 {
		return f.members[0].resolve()
	}
	if runtime, ok := f.lookup(id); ok {
		return runtime, nil
	}
	if _, err := f.ListAgents(ctx); err != nil {
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
func (f *Runtime) hostFor(host string) (session.Runtime, error) {
	for _, m := range f.members {
		if m.host == host {
			return m.resolve()
		}
	}
	return nil, fmt.Errorf("no host named %q", hostName(host))
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

func (f *Runtime) Send(ctx context.Context, id, message string) error {
	runtime, err := f.memberFor(ctx, id)
	if err != nil {
		return err
	}
	return runtime.Send(ctx, id, message)
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
