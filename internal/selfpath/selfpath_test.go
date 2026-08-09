package selfpath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeBinary creates a file standing in for an installed Stormlight.
func writeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A cask install runs the version-stamped file behind a stable launcher.
// Writing the versioned path into hooks is what left agents invoking a
// binary the next upgrade deleted (#70).
func TestResolvePrefersStableLauncherOverVersionedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	root := t.TempDir()
	versioned := filepath.Join(root, "Caskroom", "stormlight", "0.6.3", "stormlight")
	writeBinary(t, versioned)

	launcher := filepath.Join(root, "bin", "stormlight")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versioned, launcher); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(launcher))

	got, ok := stableAlias(versioned)
	if !ok || got != launcher {
		t.Fatalf("stableAlias = %q, %v; want %q", got, ok, launcher)
	}
}

// A branch build under test must never be swapped for the installed
// binary, or an end-to-end run silently measures the wrong program.
func TestResolveKeepsBuildShadowedOnPath(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree", "stormlight")
	writeBinary(t, worktree)
	installed := filepath.Join(root, "local", "bin", "stormlight")
	writeBinary(t, installed)
	t.Setenv("PATH", filepath.Dir(installed))

	if got, ok := stableAlias(worktree); ok {
		t.Fatalf("stableAlias = %q, want the worktree build left alone", got)
	}
}

// The launcher can sit behind an unrelated binary of the same name, so
// every PATH entry is checked rather than only the first match.
func TestResolveSkipsShadowingBinaryOfTheSameName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	root := t.TempDir()
	versioned := filepath.Join(root, "versions", "0.6.3", "stormlight")
	writeBinary(t, versioned)
	unrelated := filepath.Join(root, "first", "stormlight")
	writeBinary(t, unrelated)
	launcher := filepath.Join(root, "second", "stormlight")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versioned, launcher); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(unrelated)+string(os.PathListSeparator)+filepath.Dir(launcher))

	got, ok := stableAlias(versioned)
	if !ok || got != launcher {
		t.Fatalf("stableAlias = %q, %v; want %q", got, ok, launcher)
	}
}

// Tearing down a finished worktree deletes the development build a running
// dashboard was started from, and every path it writes into tmux then names
// a file the shell cannot run (#90). The installed launcher is the answer
// once there is no longer a build to protect.
func TestResolveFallsBackToPathWhenTheBinaryIsGone(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree", "stormlight")
	installed := filepath.Join(root, "local", "bin", "stormlight")
	writeBinary(t, installed)
	t.Setenv("PATH", filepath.Dir(installed))

	resolved, err := resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != installed {
		t.Fatalf("resolve = %q, want %q", resolved, installed)
	}
}

// Linux reads os.Executable from /proc/self/exe, which marks an unlinked
// binary rather than reporting the bare path. Carried into the lookup, the
// marker asks PATH for a program named "stormlight (deleted)".
func TestResolveIgnoresTheLinuxDeletedMarker(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "local", "bin", "stormlight")
	writeBinary(t, installed)
	t.Setenv("PATH", filepath.Dir(installed))

	resolved, err := resolve(filepath.Join(root, "worktree", "stormlight (deleted)"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved != installed {
		t.Fatalf("resolve = %q, want %q", resolved, installed)
	}
}

// With the binary gone and nothing on PATH to stand in for it, there is no
// path worth returning — every caller writes the answer somewhere a shell
// will try to run it.
func TestResolveFailsWhenNothingCanStartStormlight(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", filepath.Join(root, "empty"))

	resolved, err := resolve(filepath.Join(root, "worktree", "stormlight"))
	if err == nil {
		t.Fatalf("resolve = %q, want an error", resolved)
	}
}

// A build that still exists keeps its own path even though an installed
// binary of the same name sits on PATH — the fallback is for deletion, not
// for shadowing.
func TestResolveKeepsALiveBuildOverThePathFallback(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree", "stormlight")
	writeBinary(t, worktree)
	installed := filepath.Join(root, "local", "bin", "stormlight")
	writeBinary(t, installed)
	t.Setenv("PATH", filepath.Dir(installed))

	resolved, err := resolve(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != worktree {
		t.Fatalf("resolve = %q, want %q", resolved, worktree)
	}
}

// With nothing on PATH to prefer, the running executable is the answer.
func TestResolveFallsBackToTheRunningExecutable(t *testing.T) {
	t.Setenv("PATH", "")
	resolved, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != executable {
		t.Fatalf("Resolve = %q, want %q", resolved, executable)
	}
}
