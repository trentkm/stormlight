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
