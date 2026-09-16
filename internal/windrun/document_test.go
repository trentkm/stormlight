package windrun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/trentkm/windrunner"
	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/server"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

// localRuntime is a Runtime on a daemon of this build, on this machine:
// the ordinary case, with none of the bridge in the way.
func localRuntime(t *testing.T) *Runtime {
	t.Helper()
	// Not t.TempDir(): a unix socket path caps near 104 bytes and test
	// names push a per-test directory past it.
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	engine := windrunner.NewEngine()
	t.Cleanup(engine.Close)
	go server.Serve(engine, listener)
	t.Cleanup(func() { listener.Close() })
	c, err := client.Dial(socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &Runtime{client: c}
}

// TestUpdatesFromManyWritersAllLand is the agent document under the load
// it actually gets: hooks stamping state while the agent queues requests
// and dashboards claim them, all through separate connections to one
// daemon. Every update must land — a lost one here is a request an agent
// made that no dashboard will ever see.
func TestUpdatesFromManyWritersAllLand(t *testing.T) {
	runtime := localRuntime(t)
	ctx := context.Background()
	dispatched, err := runtime.Dispatch(ctx, session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Name:     "contended",
		Task:     "hold still",
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 60"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	const writers = 4
	const flipsPerWriter = 10
	var wg sync.WaitGroup
	failures := make(chan error, writers*(flipsPerWriter+2))
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for flip := range flipsPerWriter {
				update := session.Update{Mark: agent.MarkWorking}
				if flip%2 == 1 {
					update = session.Update{ClearMark: true}
				}
				if err := runtime.Update(ctx, dispatched.ID, update); err != nil {
					failures <- fmt.Errorf("writer %d flip %d: %w", writer, flip, err)
				}
				if flip < 2 {
					request := agent.DashboardActionRequest{
						ID:      fmt.Sprintf("writer-%d-request-%d", writer, flip),
						Name:    "open",
						Payload: []byte(`{}`),
					}
					if err := runtime.Update(ctx, dispatched.ID, session.Update{DashboardAction: &request}); err != nil {
						failures <- fmt.Errorf("writer %d request %d: %w", writer, flip, err)
					}
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	managedAgent := findAgent(t, runtime, dispatched.ID)
	if len(managedAgent.DashboardActions) != writers*2 {
		t.Fatalf("%d of %d queued requests survived the other writers: %#v",
			len(managedAgent.DashboardActions), writers*2, managedAgent.DashboardActions)
	}

	// Dashboards contend for the head the same way. Exactly one may have
	// it; the rest are told it is held.
	head := managedAgent.DashboardActions[0].ID
	const dashboards = 4
	outcomes := make(chan error, dashboards)
	for dashboard := range dashboards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes <- runtime.Update(ctx, dispatched.ID, session.Update{
				ClaimDashboardAction: &agent.DashboardActionClaim{
					RequestID: head,
					By:        fmt.Sprintf("dashboard-%d", dashboard),
					At:        managedAgent.CreatedAt.Add(1),
				},
			})
		}()
	}
	wg.Wait()
	close(outcomes)
	won, held := 0, 0
	for err := range outcomes {
		switch {
		case err == nil:
			won++
		case errors.Is(err, agent.ErrDashboardActionHeld):
			held++
		default:
			t.Errorf("a claim failed for another reason: %v", err)
		}
	}
	if won != 1 || held != dashboards-1 {
		t.Fatalf("%d dashboards won the claim and %d were refused; want 1 and %d", won, held, dashboards-1)
	}
	if got := findAgent(t, runtime, dispatched.ID).DashboardActions[0].ClaimedBy; got == "" {
		t.Fatal("the winning claim is not on the document")
	}
}

func findAgent(t *testing.T, runtime *Runtime, id string) agent.Agent {
	t.Helper()
	agents, err := runtime.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	for _, managedAgent := range agents {
		if managedAgent.ID == id {
			return managedAgent
		}
	}
	t.Fatalf("agent %s is not listed", id)
	return agent.Agent{}
}
