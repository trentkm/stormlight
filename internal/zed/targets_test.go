package zed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func testRepository(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, directory, "init", "-q", "-b", "main")
	if err := os.WriteFile(
		filepath.Join(directory, "README.md"),
		[]byte("before\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	testGit(t, directory, "add", ".")
	testGit(t, directory, "commit", "-q", "-m", "initial")
}

func TestDiffTargetsFindsWorkingChangesInsideARepository(t *testing.T) {
	directory := t.TempDir()
	testRepository(t, directory)
	path := filepath.Join(directory, "README.md")
	if err := os.WriteFile(path, []byte("before\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, err := DiffTargets(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets = %#v, want %q", targets, want)
	}
}

func TestDiffTargetsKeepsCommittedBranchWorkVisible(t *testing.T) {
	directory := t.TempDir()
	testRepository(t, directory)
	testGit(t, directory, "update-ref", "refs/remotes/origin/main", "HEAD")
	testGit(t, directory, "checkout", "-q", "-b", "feature")
	path := filepath.Join(directory, "README.md")
	if err := os.WriteFile(path, []byte("before\ncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, directory, "add", ".")
	testGit(t, directory, "commit", "-q", "-m", "feature work")

	targets, err := DiffTargets(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets = %#v, want %q", targets, want)
	}
}

func TestDiffTargetsFindsChangedPackagesAtABrazilWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	first := filepath.Join(workspace, "src", "PackageA")
	second := filepath.Join(workspace, "src", "PackageB")
	testRepository(t, first)
	testRepository(t, second)
	changed := filepath.Join(second, "README.md")
	if err := os.WriteFile(changed, []byte("before\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, err := DiffTargets(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets = %#v, want only %q", targets, want)
	}
}

func TestDiffTargetsReportsAnEmptyRepositoryPlainly(t *testing.T) {
	directory := t.TempDir()
	testRepository(t, directory)
	_, err := DiffTargets(context.Background(), directory)
	if err == nil || !strings.Contains(err.Error(), "no Git changes") {
		t.Fatalf("error = %v", err)
	}
}
