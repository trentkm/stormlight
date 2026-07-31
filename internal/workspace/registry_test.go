package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type staticResolver struct {
	name    string
	context Context
	matched bool
	err     error
	calls   int
}

func (r *staticResolver) Name() string {
	return r.name
}

func (r *staticResolver) Resolve(context.Context, string) (Context, bool, error) {
	r.calls++
	return r.context, r.matched, r.err
}

func TestRegistryUsesFirstMatchingResolverAndCaches(t *testing.T) {
	root := t.TempDir()
	first := &staticResolver{name: "external", matched: true, context: Context{
		ID:            "custom:workspace",
		Kind:          "custom",
		Name:          "Example",
		Root:          root,
		ExecutionRoot: root,
	}}
	second := &staticResolver{name: "later", matched: true}
	registry := NewRegistryWithResolvers(first, second)

	for range 2 {
		got, err := registry.Resolve(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != "custom:workspace" {
			t.Fatalf("workspace id = %q", got.ID)
		}
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("resolver calls = first:%d second:%d", first.calls, second.calls)
	}
}

func TestRegistryFallsBackToCanonicalDirectory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistryWithResolvers()

	got, err := registry.Resolve(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindDirectory || got.Root != canonical || got.ExecutionRoot != canonical {
		t.Fatalf("unexpected fallback: %#v", got)
	}
}

func TestGitResolverGroupsLinkedWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	mainCheckout := filepath.Join(root, "main")
	worktree := filepath.Join(root, "feature")
	runGit(t, root, "init", mainCheckout)
	runGit(t, mainCheckout, "config", "user.email", "runstead@example.invalid")
	runGit(t, mainCheckout, "config", "user.name", "runstead test")
	if err := os.WriteFile(filepath.Join(mainCheckout, "README"), []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainCheckout, "add", "README")
	runGit(t, mainCheckout, "commit", "-m", "initial")
	runGit(t, mainCheckout, "worktree", "add", "-b", "feature", worktree)

	resolver := gitResolver{}
	mainContext, matched, err := resolver.Resolve(context.Background(), mainCheckout)
	if err != nil || !matched {
		t.Fatalf("resolve main: matched=%v err=%v", matched, err)
	}
	worktreeContext, matched, err := resolver.Resolve(context.Background(), worktree)
	if err != nil || !matched {
		t.Fatalf("resolve worktree: matched=%v err=%v", matched, err)
	}
	if mainContext.ID != worktreeContext.ID {
		t.Fatalf("workspace ids differ: %q != %q", mainContext.ID, worktreeContext.ID)
	}
	if mainContext.ExecutionRoot == worktreeContext.ExecutionRoot {
		t.Fatalf("execution roots are equal: %q", mainContext.ExecutionRoot)
	}
}

func TestExternalResolverProtocol(t *testing.T) {
	root := t.TempDir()
	resolverDirectory := filepath.Join(t.TempDir(), "resolvers")
	if err := os.Mkdir(resolverDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(resolverDirectory, "10-custom")
	body := `#!/bin/sh
if [ "$1" != "resolve" ]; then
  exit 64
fi
printf '{"kind":"custom","name":"external","root":"%s","execution_root":"%s"}\n' "$2" "$2"
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	resolvers, err := loadExternalResolvers(resolverDirectory)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistryWithResolvers(resolvers...)
	got, err := registry.Resolve(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "custom" || got.Name != "external" || got.Root != canonical {
		t.Fatalf("unexpected external context: %#v", got)
	}
}

func TestResolverDirectoryFallsBackToLegacyLocation(t *testing.T) {
	configHome := t.TempDir()
	legacy := filepath.Join(configHome, "agentmux", "resolvers")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("RUNSTEAD_RESOLVERS_DIR", "")
	t.Setenv("AGENTMUX_RESOLVERS_DIR", "")

	if got := resolverDirectory(); got != legacy {
		t.Fatalf("resolver directory = %q, want %q", got, legacy)
	}

	current := filepath.Join(configHome, "runstead", "resolvers")
	if err := os.MkdirAll(current, 0755); err != nil {
		t.Fatal(err)
	}
	if got := resolverDirectory(); got != current {
		t.Fatalf("resolver directory = %q, want %q", got, current)
	}
}

func TestResolverDirectoryPrefersRunsteadEnvironment(t *testing.T) {
	t.Setenv("RUNSTEAD_RESOLVERS_DIR", "/tmp/runstead-resolvers")
	t.Setenv("AGENTMUX_RESOLVERS_DIR", "/tmp/agentmux-resolvers")
	if got := resolverDirectory(); got != "/tmp/runstead-resolvers" {
		t.Fatalf("resolver directory = %q", got)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
