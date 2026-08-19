package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The diff the pane shows is "what has this agent changed": the merge
// base with origin/main diffed against the working tree, which captures
// both committed and uncommitted work since the branch was cut. An
// agent's task lives on a branch (this repo's own convention, and the
// common one), so HEAD alone would go blank the moment the agent
// commits — precisely when a human wants to read the change.
//
// Untracked files are diffed against /dev/null and appended: a new file
// is the change an agent most often makes, and "git diff" alone
// pretends it does not exist.
//
// Every bound here is enforced *during* the work rather than after it.
// This runs on a poll, over a directory whose contents the agent — not
// the operator — decides, so a vendored tree, a 100MB artifact, or a
// scaffold of two thousand files has to cost a bounded read and a
// killed process, never a buffered gigabyte or a spawn storm that
// outlives the request.

const (
	// diffLimit caps the payload. A generated lockfile or vendored tree
	// can make a working diff enormous; past this, the reader wants a
	// terminal and git, not a browser tab.
	diffLimit = 2 << 20
	// untrackedLimit caps how many untracked files are diffed. Each
	// costs a git spawn, and a scaffold laid down before .gitignore
	// catches up holds thousands; the rest are counted, not read.
	untrackedLimit = 100
)

// Diff returns an agent's workspace changes as unified diff text. ok is
// false when the agent is remote (running git across the tunnel is
// #179-class follow-up work), its cwd is not a git repository, or git
// itself fails.
func (s *Service) Diff(ctx context.Context, id string) (string, bool) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return "", false
	}
	for _, managedAgent := range agents {
		if managedAgent.ID != id {
			continue
		}
		if managedAgent.Host != "" || managedAgent.Cwd == "" {
			return "", false
		}
		return workspaceDiff(ctx, managedAgent.Cwd)
	}
	return "", false
}

func workspaceDiff(ctx context.Context, dir string) (string, bool) {
	if !gitSucceeds(ctx, dir, "rev-parse", "--is-inside-work-tree") {
		return "", false
	}
	var out bytes.Buffer

	// Committed and uncommitted work since the branch was cut, when
	// there is a base to cut from; everything uncommitted otherwise.
	// The merge base is resolved by hand rather than spelled
	// "origin/main...": the three-dot form diffs merge-base against
	// HEAD — commits only — and would go blank on exactly the
	// uncommitted work a human most wants to see. Diffing against a
	// bare commit reaches the working tree. (Verified empirically; the
	// two forms genuinely differ.)
	base := []string{"diff", "HEAD"}
	if mergeBase, err := gitOutput(ctx, dir, "merge-base", "origin/main", "HEAD"); err == nil {
		base = []string{"diff", strings.TrimSpace(string(mergeBase))}
	} else if !gitSucceeds(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD") {
		// A repository with no commits yet: everything staged.
		base = []string{"diff", "--cached"}
	}
	tracked, truncated, err := gitLimited(ctx, dir, diffLimit, base...)
	if err != nil {
		return "", false
	}
	out.Write(tracked)
	if truncated {
		return out.String() + truncationNotice, true
	}

	appendUntracked(ctx, dir, &out)
	if out.Len() >= diffLimit {
		return out.String() + truncationNotice, true
	}
	return out.String(), true
}

const truncationNotice = "\n… diff truncated; read the rest with git\n"

// appendUntracked diffs each untracked regular file against nothing,
// under every bound at once: the byte cap, the file-count cap, and the
// request's own deadline. What gets elided is counted out loud — a
// truncation that reads as "covered everything" is worse than the
// truncation.
func appendUntracked(ctx context.Context, dir string, out *bytes.Buffer) {
	// -z, because filenames are data. Without it git quotes any path
	// with a non-ASCII, quoted, or escaped character per core.quotePath,
	// the quoted literal matches no file on disk, git answers exit 1 —
	// which the "they differ" path reads as an answer — and the agent's
	// new file silently vanishes from the diff.
	listing, err := gitOutput(ctx, dir,
		"ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return
	}
	paths := strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00")
	shown, skipped := 0, 0
	for index, path := range paths {
		if path == "" {
			continue
		}
		// ctx.Err is an early exit rather than a bound: a dead context
		// makes each remaining git fail immediately anyway, so this
		// saves spawns rather than preventing an overrun.
		if ctx.Err() != nil || out.Len() >= diffLimit || shown >= untrackedLimit {
			skipped += len(paths) - index
			break
		}
		// Regular files only. An untracked FIFO blocks git until the
		// deadline kills it — ten seconds per poll, forever — and a
		// directory (a nested repository) errors; neither is a diff.
		info, err := os.Lstat(filepath.Join(dir, path))
		if err != nil || !info.Mode().IsRegular() {
			skipped++
			continue
		}
		// --no-index exits 1 when the files differ, which for /dev/null
		// against content is always; only codes past 1 mean git failed.
		body, truncated, err := gitLimitedAllowingOne(ctx, dir, diffLimit-out.Len(),
			"diff", "--no-index", "--", "/dev/null", path)
		if err != nil {
			skipped++
			continue
		}
		out.Write(body)
		shown++
		if truncated {
			// This file filled the cap; the ones behind it are elided
			// as surely as if the count had run out.
			skipped += len(paths) - index - 1
			break
		}
	}
	if skipped > 0 {
		fmt.Fprintf(out, "\n… +%d untracked files not shown\n", skipped)
	}
}

func gitSucceeds(ctx context.Context, dir string, args ...string) bool {
	_, err := gitOutput(ctx, dir, args...)
	return err == nil
}

// gitOutput runs git for answers whose size git itself bounds —
// revisions, listings. Diff bodies go through gitLimited, which does
// not trust their size.
func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append(gitArgs(dir), args...)...)
	return command.Output()
}

// gitArgs is the invocation every call shares: the directory, and
// quotePath off so a name outside ASCII arrives as the name the agent
// gave it rather than as octal escapes nobody can read or search for.
func gitArgs(dir string) []string {
	return []string{"-C", dir, "-c", "core.quotePath=false"}
}

// gitLimited streams git's output up to limit bytes and kills the
// process the moment it has enough — so the cap protects this process's
// memory, not merely the wire. truncated reports whether the answer was
// cut short.
func gitLimited(
	ctx context.Context,
	dir string,
	limit int,
	args ...string,
) (output []byte, truncated bool, err error) {
	return gitStream(ctx, dir, limit, false, args...)
}

// gitLimitedAllowingOne is gitLimited for commands where exit code 1 is
// an answer, not a failure — diff --no-index reports "they differ" as 1.
func gitLimitedAllowingOne(
	ctx context.Context,
	dir string,
	limit int,
	args ...string,
) (output []byte, truncated bool, err error) {
	return gitStream(ctx, dir, limit, true, args...)
}

func gitStream(
	ctx context.Context,
	dir string,
	limit int,
	allowOne bool,
	args ...string,
) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, true, nil
	}
	// A cancel of our own, so reaching the cap kills the git that is
	// still writing a diff nobody will read.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	command := exec.CommandContext(ctx, "git", append(gitArgs(dir), args...)...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := command.Start(); err != nil {
		return nil, false, err
	}
	// One byte past the limit distinguishes "exactly full" from "more
	// was coming".
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		cancel()
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, truncated, readErr
	}
	if truncated {
		// We killed it; its exit status says nothing about the diff.
		return data, true, nil
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if allowOne && errors.As(waitErr, &exit) && exit.ExitCode() == 1 {
			return data, false, nil
		}
		return nil, false, waitErr
	}
	return data, false, nil
}
