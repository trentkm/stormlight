package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

// Store is the dashboard's write access to an agent's action queue: taking
// a request, and retiring it.
type Store interface {
	ClaimDashboardAction(ctx context.Context, agentID, requestID, claimer string) error
	AcknowledgeDashboardAction(ctx context.Context, agentID, requestID string) error
}

// Handler is the desktop-local half of an action: it runs where the
// dashboard and the user's desktop applications live, with the request
// the agent's prepare phase produced.
type Handler func(context.Context, agent.Agent, agent.DashboardActionRequest) error

const (
	// handleTimeout bounds one handler run. An action is an editor
	// opening a diff, not a build; one that takes longer is stuck.
	handleTimeout = 30 * time.Second
	// acknowledgeTimeout bounds retiring the request afterwards.
	acknowledgeTimeout = 10 * time.Second
)

// Dispatcher is a dashboard's side of the action contract, shared by every
// kind of dashboard: watch the roster for queued requests, take one, run
// it, retire it. One request runs at a time so desktop-side effects cannot
// race each other.
//
// Which dashboard runs a request is settled in the agent's document, not
// here. Next only proposes; Run claims the request through the Store
// before doing anything, and a claim that fails because another dashboard
// got there first is not an error — it is the other dashboard's request
// now.
type Dispatcher struct {
	store  Store
	handle Handler
	// claimer names this dashboard in the claims it writes, so a request
	// it is running reads as held by it and not by some other process.
	claimer string
	now     func() time.Time

	mu   sync.Mutex
	busy bool
}

// NewDispatcher builds a dispatcher for one dashboard process.
func NewDispatcher(store Store, handle Handler) *Dispatcher {
	return &Dispatcher{
		store:   store,
		handle:  handle,
		claimer: claimerName(),
		now:     time.Now,
	}
}

// claimerName is host, pid, and a nonce: enough to tell two dashboards
// apart in a document either may read, and to say in a log which one
// took a request.
func claimerName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "dashboard"
	}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(nonce[:]))
}

// Next proposes the request this dashboard should run now: the head of
// the first agent's queue that no live dashboard holds. Queues are
// per-agent and in order, so an agent whose head is held by another
// dashboard waits its turn rather than having its second request run
// first. A proposal makes the dispatcher busy; the caller must follow it
// with Run, which is what clears that.
func (d *Dispatcher) Next(agents []agent.Agent) (agent.Agent, agent.DashboardActionRequest, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy {
		return agent.Agent{}, agent.DashboardActionRequest{}, false
	}
	now := d.now()
	for _, managedAgent := range agents {
		if len(managedAgent.DashboardActions) == 0 {
			continue
		}
		head := managedAgent.DashboardActions[0]
		if head.ID == "" || head.Held(now) {
			continue
		}
		head.Payload = append([]byte(nil), head.Payload...)
		d.busy = true
		return managedAgent, head, true
	}
	return agent.Agent{}, agent.DashboardActionRequest{}, false
}

// Run takes the request, runs the handler, and retires the request
// whether the handler succeeded or failed — a plugin that fails is
// reported, not retried forever. It clears the busy state set by Next on
// every path.
//
// The returned error is what the dashboard should show the user. Losing
// the claim to another dashboard is not one: the request is being run,
// just not here.
func (d *Dispatcher) Run(ctx context.Context, managedAgent agent.Agent, request agent.DashboardActionRequest) error {
	defer func() {
		d.mu.Lock()
		d.busy = false
		d.mu.Unlock()
	}()

	claimCtx, cancelClaim := context.WithTimeout(ctx, acknowledgeTimeout)
	err := d.store.ClaimDashboardAction(claimCtx, managedAgent.ID, request.ID, d.claimer)
	cancelClaim()
	switch {
	case errors.Is(err, agent.ErrDashboardActionHeld), errors.Is(err, agent.ErrDashboardActionGone):
		return nil
	case err != nil:
		return fmt.Errorf("claim dashboard action %q for %s: %w", request.Name, managedAgent.Name, err)
	}

	runCtx, cancelRun := context.WithTimeout(ctx, handleTimeout)
	runErr := d.handle(runCtx, managedAgent, request)
	cancelRun()

	ackCtx, cancelAck := context.WithTimeout(ctx, acknowledgeTimeout)
	ackErr := d.store.AcknowledgeDashboardAction(ackCtx, managedAgent.ID, request.ID)
	cancelAck()

	if runErr != nil {
		runErr = fmt.Errorf("run dashboard action %q for %s: %w", request.Name, managedAgent.Name, runErr)
	}
	if ackErr != nil {
		ackErr = fmt.Errorf("retire dashboard action %q for %s: %w", request.Name, managedAgent.Name, ackErr)
	}
	return errors.Join(runErr, ackErr)
}
