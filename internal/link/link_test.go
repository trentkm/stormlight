package link

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddRefusesSelfAndCycles(t *testing.T) {
	var state State
	if _, err := state.Add("a", "a", "", true); !errors.Is(err, ErrSelf) {
		t.Fatalf("self-link: got %v, want ErrSelf", err)
	}
	if _, err := state.Add("a", "b", "review", true); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Add("b", "c", "test", true); err != nil {
		t.Fatal(err)
	}
	// c → a would close a → b → c → a.
	if _, err := state.Add("c", "a", "", true); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle: got %v, want ErrCycle", err)
	}
	// The direct loop too.
	if _, err := state.Add("b", "a", "", true); !errors.Is(err, ErrCycle) {
		t.Fatalf("two-node cycle: got %v, want ErrCycle", err)
	}
	// A fan-out from a is fine: two arrows leaving, no way back.
	if _, err := state.Add("a", "c", "", false); err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	if len(state.Links) != 3 {
		t.Fatalf("links = %d, want 3", len(state.Links))
	}
}

func TestComposeIsLabelThenAttributedReply(t *testing.T) {
	got := Compose("  review this diff  ", "implementer", "\nI moved the parser.\n")
	want := "review this diff\n\nFrom implementer:\nI moved the parser."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// No label: just the attributed reply.
	if got := Compose("", "a", "hi"); got != "From a:\nhi" {
		t.Fatalf("unlabelled: got %q", got)
	}
	// A reply past the limit is cut, and says so.
	long := strings.Repeat("x", ReplyLimit+10)
	got = Compose("", "a", long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > ReplyLimit+20 {
		t.Fatalf("long reply not bounded: %d runes", len([]rune(got)))
	}
}

func TestHopsCountFromTheLastHumanMessage(t *testing.T) {
	var state State
	link, _ := state.Add("a", "b", "", true)
	// A turn nobody delivered into starts at hop zero.
	if hop := state.TakeInbound("a"); hop != 0 {
		t.Fatalf("human turn hop = %d, want 0", hop)
	}
	now := time.Now()
	state.Delivered(&state.Links[0], 0, now)
	if state.Links[0].LastFired != now || state.Links[0].Pending != nil {
		t.Fatalf("delivered link not marked: %+v", state.Links[0])
	}
	if state.Inbound["b"].Hop != 1 || state.Inbound["b"].Link != link.ID {
		t.Fatalf("inbound = %+v", state.Inbound["b"])
	}
	// Consumed once.
	if hop := state.TakeInbound("b"); hop != 1 {
		t.Fatalf("hop = %d, want 1", hop)
	}
	if hop := state.TakeInbound("b"); hop != 0 {
		t.Fatalf("hop after take = %d, want 0", hop)
	}
}

func TestRemoveAgentDropsItsLinksAndDebts(t *testing.T) {
	var state State
	state.Add("a", "b", "", true)
	state.Add("b", "c", "", true)
	state.Add("c", "d", "", true)
	state.Delivered(&state.Links[1], 0, time.Now())
	state.RemoveAgent("c")
	if len(state.Links) != 1 || state.Links[0].From != "a" {
		t.Fatalf("links after removing c: %+v", state.Links)
	}
	if _, owed := state.Inbound["c"]; owed {
		t.Fatal("c still owed a turn")
	}
}

func TestUpdateChangesLabelAndAutoOnly(t *testing.T) {
	var state State
	added, _ := state.Add("a", "b", "old", false)
	label := "  new  "
	auto := true
	updated, err := state.Update(added.ID, Patch{Label: &label, Auto: &auto})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Label != "new" || !updated.Auto || updated.From != "a" {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := state.Update("nope", Patch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := state.Remove(added.ID); err != nil || len(state.Links) != 0 {
		t.Fatalf("remove: %v, %d left", err, len(state.Links))
	}
}

func TestStoreRoundTripsUnderTheLock(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "links.json"))
	state, err := store.Load()
	if err != nil || len(state.Links) != 0 {
		t.Fatalf("empty store: %v, %+v", err, state)
	}
	err = store.Mutate(func(state *State) error {
		_, err := state.Add("a", "b", "review", true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A change that fails writes nothing.
	err = store.Mutate(func(state *State) error {
		state.Links = nil
		return errors.New("no")
	})
	if err == nil {
		t.Fatal("expected the change's error")
	}
	state, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Links) != 1 || state.Links[0].Label != "review" {
		t.Fatalf("state after failed change: %+v", state)
	}
}
