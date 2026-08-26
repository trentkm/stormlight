package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/link"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// pipelineRuntime is a fleet that remembers what was sent into it and
// behaves the way the daemon does on a send: the target goes to work.
type pipelineRuntime struct {
	session.Runtime
	agents []agent.Agent
	sent   []sent
}

type sent struct {
	to, message, from string
}

func (r *pipelineRuntime) ListAgents(context.Context) ([]agent.Agent, error) {
	return r.agents, nil
}

func (r *pipelineRuntime) Send(_ context.Context, id, message, from string) error {
	r.sent = append(r.sent, sent{to: id, message: message, from: from})
	for index := range r.agents {
		if r.agents[index].ID == id {
			r.agents[index].Activity = agent.ActivityWorking
		}
	}
	return nil
}

func (r *pipelineRuntime) Update(_ context.Context, id string, update session.Update) error {
	for index := range r.agents {
		if r.agents[index].ID != id {
			continue
		}
		if update.Activity != "" {
			r.agents[index].Activity = update.Activity
		}
		if update.LastReply != "" {
			r.agents[index].LastReply = update.LastReply
		}
	}
	return nil
}

func (r *pipelineRuntime) Delete(_ context.Context, id string) error {
	kept := r.agents[:0]
	for _, managed := range r.agents {
		if managed.ID != id {
			kept = append(kept, managed)
		}
	}
	r.agents = kept
	return nil
}

func (r *pipelineRuntime) set(id string, activity agent.Activity, reply string) {
	for index := range r.agents {
		if r.agents[index].ID == id {
			r.agents[index].Activity = activity
			r.agents[index].LastReply = reply
		}
	}
}

func idle(id, name string) agent.Agent {
	return agent.Agent{
		ID:       id,
		Name:     name,
		Provider: agent.ProviderClaude,
		Activity: agent.ActivityIdle,
		PaneID:   "session-" + id,
	}
}

func pipeline(t *testing.T, agents ...agent.Agent) (*Service, *pipelineRuntime) {
	t.Helper()
	t.Setenv("STORMLIGHT_LINKS_FILE", filepath.Join(t.TempDir(), "links.json"))
	runtime := &pipelineRuntime{agents: agents}
	service := NewService(runtime, provider.NewRegistry(), workspace.NewRegistry())
	return service, runtime
}

func TestTurnEndFiresAutoLinksAttributedToTheSource(t *testing.T) {
	service, runtime := pipeline(t,
		idle("aaaaaaaa11111111", "implementer"),
		idle("bbbbbbbb22222222", "reviewer"),
	)
	ctx := context.Background()
	// Prefixes resolve to full ids, the way every agent command works.
	added, err := service.AddLink(ctx, "aaaa", "bbbb", "review this, be adversarial", true)
	if err != nil {
		t.Fatal(err)
	}
	if added.From != "aaaaaaaa11111111" || added.To != "bbbbbbbb22222222" {
		t.Fatalf("link stored with prefixes: %+v", added)
	}

	runtime.set("aaaaaaaa11111111", agent.ActivityIdle, "I moved the parser into its own package.")
	if err := service.TurnEnded(ctx, "aaaaaaaa11111111"); err != nil {
		t.Fatal(err)
	}

	if len(runtime.sent) != 1 {
		t.Fatalf("sent = %+v, want one hop", runtime.sent)
	}
	hop := runtime.sent[0]
	if hop.to != "bbbbbbbb22222222" || hop.from != "session-aaaaaaaa11111111" {
		t.Fatalf("hop = %+v", hop)
	}
	want := "review this, be adversarial\n\nFrom implementer:\nI moved the parser into its own package."
	if hop.message != want {
		t.Fatalf("message = %q, want %q", hop.message, want)
	}
	links, _ := service.Links(ctx)
	if links[0].LastFired.IsZero() {
		t.Fatal("last fired not recorded")
	}
}

func TestAChainStopsAtTheHopLimit(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	var agents []agent.Agent
	for _, name := range names {
		agents = append(agents, idle(strings.Repeat(name, 16), name))
	}
	service, runtime := pipeline(t, agents...)
	ctx := context.Background()
	for i := 0; i+1 < len(names); i++ {
		if _, err := service.AddLink(ctx, agents[i].ID, agents[i+1].ID, "next", true); err != nil {
			t.Fatal(err)
		}
	}
	// Each agent finishes in turn; each hop is delivered while the next
	// agent is idle, which is the shape of a real chain.
	for i := 0; i+1 < len(names); i++ {
		runtime.set(agents[i].ID, agent.ActivityIdle, "done "+names[i])
		if err := service.TurnEnded(ctx, agents[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	// a→b (hop 0), b→c (hop 1), c→d (hop 2) fire; d→e would be hop 3.
	if len(runtime.sent) != link.HopLimit {
		t.Fatalf("hops = %d, want %d", len(runtime.sent), link.HopLimit)
	}
	if runtime.sent[len(runtime.sent)-1].to != agents[3].ID {
		t.Fatalf("last hop went to %s, want %s", runtime.sent[2].to, agents[3].ID)
	}

	// A human speaking to d resets the count: d's next turn end fires.
	runtime.sent = nil
	runtime.set(agents[3].ID, agent.ActivityIdle, "done again")
	if err := service.TurnEnded(ctx, agents[3].ID); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 1 || runtime.sent[0].to != agents[4].ID {
		t.Fatalf("after a human turn: sent = %+v", runtime.sent)
	}
}

func TestAHopWaitsForABusyTarget(t *testing.T) {
	service, runtime := pipeline(t, idle("a", "a"), idle("b", "b"))
	ctx := context.Background()
	if _, err := service.AddLink(ctx, "a", "b", "look", true); err != nil {
		t.Fatal(err)
	}
	runtime.set("a", agent.ActivityIdle, "first")
	runtime.set("b", agent.ActivityWorking, "")
	if err := service.TurnEnded(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 0 {
		t.Fatalf("sent into a working agent: %+v", runtime.sent)
	}
	links, _ := service.Links(ctx)
	if links[0].Pending == nil || !strings.Contains(links[0].Pending.Message, "first") {
		t.Fatalf("hop not parked: %+v", links[0])
	}

	// A newer reply replaces the parked one.
	runtime.set("a", agent.ActivityIdle, "second")
	if err := service.TurnEnded(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	links, _ = service.Links(ctx)
	if !strings.Contains(links[0].Pending.Message, "second") {
		t.Fatalf("older hop kept: %+v", links[0].Pending)
	}

	// b's own turn end delivers what it is owed.
	runtime.set("b", agent.ActivityIdle, "b's reply")
	if err := service.TurnEnded(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 1 || runtime.sent[0].to != "b" || !strings.Contains(runtime.sent[0].message, "second") {
		t.Fatalf("pending not delivered: %+v", runtime.sent)
	}
	links, _ = service.Links(ctx)
	if links[0].Pending != nil {
		t.Fatal("pending not cleared after delivery")
	}
}

func TestFiringByHandStartsAChainAtZero(t *testing.T) {
	service, runtime := pipeline(t, idle("a", "a"), idle("b", "b"))
	ctx := context.Background()
	added, err := service.AddLink(ctx, "a", "b", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing said yet: nothing to send, and say so.
	if _, err := service.FireLink(ctx, added.ID); !errors.Is(err, link.ErrNothingToSend) {
		t.Fatalf("fire before a reply: %v", err)
	}
	runtime.set("a", agent.ActivityIdle, "hello b")
	fired, err := service.FireLink(ctx, added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fired.LastFired.IsZero() || len(runtime.sent) != 1 {
		t.Fatalf("fired = %+v, sent = %+v", fired, runtime.sent)
	}
	if runtime.sent[0].message != "From a:\nhello b" {
		t.Fatalf("message = %q", runtime.sent[0].message)
	}
	// An off link never fires on its own.
	runtime.sent = nil
	if err := service.TurnEnded(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 0 {
		t.Fatalf("a manual link fired on its own: %+v", runtime.sent)
	}
}

func TestDeletingAnAgentForgetsItsLinks(t *testing.T) {
	service, _ := pipeline(t, idle("a", "a"), idle("b", "b"), idle("c", "c"))
	ctx := context.Background()
	service.AddLink(ctx, "a", "b", "", true)
	service.AddLink(ctx, "b", "c", "", true)
	if err := service.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	links, _ := service.Links(ctx)
	if len(links) != 0 {
		t.Fatalf("links after deleting b: %+v", links)
	}
}

func TestLinksNeedAgentsThatExist(t *testing.T) {
	service, _ := pipeline(t, idle("a", "a"))
	if _, err := service.AddLink(context.Background(), "a", "nobody", "", true); err == nil {
		t.Fatal("linked to an agent that does not exist")
	}
}
