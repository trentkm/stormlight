package app

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
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

// diffLimit caps the payload. A generated lockfile or vendored tree can
// make a working diff enormous; past this, the reader wants a terminal
// and git, not a browser tab.
const diffLimit = 2 << 20

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
	if !insideGitRepo(ctx, dir) {
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
	tracked, err := gitOutput(ctx, dir, base...)
	if err != nil {
		return "", false
	}
	out.Write(tracked)

	// Untracked files, each against nothing. --no-index exits 1 when
	// the files differ, which for /dev/null against content is always;
	// only exit codes past 1 mean git failed.
	untracked, err := gitOutput(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err == nil {
		for _, path := range strings.Split(strings.TrimSpace(string(untracked)), "\n") {
			if path == "" || out.Len() > diffLimit {
				continue
			}
			body, err := gitOutputAllowingOne(ctx, dir,
				"diff", "--no-index", "--", "/dev/null", path)
			if err == nil {
				out.Write(body)
			}
		}
	}

	if out.Len() > diffLimit {
		return string(out.Bytes()[:diffLimit]) +
			"\n… diff truncated; read the rest with git\n", true
	}
	return out.String(), true
}

func insideGitRepo(ctx context.Context, dir string) bool {
	return gitSucceeds(ctx, dir, "rev-parse", "--is-inside-work-tree")
}

func gitSucceeds(ctx context.Context, dir string, args ...string) bool {
	_, err := gitOutput(ctx, dir, args...)
	return err == nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	return command.Output()
}

// gitOutputAllowingOne is gitOutput for commands where exit code 1 is
// an answer, not a failure — diff --no-index reports "they differ" as 1.
func gitOutputAllowingOne(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, err := gitOutput(ctx, dir, args...)
	var exit *exec.ExitError
	if err != nil {
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return output, nil
		}
		return nil, err
	}
	return output, nil
}
