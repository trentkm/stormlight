package pending

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/trentkm/runstead/internal/agent"
)

func TestStorePublishesListsResolvesAndRemovesAction(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.pollInterval = time.Millisecond

	published, err := store.Publish(testAction())
	if err != nil {
		t.Fatal(err)
	}
	if !validActionID(published.ID) || published.CreatedAt != now {
		t.Fatalf("published action = %#v", published)
	}
	info, err := os.Stat(store.requestPath(published.ID))
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("request permissions = %o, want 600", permission)
	}

	actions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ID != published.ID {
		t.Fatalf("listed actions = %#v", actions)
	}
	if err := store.Resolve(
		context.Background(),
		published.ID,
		OptionAllowOnce,
	); err != nil {
		t.Fatal(err)
	}
	resolution, err := store.Wait(context.Background(), published.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.ActionID != published.ID ||
		resolution.OptionID != OptionAllowOnce {
		t.Fatalf("resolution = %#v", resolution)
	}

	actions, err = store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("resolved action remained visible: %#v", actions)
	}
	if err := store.Remove(published.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.requestPath(published.ID)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("request was not removed: %v", err)
	}
}

func TestStoreWaitFallsBackWithoutActiveController(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	published, err := store.Publish(testAction())
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Wait(context.Background(), published.ID)
	if !errors.Is(err, ErrNoController) {
		t.Fatalf("wait error = %v, want ErrNoController", err)
	}

	if err := store.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(store.controllerTTL + time.Second)
	_, err = store.Wait(context.Background(), published.ID)
	if !errors.Is(err, ErrNoController) {
		t.Fatalf("stale-controller error = %v, want ErrNoController", err)
	}
}

func TestStoreRejectsUnknownResolutionOption(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	published, err := store.Publish(testAction())
	if err != nil {
		t.Fatal(err)
	}
	err = store.Resolve(context.Background(), published.ID, "not-an-option")
	if err == nil {
		t.Fatal("expected an unavailable-option error")
	}
}

func TestStoreListCleansExpiredActions(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	published, err := store.Publish(testAction())
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(maxActionAge + time.Second)
	actions, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("expired actions = %#v", actions)
	}
	if _, err := os.Stat(store.requestPath(published.ID)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("expired request was not removed: %v", err)
	}
}

func testAction() Action {
	return Action{
		AgentID:  "agent-1",
		Provider: agent.ProviderClaude,
		Kind:     KindApproval,
		Title:    "Claude requests Bash",
		Options: []Option{
			{ID: OptionAllowOnce, Label: "Allow once"},
			{ID: OptionDeny, Label: "Deny"},
		},
	}
}
