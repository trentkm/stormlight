package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// A workspace whose contents the agent decides must cost a bounded
// read: the cap is enforced while git runs, not after it has handed
// over a hundred megabytes.
func TestDiffBoundsAnEnormousFile(t *testing.T) {
	dir := repoWithCommit(t)
	huge := bytes.Repeat([]byte("a line of perfectly ordinary text\n"), 400_000)
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if len(diff) > diffLimit+len(truncationNotice) {
		t.Errorf("diff ran to %d bytes, past the %d cap", len(diff), diffLimit)
	}
	if !strings.Contains(diff, "truncated") {
		t.Errorf("a truncated diff must say so:\n%s", diff[max(0, len(diff)-200):])
	}
}

// Each untracked file costs a git spawn; a scaffold laid down before
// .gitignore catches up must not turn one poll into thousands of them.
func TestDiffBoundsUntrackedFileCount(t *testing.T) {
	dir := repoWithCommit(t)
	for i := 0; i < untrackedLimit+25; i++ {
		name := filepath.Join(dir, fmt.Sprintf("scaffold-%03d.txt", i))
		if err := os.WriteFile(name, []byte("generated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if shown := strings.Count(diff, "diff --git"); shown != untrackedLimit {
		t.Errorf("diffed %d untracked files, want exactly %d", shown, untrackedLimit)
	}
	// The count itself, not merely the sentence: a notice that says the
	// wrong number is a notice nobody can act on.
	if want := fmt.Sprintf("… +%d untracked files not shown", 25); !strings.Contains(diff, want) {
		t.Errorf("missing %q:\n%s", want, diff[max(0, len(diff)-300):])
	}
}

// Filenames are data. Git quotes anything non-ASCII unless asked for
// NUL-separated output, and a quoted literal matches no file — the
// agent's new file would simply vanish from the pane.
func TestDiffSeesAwkwardFilenames(t *testing.T) {
	dir := repoWithCommit(t)
	for _, name := range []string{"héllo.txt", "with space.txt", "--dashes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	for _, name := range []string{"héllo.txt", "with space.txt", "--dashes.txt"} {
		if !strings.Contains(diff, name) {
			t.Errorf("diff missing %q:\n%s", name, diff)
		}
	}
	// A dash-leading filename must be read as a path, never as a flag.
	if strings.Contains(diff, "unknown option") || strings.Contains(diff, "usage:") {
		t.Errorf("a filename was parsed as an option:\n%s", diff)
	}
}

// Untracked entries that are not regular files. git ls-files does not
// surface devices or fifos at all, but it does list a nested repository
// as a directory — and diffing a directory answers exit 1, which the
// "they differ" path would read as an empty answer and drop in silence.
// Anything skipped has to be counted, not vanished.
func TestDiffCountsUntrackedItCannotRead(t *testing.T) {
	dir := repoWithCommit(t)
	nested := filepath.Join(dir, "vendored")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(nested, "inner.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if !strings.Contains(diff, "real.txt") {
		t.Errorf("the unreadable entry cost us the regular file:\n%s", diff)
	}
	if !strings.Contains(diff, "untracked files not shown") {
		t.Errorf("a skipped entry must be counted, not vanished:\n%s", diff)
	}
}

// A repository with no commits at all: everything staged is the change.
func TestDiffOnARepositoryWithNoCommits(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if !strings.Contains(diff, "+initial") {
		t.Errorf("staged work missing from a commitless repository:\n%s", diff)
	}
}

// The cap has to bound this process's memory, not merely the payload.
// Truncating after buffering produces byte-identical output while
// having already allocated the whole diff — which is why an
// output-shape assertion cannot pin this, and why the measurement is
// of allocation.
func TestDiffDoesNotBufferWhatItWillNotShow(t *testing.T) {
	dir := repoWithCommit(t)
	huge := bytes.Repeat([]byte("a line of perfectly ordinary text\n"), 3_000_000)
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	huge = nil
	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, ok := service.Diff(context.Background(), "a1"); !ok {
		t.Fatal("expected a diff")
	}
	runtime.ReadMemStats(&after)

	// That file's diff runs to ~100MB; a bounded read costs the cap
	// plus change. Ten times the cap is slack enough that this measures
	// streaming versus buffering rather than GC timing.
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 10*diffLimit {
		t.Errorf("diffing allocated %dMB for a %dMB answer — the read is not bounded",
			allocated>>20, diffLimit>>20)
	}
}

// A file big enough to fill the cap elides everything behind it just as
// surely as the count running out does, and has to say so.
func TestDiffCountsFilesLostToTheByteCap(t *testing.T) {
	dir := repoWithCommit(t)
	// Named so ls-files lists it first, with the rest behind it.
	huge := bytes.Repeat([]byte("a line of perfectly ordinary text\n"), 100_000)
	if err := os.WriteFile(filepath.Join(dir, "aaa-huge.txt"), huge, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		name := filepath.Join(dir, fmt.Sprintf("zzz-%d.txt", i))
		if err := os.WriteFile(name, []byte("small\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := diffService(t, agent.Agent{ID: "a1", Cwd: dir})
	diff, ok := service.Diff(context.Background(), "a1")
	if !ok {
		t.Fatal("expected a diff")
	}
	if want := "… +3 untracked files not shown"; !strings.Contains(diff, want) {
		t.Errorf("missing %q — files behind the cap vanished silently:\n%s",
			want, diff[max(0, len(diff)-300):])
	}
}
