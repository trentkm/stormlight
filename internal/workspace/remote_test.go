package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/remote"
)

// fakeHost stands in for a machine reachable over SSH: a script that
// answers `_resolve` the way that host's own Stormlight would.
func fakeHost(t *testing.T, name, script string) remote.Host {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return remote.Host{Name: name, SSHProgram: path}
}

const remoteAnswer = `{"id":"git:/srv/api/.git","kind":"git","name":"api",` +
	`"root":"/srv/api","execution_root":"/srv/api"}`

// TestRemoteResolutionAnswersAboutTheOtherMachine: the resolved paths are
// the far side's, and this process never had to have them.
func TestRemoteResolutionAnswersAboutTheOtherMachine(t *testing.T) {
	host := fakeHost(t, "devbox", `printf '%s\n' '`+remoteAnswer+`'`)
	registry := NewRegistryForHost(host)

	value, err := registry.Resolve(context.Background(), "/srv/api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Host != "devbox" {
		t.Fatalf("host not recorded: %+v", value)
	}
	// Qualified, because /srv/api on two machines is two workspaces.
	if value.ID != "devbox:git:/srv/api/.git" {
		t.Fatalf("id not qualified with the host: %q", value.ID)
	}
	if value.Root != "/srv/api" || value.Name != "api" {
		t.Fatalf("the far side's answer was rewritten: %+v", value)
	}
	// /srv/api does not exist here, and nothing about resolving it should
	// have required that it did.
	if _, err := os.Stat("/srv/api"); err == nil {
		t.Skip("/srv/api exists on this machine; the premise does not hold")
	}
}

// TestRemoteResolutionIgnoresAPathThatHappensToExistHere is the quiet
// failure this design exists to prevent. A path can exist on both
// machines and mean different repositories; resolving it locally would
// look entirely correct and merge two workspaces into one group whose
// agents cannot see each other's files.
func TestRemoteResolutionIgnoresAPathThatHappensToExistHere(t *testing.T) {
	shared := t.TempDir()
	// A real git repository here, at the same path the far side answers
	// about. If anything resolved locally, this is what it would find.
	answer := `{"id":"git:` + shared + `/.git","kind":"git","name":"the-far-side-copy",` +
		`"root":"` + shared + `","execution_root":"` + shared + `"}`
	host := fakeHost(t, "devbox", `printf '%s\n' '`+answer+`'`)
	registry := NewRegistryForHost(host)

	value, err := registry.Resolve(context.Background(), shared)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Name != "the-far-side-copy" {
		t.Fatalf("the local filesystem answered instead of the host: %+v", value)
	}
	if value.Host != "devbox" || !strings.HasPrefix(value.ID, "devbox:") {
		t.Fatalf("identity must stay the host's: %+v", value)
	}
}

func TestRemoteExecutionRootsAreQualifiedToo(t *testing.T) {
	answer := `[{"id":"git:/srv/api/.git","kind":"git","name":"api","root":"/srv/api",` +
		`"execution_root":"/srv/api"},` +
		`{"id":"git:/srv/api/.git","kind":"git","name":"api","root":"/srv/api",` +
		`"execution_root":"/srv/api-worktrees/fix"}]`
	host := fakeHost(t, "devbox", `
for last; do :; done
case "$last" in
  *--roots*) printf '%s\n' '`+answer+`' ;;
  *) printf '%s\n' '`+remoteAnswer+`' ;;
esac`)
	registry := NewRegistryForHost(host)

	value, err := registry.Resolve(context.Background(), "/srv/api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	roots, err := registry.ExecutionRoots(context.Background(), value)
	if err != nil {
		t.Fatalf("ExecutionRoots: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("want both checkouts, got %+v", roots)
	}
	for _, root := range roots {
		if root.Host != "devbox" || root.ID != value.ID {
			t.Fatalf("every checkout belongs to the same qualified workspace: %+v", root)
		}
	}
	if roots[1].ExecutionRoot != "/srv/api-worktrees/fix" {
		t.Fatalf("worktree lost: %+v", roots[1])
	}
}

// TestRemoteResolutionReportsTheFarSidesComplaint: the first-run failures
// are all on the other machine — no stormlight there, no such directory,
// a key that is not accepted — and only that machine can say which.
func TestRemoteResolutionReportsTheFarSidesComplaint(t *testing.T) {
	host := fakeHost(t, "devbox", `echo "sh: stormlight: not found" >&2; exit 127`)
	registry := NewRegistryForHost(host)

	_, err := registry.Resolve(context.Background(), "/srv/api")
	if err == nil {
		t.Fatal("a host that cannot answer must not resolve")
	}
	if !strings.Contains(err.Error(), "stormlight: not found") {
		t.Fatalf("want the far side's own words, got: %v", err)
	}
}

// TestRemoteResolutionRefusesARelativePath: a relative path means "here",
// and here is the wrong machine. There is no working directory this side
// can offer that would mean anything over there.
func TestRemoteResolutionRefusesARelativePath(t *testing.T) {
	host := fakeHost(t, "devbox", `printf '%s\n' '`+remoteAnswer+`'`)
	registry := NewRegistryForHost(host)

	if _, err := registry.Resolve(context.Background(), "src/api"); err == nil {
		t.Fatal("a relative remote path must be refused")
	}
	if _, err := registry.Resolve(context.Background(), ""); err == nil {
		t.Fatal("an empty remote path must be refused")
	}
}

func TestLocalRegistryIsUnchangedByHosts(t *testing.T) {
	registry := NewRegistry()
	value, err := registry.Resolve(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Host != "" {
		t.Fatalf("a local workspace has no host: %+v", value)
	}
	if strings.Contains(value.ID, ":"+value.Kind+":") {
		t.Fatalf("a local id is never qualified: %q", value.ID)
	}
}

// TestAnUnconfiguredHostStillResolves: a machine picked out of
// ~/.ssh/config was never written into Stormlight's config, and a
// workspace on it has to resolve anyway. Configuration is how a host
// differs from its name, not the list of which hosts there are.
func TestAnUnconfiguredHostStillResolves(t *testing.T) {
	registry := NewRegistry()
	// Nothing was added for "devbox"; the name is all there is.
	_, err := registry.ResolveOn(context.Background(), "devbox", "/srv/api")
	if err == nil {
		t.Skip("this machine can actually reach a host called devbox")
	}
	// It failed by trying, not by refusing: the error is ssh's, not a
	// complaint that the host was never configured.
	if strings.Contains(err.Error(), "configured") {
		t.Fatalf("an unconfigured host should still be attempted: %v", err)
	}
	if !strings.Contains(err.Error(), "devbox") {
		t.Fatalf("the error should name the host: %v", err)
	}
}

// TestAConfiguredHostKeepsItsSettings: AddHost is how a host differs from
// its bare name — a different destination, an explicit binary — and that
// has to survive the on-demand path.
func TestAConfiguredHostKeepsItsSettings(t *testing.T) {
	registry := NewRegistry()
	host := fakeHost(t, "devbox", `printf '%s\n' '`+remoteAnswer+`'`)
	registry.AddHost(host)

	value, err := registry.ResolveOn(context.Background(), "devbox", "/srv/api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if value.Host != "devbox" || value.Name != "api" {
		t.Fatalf("the configured host was not used: %+v", value)
	}
}
