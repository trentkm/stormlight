package picker

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveDirectoryPrefersTheChoice: Yazi writes two answers, and they
// disagree whenever someone navigates somewhere and then picks something
// else. What they chose wins.
func TestResolveDirectoryPrefersTheChoice(t *testing.T) {
	chosen := t.TempDir()
	elsewhere := t.TempDir()

	got, err := resolveDirectory([]byte(chosen+"\n"), []byte(elsewhere+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != chosen {
		t.Fatalf("got %q, want the chosen directory %q", got, chosen)
	}
}

// TestResolveDirectoryFallsBackToWhereTheyWere: quitting without choosing
// still says something — the directory they had navigated to.
func TestResolveDirectoryFallsBackToWhereTheyWere(t *testing.T) {
	where := t.TempDir()
	got, err := resolveDirectory(nil, []byte(where+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != where {
		t.Fatalf("got %q, want %q", got, where)
	}
}

// TestAChosenFileMeansItsDirectory: nobody picks a file when they were
// asked for a workspace, so the directory holding it is what they meant.
func TestAChosenFileMeansItsDirectory(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "main.go")
	if err := os.WriteFile(file, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDirectory([]byte(file+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != directory {
		t.Fatalf("got %q, want %q", got, directory)
	}
}

func TestNothingChosenIsNotAnError(t *testing.T) {
	got, err := resolveDirectory(nil, nil)
	if err != nil || got != "" {
		t.Fatalf("got %q, %v — want an empty answer and no error", got, err)
	}
}

// TestHandoffsAreOnlyReadableByTheirOwner: the file carries a path the
// user chose, on a machine that may have other people on it.
func TestHandoffsAreOnlyReadableByTheirOwner(t *testing.T) {
	path, err := createHandoff("choice")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("handoff mode = %o, want 600", mode)
	}
}

func TestChooseRefusesWithoutYazi(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Choose("", t.TempDir()); err == nil {
		t.Fatal("a machine without yazi should say so")
	}
}

// TestYaziIsFoundBesideStormlight: `remote setup` installs both into the
// same directory, and that directory is regularly on no PATH at all — a
// fresh machine's ~/.local/bin is missing from the non-interactive
// environment, and often from the login one too, because the profile
// that would add it checked for the directory before it existed.
func TestYaziIsFoundBesideStormlight(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot locate this test binary")
	}
	beside := filepath.Join(filepath.Dir(self), "yazi")
	if err := os.WriteFile(beside, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	defer os.Remove(beside)

	// Nothing on PATH, so only the neighbour can answer.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	found, err := findYazi()
	if err != nil {
		t.Fatalf("findYazi: %v", err)
	}
	if found != beside {
		t.Fatalf("found %q, want the one beside this binary %q", found, beside)
	}
}
