package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSH stands in for the ssh client: whatever the script prints is
// what the host said.
func fakeSSH(t *testing.T, script string) *Transport {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return NewTransport(Host{Name: "devbox", SSHProgram: path})
}

func TestProbeReadsWhatAHostHas(t *testing.T) {
	transport := fakeSSH(t, `cat >/dev/null
printf 'platform=Linux x86_64\n'
printf 'home=/home/trent\n'
printf 'stormlight=/home/trent/.local/bin/stormlight\n'
printf 'stormlight_version=stormlight version v0.2.2\n'
printf 'package_manager=apt-get\n'`)

	report, err := Probe(context.Background(), transport)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if report.Platform != "Linux x86_64" || report.Home != "/home/trent" {
		t.Fatalf("report = %+v", report)
	}
	if !report.Stormlight.Present() || report.Stormlight.Version == "" {
		t.Fatalf("stormlight = %+v", report.Stormlight)
	}
	// Yazi said nothing, so it is missing — and that is not fatal.
	if report.Yazi.Present() {
		t.Fatalf("yazi = %+v", report.Yazi)
	}
	if !report.Ready() {
		t.Fatal("a host with stormlight can hold agents, yazi or not")
	}
	if got := report.YaziInstallCommand(); !strings.Contains(got, "apt-get") {
		t.Fatalf("install command = %q", got)
	}
	if got := report.InstallPath(); got != "/home/trent/.local/bin/stormlight" {
		t.Fatalf("install path = %q", got)
	}
}

// TestAHostWithoutStormlightIsNotReady: the bridge, the resolver and the
// picker are all that binary, so its absence is the one that stops
// everything.
func TestAHostWithoutStormlightIsNotReady(t *testing.T) {
	transport := fakeSSH(t, `cat >/dev/null; printf 'platform=Linux x86_64\n'`)
	report, err := Probe(context.Background(), transport)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if report.Ready() {
		t.Fatal("a host without stormlight cannot be reached at all")
	}
}

// TestProbeExplainsAnUnacceptedHostKey: the first thing anyone does with
// a new machine, and BatchMode turns "should I trust this?" into a
// refusal with no way to say yes.
func TestProbeExplainsAnUnacceptedHostKey(t *testing.T) {
	transport := fakeSSH(t, `cat >/dev/null
echo "Host key verification failed." >&2
exit 255`)
	_, err := Probe(context.Background(), transport)
	if err == nil {
		t.Fatal("an unverified host key must not look like an empty report")
	}
	if !strings.Contains(err.Error(), "ssh devbox") {
		t.Fatalf("the error should say how to fix it: %v", err)
	}
}

// TestAForeignPlatformGetsItsOwnBuild: a Darwin arm64 binary on a Linux
// box is not a slow path, it is a file that will not execute — so the
// host's own published build is fetched instead. macOS preparing a Linux
// machine is the ordinary case, not the exotic one.
func TestAForeignPlatformGetsItsOwnBuild(t *testing.T) {
	local, err := localPlatform()
	if err != nil {
		t.Skip("cannot read this machine's platform")
	}
	if !(Report{Platform: local}).CanCopyBinary() {
		t.Fatal("the same platform should be copyable")
	}
	foreign := Report{Host: "devbox", Platform: "Linux aarch64"}
	if foreign.CanCopyBinary() {
		t.Fatal("a different platform must not be copied to")
	}
	target, ok := foreign.Target()
	if !ok || target.OS != "linux" || target.Arch != "arm64" {
		t.Fatalf("target = %+v, ok = %v", target, ok)
	}
}

// TestPlatformNamesAreTranslated: uname and Go name the same machines
// differently, and picking the wrong one downloads a binary that will not
// run.
func TestPlatformNamesAreTranslated(t *testing.T) {
	for platform, want := range map[string]Target{
		"Linux aarch64": {OS: "linux", Arch: "arm64"},
		"Linux x86_64":  {OS: "linux", Arch: "amd64"},
		"Darwin arm64":  {OS: "darwin", Arch: "arm64"},
		"Darwin x86_64": {OS: "darwin", Arch: "amd64"},
	} {
		got, ok := TargetFor(platform)
		if !ok || got != want {
			t.Fatalf("%s → %+v (%v), want %+v", platform, got, ok, want)
		}
	}
	for _, unknown := range []string{"Plan9 386", "Linux", "", "FreeBSD amd64"} {
		if _, ok := TargetFor(unknown); ok {
			t.Fatalf("%q is not a platform Stormlight publishes for", unknown)
		}
	}
}

// TestADevelopmentBuildSaysSoRatherThanGuessing: there is no published
// archive for a build that was never published, and inventing a version
// to download would install something other than what is running here.
func TestADevelopmentBuildSaysSoRatherThanGuessing(t *testing.T) {
	_, err := releaseBinary(
		context.Background(), "dev", Target{OS: "linux", Arch: "arm64"})
	if err == nil {
		t.Fatal("a development build has no release to install from")
	}
	for _, want := range []string{"development build", "bin ="} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("it should say what to do instead: %v", err)
		}
	}
}

// TestAChecksumMismatchRefuses: the machine being prepared is the one
// that cannot check anything yet, which is why this side checks.
func TestAChecksumMismatchRefuses(t *testing.T) {
	sums := []byte("abc123  stormlight_1.0.0_linux_arm64.tar.gz\n" +
		"def456  stormlight_1.0.0_darwin_arm64.tar.gz\n")
	got, ok := checksumFor(sums, "stormlight_1.0.0_linux_arm64.tar.gz")
	if !ok || got != "abc123" {
		t.Fatalf("checksum = %q, %v", got, ok)
	}
	if _, ok := checksumFor(sums, "stormlight_1.0.0_windows_arm64.tar.gz"); ok {
		t.Fatal("a file the release never published has no checksum")
	}
}

// TestYaziIsOnlyOfferedWhereItCanBeInstalled: package management belongs
// to the machine's owner. Offering the one command its own package
// manager would use is help; inventing one is damage.
func TestYaziIsOnlyOfferedWhereItCanBeInstalled(t *testing.T) {
	for manager, want := range map[string]string{
		"brew":    "brew install yazi",
		"pacman":  "pacman -S",
		"unknown": "",
	} {
		got := (Report{PackageManager: manager}).YaziInstallCommand()
		if want == "" && got != "" {
			t.Fatalf("%s: offered %q", manager, got)
		}
		if want != "" && !strings.Contains(got, want) {
			t.Fatalf("%s: got %q", manager, got)
		}
	}
	err := InstallYazi(context.Background(), nil, Report{Host: "devbox"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Enter a path") {
		t.Fatalf("it should say what still works instead: %v", err)
	}
}

// TestScriptsDoNotDependOnTheLoginShell: `ssh host <script>` runs it in
// whatever shell that account has, which is as likely to be fish as sh —
// and a POSIX script is not fish. Sending it to /bin/sh on stdin means
// nothing quotes it and nothing else parses it.
func TestScriptsDoNotDependOnTheLoginShell(t *testing.T) {
	transport := NewTransport(Host{Name: "devbox"})
	command := transport.ShellCommand(context.Background(), "printf hello\n")
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "/bin/sh -s") {
		t.Fatalf("a script needs a shell it can name: %v", command.Args)
	}
	if strings.Contains(joined, "printf hello") {
		t.Fatalf("the script must not be an argument for a login shell to parse: %v",
			command.Args)
	}
	if command.Stdin == nil {
		t.Fatal("the script travels on stdin")
	}
}
