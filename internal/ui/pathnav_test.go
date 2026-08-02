package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pathNavFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta", "beta-service", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPathNavTabCompletesThenCycles(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(root)

	// "bet" + Tab extends to the common prefix "beta" without moving.
	nav.update(runeKey("bet"))
	nav.complete(1)
	if nav.filter.Value() != "beta" || nav.base != root {
		t.Fatalf("prefix completion = %q at %q", nav.filter.Value(), nav.base)
	}

	// The prefix is itself a full candidate, so Tab is already cycling:
	// the next press moves to beta-service, then wraps back.
	nav.complete(1)
	if nav.filter.Value() != "beta-service" {
		t.Fatalf("cycle #2 = %q", nav.filter.Value())
	}
	nav.complete(1)
	if nav.filter.Value() != "beta" {
		t.Fatalf("cycle wrap = %q", nav.filter.Value())
	}

	// Enter accepts the cycled candidate: cd beta.
	if action := nav.enter(); action != pathNavHandled {
		t.Fatalf("enter action = %v", action)
	}
	if filepath.Base(nav.base) != "beta" || !nav.filterEmpty() {
		t.Fatalf("enter landed at %q filter %q", nav.base, nav.filter.Value())
	}

	// Enter on the empty line confirms the directory itself.
	if action := nav.enter(); action != pathNavConfirm {
		t.Fatalf("empty enter action = %v", action)
	}

	nav.up()
	if nav.base != root {
		t.Fatalf("up landed at %q, want %q", nav.base, root)
	}
}

func TestPathNavTypedEnterDescendsBestMatch(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(root)
	nav.update(runeKey("alp"))
	if action := nav.enter(); action != pathNavHandled {
		t.Fatalf("action = %v", action)
	}
	if filepath.Base(nav.base) != "alpha" {
		t.Fatalf("base = %q", nav.base)
	}

	nav.update(runeKey("zzz"))
	if action := nav.enter(); action != pathNavInvalid {
		t.Fatalf("bogus enter action = %v", action)
	}
}

func TestPathNavHidesDotDirsUnlessAsked(t *testing.T) {
	nav := newPathNav(pathNavFixture(t))
	for _, entry := range nav.matches() {
		if entry == ".hidden" {
			t.Fatal("hidden directory shown without a dot filter")
		}
	}
	nav.update(runeKey(".h"))
	matches := nav.matches()
	if len(matches) != 1 || matches[0] != ".hidden" {
		t.Fatalf("dot filter matches = %#v", matches)
	}
}

func TestPathNavJumpsToTypedAbsolutePaths(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(t.TempDir())
	nav.update(runeKey(root))
	if action := nav.enter(); action != pathNavHandled || nav.base != filepath.Clean(root) {
		t.Fatalf("jump action=%v base=%q", action, nav.base)
	}

	nav.update(runeKey("/definitely/not/a/dir"))
	if action := nav.enter(); action != pathNavInvalid {
		t.Fatalf("bogus jump action = %v", action)
	}
}

func TestDispatchPathNavConfirmsIntoTask(t *testing.T) {
	root := pathNavFixture(t)
	model := NewModel(stubBackend{})
	model.width = 100
	model.height = 30
	model.mode = modeDispatch
	model.chooseDispatchDirectory = true
	model.formFocus = dispatchCustomPath
	model.pathNav = newPathNav(root)

	updated, _ := model.updateDispatch(runeKey("alp"))
	model = updated.(Model)
	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.pathNav.filter.Value() != "alpha" {
		t.Fatalf("tab completion = %q", model.pathNav.filter.Value())
	}

	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if filepath.Base(model.pathNav.base) != "alpha" {
		t.Fatalf("enter did not descend: %q", model.pathNav.base)
	}

	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("confirm did not reach task; focus = %v", model.formFocus)
	}
	if filepath.Base(strings.TrimSpace(model.cwdInput.Value())) != "alpha" {
		t.Fatalf("confirmed path = %q", model.cwdInput.Value())
	}
}

func TestPathNavViewListsMatches(t *testing.T) {
	nav := newPathNav(pathNavFixture(t))
	view := ansi.Strip(nav.view(40, 4))
	if !strings.Contains(view, "cd ") ||
		!strings.Contains(view, "alpha") ||
		!strings.Contains(view, parentEntry) {
		t.Fatalf("view missing content:\n%s", view)
	}
}

func TestPathNavEmptyFilterTabCyclesFromParent(t *testing.T) {
	nav := newPathNav(pathNavFixture(t))
	nav.complete(1)
	if nav.filter.Value() != parentEntry || nav.cycle == nil {
		t.Fatalf("first tab = %q cycling=%v", nav.filter.Value(), nav.cycle != nil)
	}
	nav.complete(1)
	if nav.filter.Value() != "alpha" {
		t.Fatalf("second tab = %q", nav.filter.Value())
	}
	nav.complete(-1)
	if nav.filter.Value() != parentEntry {
		t.Fatalf("reverse tab = %q", nav.filter.Value())
	}
}
