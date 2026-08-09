package resurrect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func storeFixture(t *testing.T) *Store {
	t.Helper()
	return NewStoreAt(filepath.Join(t.TempDir(), "state", "session.json"))
}

func agentFixture(id string) agent.Agent {
	return agent.Agent{
		ID:             id,
		Provider:       agent.ProviderClaude,
		Name:           "cl-" + id,
		Task:           "do the thing",
		Summary:        "did the thing",
		Cwd:            "/tmp/work",
		CreatedAt:      time.Unix(1700000000, 0).UTC(),
		Activity:       agent.ActivityWorking,
		Attention:      agent.AttentionQuestion,
		AttentionAt:    time.Unix(1700000100, 0).UTC(),
		Mode:           agent.ModeAuto,
		TranscriptPath: "/tmp/transcripts/" + id + ".jsonl",
		PaneID:         "%9",
		ProcessLive:    true,
		Workspace:      workspace.DirectoryContext("/tmp/work"),
	}
}

func TestSaveAndLoadRoundTripsTheFieldsRestoreNeeds(t *testing.T) {
	store := storeFixture(t)
	if err := store.Save("agents", []agent.Agent{agentFixture("a1")}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session != "agents" || len(snapshot.Agents) != 1 {
		t.Fatalf("snapshot did not record the roster: %#v", snapshot)
	}
	entry := snapshot.Agents[0]
	want := agentFixture("a1")
	switch {
	case entry.ID != want.ID:
		t.Errorf("id = %q, want %q", entry.ID, want.ID)
	case entry.TranscriptPath != want.TranscriptPath:
		t.Errorf("transcript = %q, want %q",
			entry.TranscriptPath, want.TranscriptPath)
	case entry.Attention != want.Attention:
		t.Errorf("attention = %q, want %q", entry.Attention, want.Attention)
	case !entry.AttentionAt.Equal(want.AttentionAt):
		t.Errorf("attention_at = %v, want %v", entry.AttentionAt, want.AttentionAt)
	case entry.Mode != want.Mode:
		t.Errorf("mode = %q, want %q", entry.Mode, want.Mode)
	case entry.Workspace.Root != want.Workspace.Root:
		t.Errorf("workspace root = %q, want %q",
			entry.Workspace.Root, want.Workspace.Root)
	}
}

// A restore must never claim to know what a dead process was doing, so
// activity and pane identity deliberately do not survive the write.
func TestSnapshotOmitsLiveProcessState(t *testing.T) {
	store := storeFixture(t)
	if err := store.Save("agents", []agent.Agent{agentFixture("a1")}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"activity", "pane_id", "process_live"} {
		if _, present := raw.Agents[0][forbidden]; present {
			t.Errorf("snapshot recorded %q, which describes a dead process", forbidden)
		}
	}
}

func TestSaveSkipsAnUnchangedRoster(t *testing.T) {
	store := storeFixture(t)
	agents := []agent.Agent{agentFixture("a1")}
	if err := store.Save("agents", agents); err != nil {
		t.Fatal(err)
	}
	// Tamper with the file, then save the same roster. Surviving bytes are
	// proof no write happened — where a timestamp comparison would only
	// prove the two writes landed inside one clock tick. A dashboard polls
	// this path about once a second and the roster changes far less often.
	marker := []byte(`{"version":1,"session":"tampered","agents":[]}`)
	if err := os.WriteFile(store.Path(), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("agents", agents); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(marker) {
		t.Fatal("an unchanged roster was written again")
	}

	changed := []agent.Agent{agentFixture("a1")}
	changed[0].Summary = "something new"
	if err := store.Save("agents", changed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Agents[0].Summary != "something new" {
		t.Fatalf("a changed roster was not written: %#v", snapshot.Agents[0])
	}
}

func TestForgetDropsOnlyTheNamedAgents(t *testing.T) {
	store := storeFixture(t)
	if err := store.Save("agents", []agent.Agent{
		agentFixture("a1"),
		agentFixture("a2"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Forget("a1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 1 || snapshot.Agents[0].ID != "a2" {
		t.Fatalf("forget did not leave a2 alone: %#v", snapshot.Agents)
	}
}

// Forget has to invalidate the fingerprint too, or the next identical
// listing would decide there was nothing to write and leave the agent
// deleted in tmux but remembered on disk.
func TestForgetLetsTheNextIdenticalSaveRewrite(t *testing.T) {
	store := storeFixture(t)
	agents := []agent.Agent{agentFixture("a1"), agentFixture("a2")}
	if err := store.Save("agents", agents); err != nil {
		t.Fatal(err)
	}
	if err := store.Forget("a1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("agents", agents); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Agents) != 2 {
		t.Fatalf("the roster was not written back after a forget: %#v",
			snapshot.Agents)
	}
}

func TestLoadRejectsAnUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"agents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreAt(path).Load(); err == nil {
		t.Fatal("a future snapshot version loaded without complaint")
	}
}

func TestLoadOfAMissingFileIsEmptyRatherThanAnError(t *testing.T) {
	snapshot, err := storeFixture(t).Load()
	if err != nil {
		t.Fatalf("a first run should not fail: %v", err)
	}
	if len(snapshot.Agents) != 0 {
		t.Fatalf("empty store returned agents: %#v", snapshot.Agents)
	}
}

func TestFindAcceptsAShortenedID(t *testing.T) {
	snapshot := Snapshot{Agents: []Entry{{ID: "abcdef01"}, {ID: "abcff002"}}}
	if entry, ok := snapshot.Find("abcd"); !ok || entry.ID != "abcdef01" {
		t.Fatalf("prefix lookup returned %#v (ok=%v)", entry, ok)
	}
	if _, ok := snapshot.Find("abc"); ok {
		t.Fatal("an ambiguous prefix resolved to a single agent")
	}
	if _, ok := snapshot.Find("zz"); ok {
		t.Fatal("an unknown id resolved")
	}
}

func TestRestorableKeepsOnlyWhatCanComeBack(t *testing.T) {
	candidates := []Candidate{
		{Entry: Entry{ID: "ready"}},
		{Entry: Entry{ID: "running"}, Live: true},
		{Entry: Entry{ID: "blocked"}, Reason: "no transcript"},
	}
	restorable := Restorable(candidates)
	if len(restorable) != 1 || restorable[0].Entry.ID != "ready" {
		t.Fatalf("restorable = %#v", restorable)
	}
}
