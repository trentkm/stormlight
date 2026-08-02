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

func TestPathNavFiltersAndDescends(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(root)

	matches := nav.matches()
	if len(matches) != 4 || matches[0] != parentEntry {
		t.Fatalf("initial matches = %#v", matches)
	}

	nav.update(runeKey("bet"))
	matches = nav.matches()
	if len(matches) != 2 || matches[0] != "beta" || matches[1] != "beta-service" {
		t.Fatalf("filtered matches = %#v", matches)
	}

	if !nav.descend() {
		t.Fatal("descend refused with matches present")
	}
	if filepath.Base(nav.base) != "beta" || nav.filter.Value() != "" {
		t.Fatalf("descend landed at %q filter %q", nav.base, nav.filter.Value())
	}

	nav.up()
	if nav.base != root {
		t.Fatalf("up landed at %q, want %q", nav.base, root)
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
	attempted, ok := nav.jump()
	if !attempted || !ok || nav.base != filepath.Clean(root) {
		t.Fatalf("jump attempted=%v ok=%v base=%q", attempted, ok, nav.base)
	}

	nav.update(runeKey("/definitely/not/a/dir"))
	attempted, ok = nav.jump()
	if !attempted || ok {
		t.Fatalf("bogus jump attempted=%v ok=%v", attempted, ok)
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
	if filepath.Base(model.pathNav.base) != "alpha" {
		t.Fatalf("tab did not descend: %q", model.pathNav.base)
	}

	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("enter did not confirm; focus = %v", model.formFocus)
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
