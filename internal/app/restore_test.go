package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/resurrect"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// restoringRuntime is a runtime that can lose its roster, and says so.
type restoringRuntime struct {
	session.Runtime
	agents     []agent.Agent
	rosterLive bool
	restored   []session.RestoreRequest
	deleted    []string
}

func (r *restoringRuntime) ListAgents(context.Context) ([]agent.Agent, error) {
	return r.agents, nil
}

func (r *restoringRuntime) SetWorkspace(
	context.Context,
	string,
	workspace.Context,
) error {
	return nil
}

func (r *restoringRuntime) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	r.agents = nil
	return nil
}

func (r *restoringRuntime) SessionName() string { return "agents" }

func (r *restoringRuntime) RosterLive(context.Context) (bool, error) {
	return r.rosterLive, nil
}

func (r *restoringRuntime) Restore(
	_ context.Context,
	req session.RestoreRequest,
) (agent.Agent, error) {
	r.restored = append(r.restored, req)
	restored := req.Agent
	restored.WindowID = "@restored"
	restored.ProcessLive = true
	return restored, nil
}

// stubClaude puts an executable named `claude` on PATH. Resolving a launch
// looks the binary up, and CI has no coding agents installed — so the test
// supplies one rather than asserting only what runs on a developer's laptop.
func stubClaude(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}

// transcriptFixture writes a Claude-shaped transcript so an entry naming it
// looks as restorable as a real one does.
func transcriptFixture(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serviceFixture(
	t *testing.T,
	runtime session.Runtime,
) (*Service, *resurrect.Store) {
	t.Helper()
	store := resurrect.NewStoreAt(
		filepath.Join(t.TempDir(), "state", "session.json"))
	service := NewServiceWithCatalog(
		runtime,
		provider.NewRegistry(),
		workspace.NewRegistry(),
		workspace.NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json")),
		history.NewLogAt(filepath.Join(t.TempDir(), "sessions.jsonl")),
	).Remembering(store)
	return service, store
}

func liveAgent(t *testing.T, id string) agent.Agent {
	t.Helper()
	return agent.Agent{
		ID:             id,
		Provider:       agent.ProviderClaude,
		Name:           "cl-" + id,
		Task:           "keep going",
		Cwd:            t.TempDir(),
		CreatedAt:      time.Unix(1700000000, 0).UTC(),
		Mode:           agent.ModeAuto,
		Attention:      agent.AttentionQuestion,
		AttentionAt:    time.Unix(1700000100, 0).UTC(),
		TranscriptPath: transcriptFixture(t, "5f2b8c14-0000-4000-8000-000000000001"),
		ProcessLive:    true,
	}
}

func TestListingRecordsTheRoster(t *testing.T) {
	runtime := &restoringRuntime{
		agents:     []agent.Agent{liveAgent(t, "a1")},
		rosterLive: true,
	}
	service, store := serviceFixture(t, runtime)
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 1 || snapshot.Agents[0].ID != "a1" {
		t.Fatalf("the listing was not recorded: %#v", snapshot.Agents)
	}
	if snapshot.Session != "agents" {
		t.Fatalf("session = %q, want the runtime's own name", snapshot.Session)
	}
}

// The whole point of the record. An empty listing from a runtime whose
// session is gone means "everything was lost", and writing that as "there
// are no agents" would destroy the file at the one moment it matters.
func TestAnEmptyListingFromALostSessionLeavesTheRecordAlone(t *testing.T) {
	runtime := &restoringRuntime{
		agents:     []agent.Agent{liveAgent(t, "a1")},
		rosterLive: true,
	}
	service, store := serviceFixture(t, runtime)
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}

	runtime.agents = nil
	runtime.rosterLive = false
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 1 {
		t.Fatalf("the record was erased by a lost session: %#v", snapshot.Agents)
	}
}

// A session that is genuinely there and genuinely empty is the other half:
// the record has to follow it down, or deleting your last agent would leave
// it offering to restore forever.
func TestAnEmptyListingFromALiveSessionEmptiesTheRecord(t *testing.T) {
	runtime := &restoringRuntime{
		agents:     []agent.Agent{liveAgent(t, "a1")},
		rosterLive: true,
	}
	service, store := serviceFixture(t, runtime)
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.agents = nil
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 0 {
		t.Fatalf("a live empty session left agents behind: %#v", snapshot.Agents)
	}
}

func TestDeleteForgetsTheAgent(t *testing.T) {
	runtime := &restoringRuntime{
		agents:     []agent.Agent{liveAgent(t, "a1")},
		rosterLive: true,
	}
	service, store := serviceFixture(t, runtime)
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 0 {
		t.Fatalf("a deleted agent is still remembered: %#v", snapshot.Agents)
	}
}

func TestRestoreCandidatesExplainWhatCannotComeBack(t *testing.T) {
	runtime := &restoringRuntime{rosterLive: true}
	service, store := serviceFixture(t, runtime)

	live := liveAgent(t, "resumable")
	noTranscript := liveAgent(t, "silent")
	noTranscript.TranscriptPath = ""
	otherProvider := liveAgent(t, "codex")
	otherProvider.Provider = agent.ProviderCodex
	goneDirectory := liveAgent(t, "orphan")
	goneDirectory.Cwd = filepath.Join(t.TempDir(), "torn-down-worktree")
	running := liveAgent(t, "running")

	if err := store.Save("agents", []agent.Agent{
		live, noTranscript, otherProvider, goneDirectory, running,
	}); err != nil {
		t.Fatal(err)
	}
	runtime.agents = []agent.Agent{running}

	candidates, err := service.RestoreCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]resurrect.Candidate{}
	for _, candidate := range candidates {
		reasons[candidate.Entry.ID] = candidate
	}
	if len(reasons) != 5 {
		t.Fatalf("candidates dropped rows instead of explaining them: %#v", candidates)
	}
	if !reasons["resumable"].Restorable() {
		t.Errorf("a Claude agent with a transcript was not restorable: %#v",
			reasons["resumable"])
	}
	if reasons["silent"].Reason == "" {
		t.Error("an agent with no transcript was offered as restorable")
	}
	if reasons["codex"].Reason == "" {
		t.Error("a provider that cannot resume was offered as restorable")
	}
	if reasons["orphan"].Reason == "" {
		t.Error("an agent whose directory is gone was offered as restorable")
	}
	if !reasons["running"].Live || reasons["running"].Restorable() {
		t.Errorf("a running agent was offered for restore: %#v", reasons["running"])
	}
}

func TestRestorePreservesIdentityAndCarriesNoPrompt(t *testing.T) {
	stubClaude(t)
	runtime := &restoringRuntime{rosterLive: true}
	service, store := serviceFixture(t, runtime)
	remembered := liveAgent(t, "a1")
	if err := store.Save("agents", []agent.Agent{remembered}); err != nil {
		t.Fatal(err)
	}

	results, err := service.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("restore failed: %#v", results)
	}
	if len(runtime.restored) != 1 {
		t.Fatalf("the runtime was asked to restore %d agents", len(runtime.restored))
	}
	request := runtime.restored[0]
	switch {
	case request.Agent.ID != "a1":
		t.Errorf("id = %q; a restored agent must keep its identity", request.Agent.ID)
	case !request.Agent.CreatedAt.Equal(remembered.CreatedAt):
		t.Errorf("created_at = %v, want %v",
			request.Agent.CreatedAt, remembered.CreatedAt)
	case request.Agent.Attention != agent.AttentionQuestion:
		t.Errorf("attention = %q; a question left unanswered stays unanswered",
			request.Agent.Attention)
	case !request.Agent.AttentionAt.Equal(remembered.AttentionAt):
		t.Errorf("attention_at = %v; the queue's ordering key must survive",
			request.Agent.AttentionAt)
	}

	// The launch must reopen the conversation and stop there. A trailing
	// prompt would make a reboot start twelve turns nobody asked for.
	last := request.Launch.Args[len(request.Launch.Args)-1]
	if last != "--resume=5f2b8c14-0000-4000-8000-000000000001" {
		t.Fatalf("resume launch ends with %q, want a bare --resume", last)
	}
	if request.Agent.Task != "" && last == request.Agent.Task {
		t.Fatal("the resume launch carried the original task as a prompt")
	}
}

func TestRestoreRefusesToNameAnAgentItCannotBringBack(t *testing.T) {
	runtime := &restoringRuntime{rosterLive: true}
	service, store := serviceFixture(t, runtime)
	silent := liveAgent(t, "silent")
	silent.TranscriptPath = ""
	if err := store.Save("agents", []agent.Agent{silent}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restore(context.Background(), "silent"); err == nil {
		t.Fatal("restoring an agent with no transcript reported success")
	}
	if len(runtime.restored) != 0 {
		t.Fatal("the runtime was asked to restore an unrestorable agent")
	}
}

func TestRestoreOfEverythingSkipsWhatCannotComeBack(t *testing.T) {
	stubClaude(t)
	runtime := &restoringRuntime{rosterLive: true}
	service, store := serviceFixture(t, runtime)
	silent := liveAgent(t, "silent")
	silent.TranscriptPath = ""
	if err := store.Save("agents", []agent.Agent{
		liveAgent(t, "a1"), silent,
	}); err != nil {
		t.Fatal(err)
	}
	results, err := service.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Entry.ID != "a1" {
		t.Fatalf("a bulk restore did not skip the unrestorable one: %#v", results)
	}
}

func TestForgetRejectsAnUnknownAgent(t *testing.T) {
	service, store := serviceFixture(t, &restoringRuntime{rosterLive: true})
	if err := store.Save("agents", []agent.Agent{liveAgent(t, "a1")}); err != nil {
		t.Fatal(err)
	}
	if err := service.Forget(context.Background(), "nope"); err == nil {
		t.Fatal("forgetting an unknown agent reported success")
	}
	if err := service.Forget(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 0 {
		t.Fatalf("forget left the agent behind: %#v", snapshot.Agents)
	}
}

// A runtime with no restore capability must not be asked to remember: the
// snapshot would describe a roster nothing can rebuild.
func TestARuntimeThatCannotRestoreIsNotRecorded(t *testing.T) {
	service, store := serviceFixture(t, &recordingRuntime{
		agents: []agent.Agent{{ID: "a1", Cwd: t.TempDir()}},
	})
	if _, err := service.ListAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 0 {
		t.Fatalf("a runtime with no restore path was recorded: %#v", snapshot.Agents)
	}
	candidates, err := service.RestoreCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates from a runtime that cannot restore: %#v", candidates)
	}
}
