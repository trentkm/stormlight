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
	for _, dir := range []string{
		"alpha",
		"beta",
		"beta/deep",
		"beta/deep/nested",
		"services/payments",
		".hidden",
		"node_modules/junk",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPathNavListsRecursivelyWithoutNoise(t *testing.T) {
	nav := newPathNav(pathNavFixture(t))
	matches := nav.matches()
	joined := strings.Join(matches, "|")
	for _, want := range []string{
		rootEntry, "alpha", "beta", "beta/deep", "beta/deep/nested",
		"services/payments",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %#v", want, matches)
		}
	}
	if strings.Contains(joined, "hidden") || strings.Contains(joined, "node_modules") {
		t.Fatalf("noise leaked into %#v", matches)
	}
}

func TestPathNavFiltersAcrossDepthsAndPicks(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(root)

	nav.update(runeKey("nest"))
	matches := nav.matches()
	if len(matches) != 1 || matches[0] != "beta/deep/nested" {
		t.Fatalf("deep filter matches = %#v", matches)
	}
	if nav.chosen() != filepath.Join(root, "beta/deep/nested") {
		t.Fatalf("chosen = %q", nav.chosen())
	}

	// Multiple terms AND together.
	nav.filter.SetValue("")
	nav.update(runeKey("beta deep"))
	matches = nav.matches()
	if len(matches) != 2 {
		t.Fatalf("multi-term matches = %#v", matches)
	}
}

func TestPathNavEmptyEnterChoosesRoot(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(root)
	if nav.chosen() != root {
		t.Fatalf("chosen = %q, want root %q", nav.chosen(), root)
	}
	nav.moveHighlight(1)
	if nav.chosen() != filepath.Join(root, "alpha") {
		t.Fatalf("second entry = %q", nav.chosen())
	}
}

func TestPathNavRerootsUpAndByTypedPath(t *testing.T) {
	root := pathNavFixture(t)
	nav := newPathNav(filepath.Join(root, "beta"))
	nav.up()
	if nav.root != root {
		t.Fatalf("up landed at %q", nav.root)
	}

	other := t.TempDir()
	nav.update(runeKey(other))
	attempted, ok := nav.jump()
	if !attempted || !ok || nav.root != filepath.Clean(other) {
		t.Fatalf("jump attempted=%v ok=%v root=%q", attempted, ok, nav.root)
	}

	nav.update(runeKey("/definitely/not/a/dir"))
	attempted, ok = nav.jump()
	if !attempted || ok {
		t.Fatalf("bogus jump attempted=%v ok=%v", attempted, ok)
	}
}

func TestDispatchPathNavPicksIntoTask(t *testing.T) {
	root := pathNavFixture(t)
	model := NewModel(stubBackend{})
	model.width = 100
	model.height = 30
	model.mode = modeDispatch
	model.chooseDispatchDirectory = true
	model.formFocus = dispatchCustomPath
	model.pathNav = newPathNav(root)

	updated, _ := model.updateDispatch(runeKey("payments"))
	model = updated.(Model)
	updated, _ = model.updateDispatch(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.formFocus != dispatchTask {
		t.Fatalf("enter did not confirm; focus = %v", model.formFocus)
	}
	if got := strings.TrimSpace(model.cwdInput.Value()); got != filepath.Join(root, "services/payments") {
		t.Fatalf("confirmed path = %q", got)
	}
}

func TestPathNavViewListsMatches(t *testing.T) {
	nav := newPathNav(pathNavFixture(t))
	view := ansi.Strip(nav.view(46, 5))
	if !strings.Contains(view, "cd ") ||
		!strings.Contains(view, "alpha") ||
		!strings.Contains(view, rootEntry) {
		t.Fatalf("view missing content:\n%s", view)
	}
}
