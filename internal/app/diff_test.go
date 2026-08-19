package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

// git runs git in dir and fails the test on error — repo fixtures only.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func repoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "start")
	return dir
}

func diffService(t *testing.T, agents ...agent.Agent) *Service {
	t.Helper()
	return serviceWithRuntime(t, &recordingRuntime{agents: agents})
}

func TestDiffShowsWorkSinceTheBase(t *testing.T) {
	dir := repoWithCommit(t)
	// The base the branch was cut from, the way a real checkout has one.
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")

	// Committed work after the base: HEAD-only diffing would go blank
	// here, which is exactly when a human wants to read the change.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\ncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "work")
	// Uncommitted work on top.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\ncommitted\nuncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// And a file git does not track yet.
	if err := os.WriteFile(filepath.Join(dir, "fresh.go"), []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	for _, want := range []string{"+committed", "+uncommitted", "fresh.go", "+package fresh"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestDiffFallsBackWithoutABase(t *testing.T) {
	dir := repoWithCommit(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if !strings.Contains(diff, "+edited") {
		t.Errorf("diff missing the uncommitted edit:\n%s", diff)
	}
}

func TestDiffRefusesWhatItCannotAnswer(t *testing.T) {
	service := diffService(t,
		agent.Agent{ID: "remote", Cwd: "/somewhere", Host: "far"},
		agent.Agent{ID: "homeless", Cwd: ""},
		agent.Agent{ID: "plain", Cwd: t.TempDir()},
	)

	for _, id := range []string{"remote", "homeless", "plain", "missing"} {
		if _, ok := service.Diff(context.Background(), id); ok {
			t.Errorf("%s: expected no diff", id)
		}
	}
}
