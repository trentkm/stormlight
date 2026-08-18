package remote

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeSSHConfig(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".ssh", "config"), []byte(content), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func TestKnownHostsReadsHostEntries(t *testing.T) {
	writeSSHConfig(t, `
# a comment
Host devbox
  HostName 10.0.0.4
  User trent

Host build ci
  HostName builder.internal
`)
	hosts := KnownHosts()
	if !slices.Equal(names(hosts), []string{"devbox", "build", "ci"}) {
		t.Fatalf("hosts = %v, want them in file order", names(hosts))
	}
	// The details are what tell one `builder` from another in a list.
	if got := hosts[0].Summary(); got != "trent@10.0.0.4" {
		t.Fatalf("devbox summary = %q", got)
	}
	// One block can name several machines, and they share its settings.
	if hosts[1].HostName != "builder.internal" || hosts[2].HostName != "builder.internal" {
		t.Fatalf("a shared Host block should reach both: %+v", hosts[1:])
	}
}

func names(hosts []ConfigHost) []string {
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, host.Name)
	}
	return out
}

// TestSummarySaysNothingItDoesNotKnow: `Host laptop` with no HostName
// resolves to "laptop", and echoing the name back as its own detail is
// noise in a list of names.
func TestSummarySaysNothingItDoesNotKnow(t *testing.T) {
	if got := (ConfigHost{Name: "laptop"}).Summary(); got != "" {
		t.Fatalf("summary = %q, want nothing", got)
	}
	if got := (ConfigHost{Name: "b", HostName: "b.internal", Port: "2222"}).Summary(); got != "b.internal:2222" {
		t.Fatalf("summary = %q", got)
	}
	// Port 22 is the default and says nothing.
	if got := (ConfigHost{Name: "b", HostName: "b.internal", Port: "22"}).Summary(); got != "b.internal" {
		t.Fatalf("summary = %q", got)
	}
}

// TestKnownHostsSkipsPatterns: `Host *.internal` configures whatever
// matches it. It is not a machine, and offering it as one offers
// something that cannot work.
func TestKnownHostsSkipsPatterns(t *testing.T) {
	writeSSHConfig(t, `
Host *
  AddKeysToAgent yes

Host *.internal
  User deploy

Host build-*
  Port 2222

Host !prod
  User ci

Host devbox
  HostName 10.0.0.4
`)
	hosts := KnownHosts()
	if !slices.Equal(names(hosts), []string{"devbox"}) {
		t.Fatalf("hosts = %v, want only the one real name", names(hosts))
	}
}

// TestKnownHostsFollowsInclude: splitting the config is common enough —
// work in one file, personal machines in another — that a host list which
// ignored Include would be missing exactly the machines people organised.
func TestKnownHostsFollowsInclude(t *testing.T) {
	writeSSHConfig(t, `
Include work/*.conf
Host laptop
`)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".ssh", "work", "hosts.conf"),
		[]byte("Host devbox\n  HostName 10.0.0.4\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}

	hosts := KnownHosts()
	if !slices.Contains(names(hosts), "devbox") || !slices.Contains(names(hosts), "laptop") {
		t.Fatalf("hosts = %v, want both the included and the direct entry", names(hosts))
	}
}

func TestKnownHostsSurvivesAnIncludeLoop(t *testing.T) {
	writeSSHConfig(t, "Include config\nHost devbox\n")
	done := make(chan []ConfigHost, 1)
	go func() { done <- KnownHosts() }()
	select {
	case hosts := <-done:
		if !slices.Contains(names(hosts), "devbox") {
			t.Fatalf("hosts = %v", names(hosts))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a self-including config should stop, not spin")
	}
}

// TestNoSSHConfigIsNotAFailure: plenty of people have never written one,
// and Stormlight can still reach any destination ssh can. The suggestion
// list is simply empty.
func TestNoSSHConfigIsNotAFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if hosts := KnownHosts(); len(hosts) != 0 {
		t.Fatalf("hosts = %v, want none", hosts)
	}
}

func TestKnownHostsAcceptsEqualsForm(t *testing.T) {
	writeSSHConfig(t, "Host=devbox\n  HostName=10.0.0.4\n")
	if hosts := KnownHosts(); !slices.Equal(names(hosts), []string{"devbox"}) {
		t.Fatalf("hosts = %v", names(hosts))
	}
}
