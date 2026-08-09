// Package selfpath resolves the path Stormlight re-invokes itself by.
package selfpath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Resolve returns a path to the running executable that will still start
// Stormlight after an upgrade.
//
// os.Executable answers with the real file behind the process, which for a
// packaged install is version-stamped — Homebrew's cask resolves
// `bin/stormlight` to `Caskroom/stormlight/0.6.3/stormlight`. Stormlight
// writes that path into tmux window commands and hands it to provider hooks
// as $STORMLIGHT_BIN, and both outlive the process that computed them: the
// next upgrade deletes the binary out from under every agent still running,
// and their hooks start failing with "No such file or directory".
//
// The launcher on PATH is the stable name for the same program, so prefer
// it — but only once it is confirmed to be the same file. A development
// build run out of a worktree must keep its own path even though an
// installed `stormlight` shadows it on PATH, or end-to-end runs would
// silently exercise the installed binary instead of the one under test.
//
// Once the running binary has actually been deleted that preference has
// nothing left to protect, and the identity check cannot run at all — there
// is no file to compare a candidate against. Rather than hand back a path
// that starts nothing, fall back to whatever PATH offers under the same name.
func Resolve() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve Stormlight executable: %w", err)
	}
	return resolve(executable)
}

// resolve is Resolve with the running executable supplied, so the answer for
// a binary that no longer exists can be exercised without deleting the test
// binary out from under the test.
func resolve(executable string) (string, error) {
	if alias, ok := stableAlias(executable); ok {
		return alias, nil
	}
	if _, err := os.Stat(executable); err == nil {
		return executable, nil
	}
	name := filepath.Base(strings.TrimSuffix(executable, deletedSuffix))
	if launcher, ok := launcherOnPath(name); ok {
		return launcher, nil
	}
	return "", fmt.Errorf(
		"the Stormlight binary this process was started from no longer exists (%s) and no %s is on PATH",
		executable, name,
	)
}

// deletedSuffix is what Linux appends when os.Executable reads
// /proc/self/exe for a binary that has since been unlinked. Left in place it
// turns the PATH lookup into a search for a program nobody has installed.
const deletedSuffix = " (deleted)"

// launcherOnPath finds a same-named executable on PATH to stand in for a
// binary that is gone. The name and the executable bit are the whole test:
// os.SameFile cannot decide this one, since the file it would compare
// against is exactly what has been deleted. That is weaker than stableAlias
// and is reached only when the alternative is a path that cannot start
// anything at all.
func launcherOnPath(name string) (string, bool) {
	found, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	// A relative PATH entry resolves against this process's directory, and
	// the answer is written into tmux commands that run from somewhere else.
	absolute, err := filepath.Abs(found)
	if err != nil {
		return "", false
	}
	return absolute, true
}

// stableAlias finds the first entry on PATH naming the same file as
// executable. Every candidate is checked rather than just the first match
// exec.LookPath would return, because the launcher that survives upgrades
// may sit behind an unrelated binary of the same name.
func stableAlias(executable string) (string, bool) {
	self, err := os.Stat(executable)
	if err != nil {
		return "", false
	}
	name := filepath.Base(executable)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if candidate == executable {
			// Already the stable name, or PATH points straight at the
			// versioned directory and there is nothing better on offer.
			return executable, true
		}
		info, err := os.Stat(candidate)
		if err != nil || !os.SameFile(self, info) {
			continue
		}
		return candidate, true
	}
	return "", false
}
