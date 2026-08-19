package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// A host needs two things, and only one of them is Stormlight's problem.
//
// Stormlight itself is not optional: the bridge, the resolver, and the
// picker are all that binary, so a host without it cannot be reached at
// all. Yazi is optional — it is what makes browsing possible, and a host
// without it can still take a typed path.
type Requirement struct {
	Name string
	// Path is where the host found it; empty means it did not.
	Path string
	// Version is what it reports, when it can be asked.
	Version string
	// Required says whether the host is unusable without it.
	Required bool
}

func (r Requirement) Present() bool { return r.Path != "" }

// Report is what a host says about itself.
type Report struct {
	Host string
	// Platform is `uname -sm`, which is what decides whether this
	// machine's binary can simply be copied over.
	Platform   string
	Home       string
	Stormlight Requirement
	Yazi       Requirement
	// PackageManager is the first recognised one on the host. It is
	// reported for the message it makes possible, not because Yazi is
	// installed with it.
	PackageManager string
	// Musl says the host links against musl rather than glibc, which is
	// a different binary rather than a slower one.
	Musl bool
}

// Ready reports whether the host can host agents at all.
func (r Report) Ready() bool { return r.Stormlight.Present() }

// probeScript asks every question in one round trip, and asks each one
// twice: a command run over ssh gets a non-interactive shell whose PATH
// is often the bare system one, so a login shell is the difference
// between "not installed" and "not on this particular PATH".
const probeScript = `
look() {
  command -v "$1" 2>/dev/null && return 0
  [ -n "$SHELL" ] && "$SHELL" -lc "command -v $1" 2>/dev/null && return 0
  return 1
}
printf 'platform=%s\n' "$(uname -sm)"
printf 'home=%s\n' "$HOME"
if [ -e /lib/ld-musl-x86_64.so.1 ] || [ -e /lib/ld-musl-aarch64.so.1 ] || \
   (command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -qi musl); then
  printf 'libc=musl\n'
fi
# A configured bin is the binary the bridge will actually run, so it is
# the one worth asking about — a Stormlight elsewhere on PATH says
# nothing about whether the configured path exists.
if [ -n "$STORMLIGHT_CONFIGURED_BIN" ]; then
  [ -x "$STORMLIGHT_CONFIGURED_BIN" ] && sl="$STORMLIGHT_CONFIGURED_BIN"
else
  sl=$(look stormlight)
fi
[ -n "$sl" ] && printf 'stormlight=%s\n' "$sl"
[ -n "$sl" ] && printf 'stormlight_version=%s\n' "$("$sl" --version 2>/dev/null | head -1)"
yz=$(look yazi)
# Beside Stormlight, which is where setup puts it and is regularly on no
# PATH at all — without this, a machine reports yazi missing immediately
# after being given one.
if [ -z "$yz" ] && [ -n "$sl" ] && [ -x "$(dirname "$sl")/yazi" ]; then
  yz="$(dirname "$sl")/yazi"
fi
[ -n "$yz" ] && printf 'yazi=%s\n' "$yz"
for manager in brew apt-get dnf pacman apk; do
  if command -v "$manager" >/dev/null 2>&1; then printf 'package_manager=%s\n' "$manager"; break; fi
done
`

// Probe asks a host what it has. It is the first thing that touches a
// machine, so its errors are the ones that have to be legible: an
// unaccepted host key, a refused login, a name that does not resolve.
func Probe(ctx context.Context, transport *Transport) (Report, error) {
	// The configured binary travels as an environment variable rather
	// than spliced into the script: it is a path someone typed, and a
	// script is not the place to find out it had a quote in it.
	script := probeScript
	if bin := transport.Host().Bin; bin != "" {
		script = "STORMLIGHT_CONFIGURED_BIN=" +
			shellQuote([]string{bin}) + "\n" + script
	}
	command := transport.ShellCommand(ctx, script)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return Report{}, fmt.Errorf("%s", Explain(transport.Host().Name, message))
		}
		return Report{}, fmt.Errorf("%s: %w", transport.Host().Name, err)
	}

	report := Report{
		Host:       transport.Host().Name,
		Stormlight: Requirement{Name: "stormlight", Required: true},
		Yazi:       Requirement{Name: "yazi"},
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "platform":
			report.Platform = value
		case "home":
			report.Home = value
		case "stormlight":
			report.Stormlight.Path = value
		case "stormlight_version":
			report.Stormlight.Version = value
		case "yazi":
			report.Yazi.Path = value
		case "package_manager":
			report.PackageManager = value
		case "libc":
			report.Musl = value == "musl"
		}
	}
	return report, nil
}

// InstallPath is where Stormlight puts itself on a host that has none.
// ~/.local/bin is the convention, and the reason `bin` exists in the
// host's configuration: a non-interactive shell often does not have it on
// PATH, so the dashboard names the binary outright rather than hoping.
func (r Report) InstallPath() string {
	home := r.Home
	if home == "" {
		home = "$HOME"
	}
	return home + "/.local/bin/stormlight"
}

// CanCopyBinary reports whether this machine's Stormlight will run on
// that one. Nothing else here is a matter of taste: a Darwin arm64 binary
// on a Linux box is not a slow path, it is a file that will not execute.
func (r Report) CanCopyBinary() bool {
	local, err := localPlatform()
	if err != nil {
		return false
	}
	return r.Platform != "" && r.Platform == local
}

// Target is the host's platform in the naming published builds use.
func (r Report) Target() (Target, bool) { return TargetFor(r.Platform) }

// YaziInstallCommand is what this host's own package manager would use,
// for the machines where Yazi is actually packaged.
//
// It is a suggestion printed for the reader, not the path Stormlight
// takes. Yazi is in Homebrew and in Arch's repositories; it is not in
// Fedora's, and its presence in Debian's and Alpine's depends on how new
// they are. Naming a command that does not exist is worse than naming
// none — `No match for argument: yazi` is how that was found out.
func (r Report) YaziInstallCommand() string {
	switch r.PackageManager {
	case "brew":
		return "brew install yazi"
	case "pacman":
		return "sudo pacman -S yazi"
	default:
		return ""
	}
}

// InstallStormlight puts a Stormlight the host can run at the path the
// bridge will look for, and reports where it went.
//
// Where the platforms match it copies this machine's own binary, which is
// deliberate: it is the build the dashboard is already running, so the
// two ends cannot disagree about the protocol between them — the one
// failure this whole exercise exists to avoid. Where they do not — a Mac
// preparing a Linux box, which is the ordinary case rather than the
// exotic one — it fetches that platform's published archive, checks it
// against the release's own checksums here, and sends the binary from
// inside it.
func InstallStormlight(
	ctx context.Context,
	transport *Transport,
	report Report,
	binary string,
	progress io.Writer,
) (string, error) {
	payload, err := binaryFor(ctx, report, binary, progress)
	if err != nil {
		return "", err
	}
	file := bytes.NewReader(payload)

	target := report.InstallPath()
	fmt.Fprintf(progress, "installing to %s:%s\n", report.Host, target)

	// Three steps rather than one script, because the middle one needs
	// stdin for the binary and a script needs stdin for itself.
	prepare := transport.ShellCommand(ctx, fmt.Sprintf(
		"set -e\nmkdir -p %q\n", pathDir(target)))
	prepare.Stdout, prepare.Stderr = progress, progress
	if err := prepare.Run(); err != nil {
		return "", fmt.Errorf("prepare %s on %s: %w", pathDir(target), report.Host, err)
	}

	// Written beside the target and moved into place, so a half-copied
	// binary is never the one a hook tries to run.
	copyIn := transport.PipeCommand(ctx, file, "tee", target+".new")
	copyIn.Stdout, copyIn.Stderr = io.Discard, progress
	if err := copyIn.Run(); err != nil {
		return "", fmt.Errorf("copy stormlight to %s: %w", report.Host, err)
	}

	finish := transport.ShellCommand(ctx, fmt.Sprintf(
		"set -e\nchmod 0755 %[1]q.new\nmv %[1]q.new %[1]q\n%[1]q --version\n", target))
	finish.Stdout, finish.Stderr = progress, progress
	if err := finish.Run(); err != nil {
		return "", fmt.Errorf("install stormlight on %s: %w", report.Host, err)
	}
	return target, nil
}

// binaryFor is the executable to send: this machine's when the host runs
// the same platform, and that platform's published build otherwise.
func binaryFor(
	ctx context.Context,
	report Report,
	binary string,
	progress io.Writer,
) ([]byte, error) {
	if report.CanCopyBinary() {
		fmt.Fprintf(progress, "%s runs %s, same as here — sending this build\n",
			report.Host, report.Platform)
		return localBinary(binary)
	}
	target, ok := report.Target()
	if !ok {
		return nil, fmt.Errorf(
			"%s reports %q, which is not a platform Stormlight publishes for",
			report.Host, report.Platform)
	}
	fmt.Fprintf(progress, "%s runs %s — fetching the %s build of %s\n",
		report.Host, report.Platform, target, localVersion)
	return releaseBinary(ctx, localVersion, target)
}

// pathDir is filepath.Dir for a path on another machine, which is always
// slash-separated whatever this one uses.
func pathDir(path string) string {
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return "."
}

// InstallYazi puts Yazi's own published build beside Stormlight on the
// host.
//
// Not through the host's package manager: Yazi is absent from some
// distributions' repositories entirely, and a package install wants a
// password that a popup is a poor place to ask for. A user-local binary
// needs no privileges and is the same everywhere.
func InstallYazi(
	ctx context.Context,
	transport *Transport,
	report Report,
	progress io.Writer,
) error {
	target, ok := report.Target()
	if !ok {
		return fmt.Errorf(
			"%s reports %q, which is not a platform yazi publishes for — "+
				"use Enter a path instead of browsing", report.Host, report.Platform)
	}
	fmt.Fprintf(progress, "fetching yazi's %s build\n", target)
	binaries, err := YaziBinaries(ctx, target, report.Musl)
	if err != nil {
		return err
	}

	directory := pathDir(report.InstallPath())
	prepare := transport.ShellCommand(ctx, fmt.Sprintf("set -e\nmkdir -p %q\n", directory))
	prepare.Stdout, prepare.Stderr = progress, progress
	if err := prepare.Run(); err != nil {
		return fmt.Errorf("prepare %s on %s: %w", directory, report.Host, err)
	}

	// yazi is the browser; ya is what its plugins are managed with, and
	// costs one more copy to have rather than to explain the absence of.
	for _, name := range []string{"yazi", "ya"} {
		payload := binaries[name]
		if len(payload) == 0 {
			continue
		}
		destination := directory + "/" + name
		fmt.Fprintf(progress, "installing to %s:%s\n", report.Host, destination)
		copyIn := transport.PipeCommand(ctx, bytes.NewReader(payload), "tee", destination+".new")
		copyIn.Stdout, copyIn.Stderr = io.Discard, progress
		if err := copyIn.Run(); err != nil {
			return fmt.Errorf("copy %s to %s: %w", name, report.Host, err)
		}
		finish := transport.ShellCommand(ctx, fmt.Sprintf(
			"set -e\nchmod 0755 %[1]q.new\nmv %[1]q.new %[1]q\n", destination))
		finish.Stdout, finish.Stderr = progress, progress
		if err := finish.Run(); err != nil {
			return fmt.Errorf("install %s on %s: %w", name, report.Host, err)
		}
	}
	return nil
}

// localPlatform is this machine's `uname -sm`, asked the same way the
// host was asked so the two answers are comparable.
func localPlatform() (string, error) {
	output, err := exec.Command("uname", "-sm").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
