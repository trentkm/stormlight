package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogPersistsCanonicalUniquePaths(t *testing.T) {
	root := t.TempDir()
	catalogPath := filepath.Join(t.TempDir(), "state", "workspaces.json")
	catalog := NewCatalogAt(catalogPath)

	if err := catalog.Add(root); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Add(filepath.Join(root, ".")); err != nil {
		t.Fatal(err)
	}

	paths, err := NewCatalogAt(catalogPath).Paths()
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != canonicalRoot {
		t.Fatalf("paths = %#v", paths)
	}
	info, err := os.Stat(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog permissions = %o", info.Mode().Perm())
	}
}

func TestCatalogRemovesPath(t *testing.T) {
	root := t.TempDir()
	catalog := NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json"))
	if err := catalog.Add(root); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Remove(root); err != nil {
		t.Fatal(err)
	}
	paths, err := catalog.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestSetNamePersistsTrimmedOverrideAndAddsMissingPath(t *testing.T) {
	root := t.TempDir()
	catalogPath := filepath.Join(t.TempDir(), "workspaces.json")
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	// The path is not in the catalog yet; SetName must add it so the name
	// survives for workspaces discovered through their agents.
	if err := NewCatalogAt(catalogPath).SetName(root, "  Stormlight  "); err != nil {
		t.Fatal(err)
	}

	reopened := NewCatalogAt(catalogPath)
	paths, err := reopened.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != canonicalRoot {
		t.Fatalf("paths = %#v", paths)
	}
	names, err := reopened.Names()
	if err != nil {
		t.Fatal(err)
	}
	if names[canonicalRoot] != "Stormlight" {
		t.Fatalf("names = %#v", names)
	}
}

func TestSetNameOverwritesAndEmptyNameClearsOverride(t *testing.T) {
	root := t.TempDir()
	catalog := NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json"))
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := catalog.SetName(root, "first"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetName(root, "second"); err != nil {
		t.Fatal(err)
	}
	names, err := catalog.Names()
	if err != nil {
		t.Fatal(err)
	}
	if names[canonicalRoot] != "second" {
		t.Fatalf("names = %#v", names)
	}

	// Whitespace-only is treated as empty and removes the override without
	// dropping the path itself.
	if err := catalog.SetName(root, "   "); err != nil {
		t.Fatal(err)
	}
	names, err = catalog.Names()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names[canonicalRoot]; ok {
		t.Fatalf("override survived clearing: %#v", names)
	}
	paths, err := catalog.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("clearing a name dropped the path: %#v", paths)
	}
}

func TestRemoveDiscardsTheNameOverride(t *testing.T) {
	root := t.TempDir()
	catalogPath := filepath.Join(t.TempDir(), "workspaces.json")
	catalog := NewCatalogAt(catalogPath)
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := catalog.SetName(root, "Stormlight"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Remove(root); err != nil {
		t.Fatal(err)
	}

	// A stale name would resurface if the same path were added back later.
	if err := catalog.Add(root); err != nil {
		t.Fatal(err)
	}
	names, err := NewCatalogAt(catalogPath).Names()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := names[canonicalRoot]; ok {
		t.Fatalf("removed name came back: %#v", names)
	}
}

func TestNamesReturnsACopyCallersCannotMutate(t *testing.T) {
	root := t.TempDir()
	catalog := NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json"))
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetName(root, "Stormlight"); err != nil {
		t.Fatal(err)
	}

	names, err := catalog.Names()
	if err != nil {
		t.Fatal(err)
	}
	names[canonicalRoot] = "mutated"
	delete(names, canonicalRoot)

	fresh, err := catalog.Names()
	if err != nil {
		t.Fatal(err)
	}
	if fresh[canonicalRoot] != "Stormlight" {
		t.Fatalf("caller mutation leaked into the catalog: %#v", fresh)
	}
}

func TestCatalogRejectsCorruptAndFutureVersionFiles(t *testing.T) {
	for _, testCase := range []struct{ name, content string }{
		{"unparsable json", "{not json"},
		{"future version", `{"version": 99, "paths": []}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspaces.json")
			if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewCatalogAt(path).Paths(); err == nil {
				t.Fatal("corrupt catalog was accepted")
			}
		})
	}
}

func TestMissingCatalogFileReadsAsEmpty(t *testing.T) {
	paths, err := NewCatalogAt(filepath.Join(t.TempDir(), "absent.json")).Paths()
	if err != nil {
		t.Fatalf("missing catalog was an error: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestCatalogPathPrefersExplicitFileThenStateHome(t *testing.T) {
	stateHome := t.TempDir()
	home := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "custom.json")

	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("STORMLIGHT_WORKSPACES_FILE", explicit)
	if got := catalogPath(); got != explicit {
		t.Fatalf("explicit file override = %q", got)
	}

	t.Setenv("STORMLIGHT_WORKSPACES_FILE", "")
	want := filepath.Join(stateHome, "stormlight", "workspaces.json")
	if got := catalogPath(); got != want {
		t.Fatalf("state home path = %q, want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	want = filepath.Join(home, ".local", "state", "stormlight", "workspaces.json")
	if got := catalogPath(); got != want {
		t.Fatalf("home fallback path = %q, want %q", got, want)
	}

	// NewCatalog must agree with catalogPath, or an isolated test run would
	// silently edit the real catalog.
	if got := NewCatalog().path; got != want {
		t.Fatalf("NewCatalog path = %q, want %q", got, want)
	}
}
