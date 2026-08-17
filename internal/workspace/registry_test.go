package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type staticResolver struct {
	name         string
	context      Context
	matched      bool
	err          error
	calls        int
	roots        []Context
	rootsMatched bool
	rootsErr     error
	rootsCalls   int
	waitForRoots bool
}

func (r *staticResolver) Name() string {
	return r.name
}

func (r *staticResolver) Resolve(context.Context, string) (Context, bool, error) {
	r.calls++
	return r.context, r.matched, r.err
}

func (r *staticResolver) ExecutionRoots(
	ctx context.Context,
	_ Context,
) ([]Context, bool, error) {
	r.rootsCalls++
	if r.waitForRoots {
		<-ctx.Done()
		return nil, true, ctx.Err()
	}
	return r.roots, r.rootsMatched, r.rootsErr
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

func TestRegistryUsesOnlyClaimingResolverForExecutionRoots(t *testing.T) {
	root := canonicalTempDir(t)
	feature := canonicalTempDir(t)
	value := Context{
		ID:            "custom:workspace",
		Kind:          "custom",
		Name:          "Example",
		Root:          root,
		ExecutionRoot: root,
	}
	linked := value
	linked.ExecutionRoot = feature
	first := &staticResolver{
		name:         "claiming",
		context:      value,
		matched:      true,
		roots:        []Context{value, linked},
		rootsMatched: true,
	}
	second := &staticResolver{
		name:         "unrelated",
		context:      value,
		matched:      true,
		roots:        []Context{value},
		rootsMatched: true,
	}
	registry := NewRegistryWithResolvers(first, second)

	resolved, err := registry.Resolve(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := registry.ExecutionRoots(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %#v", roots)
	}
	if first.rootsCalls != 1 || second.rootsCalls != 0 {
		t.Fatalf("root calls = first:%d second:%d",
			first.rootsCalls, second.rootsCalls)
	}
}

func TestRegistryCachesExecutionRootFallbacks(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		matched bool
		err     error
		waits   bool
		maximum time.Duration
	}{
		{name: "unsupported", matched: false},
		{name: "failure", matched: true, err: errors.New("broken")},
		{
			name:    "timeout",
			matched: true,
			waits:   true,
			maximum: 1500 * time.Millisecond,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			value := Context{
				ID:            "custom:" + root,
				Kind:          "custom",
				Name:          "custom",
				Root:          root,
				ExecutionRoot: root,
			}
			resolver := &staticResolver{
				name:         "custom",
				context:      value,
				matched:      true,
				rootsMatched: testCase.matched,
				rootsErr:     testCase.err,
				waitForRoots: testCase.waits,
			}
			registry := NewRegistryWithResolvers(resolver)
			resolved, err := registry.Resolve(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}

			started := time.Now()
			for range 2 {
				roots, err := registry.ExecutionRoots(context.Background(), resolved)
				if err != nil {
					t.Fatal(err)
				}
				if len(roots) != 1 || roots[0].ExecutionRoot != root {
					t.Fatalf("roots = %#v", roots)
				}
			}
			if resolver.rootsCalls != 1 {
				t.Fatalf("root calls = %d, want 1", resolver.rootsCalls)
			}
			if testCase.maximum > 0 && time.Since(started) > testCase.maximum {
				t.Fatalf("fallback took %s", time.Since(started))
			}
		})
	}
}

func TestRegistryIgnoresRootsFromAnotherWorkspace(t *testing.T) {
	root := canonicalTempDir(t)
	other := canonicalTempDir(t)
	value := Context{
		ID:            "custom:expected",
		Kind:          "custom",
		Name:          "expected",
		Root:          root,
		ExecutionRoot: root,
	}
	wrong := value
	wrong.ID = "custom:other"
	wrong.Root = other
	wrong.ExecutionRoot = other
	resolver := &staticResolver{
		name:         "custom",
		context:      value,
		matched:      true,
		roots:        []Context{wrong},
		rootsMatched: true,
	}
	registry := NewRegistryWithResolvers(resolver)
	resolved, err := registry.Resolve(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := registry.ExecutionRoots(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].ID != value.ID {
		t.Fatalf("roots = %#v", roots)
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
	root := canonicalTempDir(t)
	mainCheckout := filepath.Join(root, "main")
	worktree := filepath.Join(root, "feature")
	runGit(t, root, "init", mainCheckout)
	runGit(t, mainCheckout, "config", "user.email", "stormlight@example.invalid")
	runGit(t, mainCheckout, "config", "user.name", "stormlight test")
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
	if worktreeContext.ComponentName != "" || worktreeContext.ComponentRoot != "" {
		t.Fatalf("git resolver invented a component: %#v", worktreeContext)
	}
	roots, matched, err := resolver.ExecutionRoots(context.Background(), mainContext)
	if err != nil || !matched {
		t.Fatalf("list roots: matched=%v err=%v", matched, err)
	}
	if len(roots) != 2 ||
		roots[0].ExecutionRoot != mainCheckout ||
		roots[1].ExecutionRoot != worktree {
		t.Fatalf("roots = %#v", roots)
	}
}

func TestGitResolverSkipsStaleWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := canonicalTempDir(t)
	mainCheckout := filepath.Join(root, "main")
	liveWorktree := filepath.Join(root, "live")
	staleWorktree := filepath.Join(root, "stale")
	runGit(t, root, "init", mainCheckout)
	runGit(t, mainCheckout, "config", "user.email", "stormlight@example.invalid")
	runGit(t, mainCheckout, "config", "user.name", "stormlight test")
	if err := os.WriteFile(filepath.Join(mainCheckout, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainCheckout, "add", "README")
	runGit(t, mainCheckout, "commit", "-m", "initial")
	runGit(t, mainCheckout, "worktree", "add", "-b", "live", liveWorktree)
	runGit(t, mainCheckout, "worktree", "add", "-b", "stale", staleWorktree)
	if err := os.RemoveAll(staleWorktree); err != nil {
		t.Fatal(err)
	}

	resolver := gitResolver{}
	value, matched, err := resolver.Resolve(context.Background(), mainCheckout)
	if err != nil || !matched {
		t.Fatalf("resolve main: matched=%v err=%v", matched, err)
	}
	roots, matched, err := resolver.ExecutionRoots(context.Background(), value)
	if err != nil || !matched {
		t.Fatalf("list roots: matched=%v err=%v", matched, err)
	}
	if len(roots) != 2 ||
		roots[0].ExecutionRoot != mainCheckout ||
		roots[1].ExecutionRoot != liveWorktree {
		t.Fatalf("roots = %#v", roots)
	}
}

func TestExternalResolverProtocol(t *testing.T) {
	root := canonicalTempDir(t)
	feature := filepath.Join(root, "feature")
	if err := os.Mkdir(feature, 0o755); err != nil {
		t.Fatal(err)
	}
	resolverDirectory := filepath.Join(t.TempDir(), "resolvers")
	if err := os.Mkdir(resolverDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(resolverDirectory, "10-custom")
	body := `#!/bin/sh
case "$1" in
  resolve)
    printf '{"kind":"custom","name":"external","root":"%s","execution_root":"%s"}\n' "$2" "$2"
    ;;
  roots)
    printf '[{"kind":"custom","name":"external","root":"%s","execution_root":"%s"},{"kind":"custom","name":"external","root":"%s","execution_root":"%s/feature","metadata":{"execution_root_label":"feature root"}}]\n' "$2" "$2" "$2" "$2"
    ;;
  *)
    exit 2
    ;;
esac
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
	roots, err := registry.ExecutionRoots(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 ||
		roots[0].ExecutionRoot != canonical ||
		roots[1].ExecutionRoot != feature ||
		roots[1].Metadata["execution_root_label"] != "feature root" {
		t.Fatalf("unexpected external roots: %#v", roots)
	}
}

func TestExternalResolverWithoutRootsFallsBackToResolvedContext(t *testing.T) {
	root := t.TempDir()
	script := writeResolver(t, `#!/bin/sh
if [ "$1" = "resolve" ]; then
  printf '{"kind":"legacy","root":"%s"}\n' "$2"
  exit 0
fi
exit 2
`)
	registry := NewRegistryWithResolvers(externalResolver{
		name: "legacy",
		path: script,
	})
	value, err := registry.Resolve(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := registry.ExecutionRoots(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].ExecutionRoot != value.ExecutionRoot {
		t.Fatalf("roots = %#v", roots)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolverDirectoryUsesStormlightConfigLocation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("STORMLIGHT_RESOLVERS_DIR", "")

	want := filepath.Join(configHome, "stormlight", "resolvers")
	if got := resolverDirectory(); got != want {
		t.Fatalf("resolver directory = %q, want %q", got, want)
	}
}

func TestResolverDirectoryUsesStormlightEnvironment(t *testing.T) {
	t.Setenv("STORMLIGHT_RESOLVERS_DIR", "/tmp/stormlight-resolvers")
	if got := resolverDirectory(); got != "/tmp/stormlight-resolvers" {
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

func writeResolver(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolver")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
