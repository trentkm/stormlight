// Package selfpath resolves the path Stormlight re-invokes itself by.
package selfpath

import (
	"fmt"
	"os"
	"path/filepath"
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
func Resolve() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve Stormlight executable: %w", err)
	}
	if alias, ok := stableAlias(executable); ok {
		return alias, nil
	}
	return executable, nil
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
