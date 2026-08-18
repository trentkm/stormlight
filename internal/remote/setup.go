package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
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
	// PackageManager is the first recognised one on the host, and the
	// only thing that makes installing Yazi something Stormlight can
	// offer rather than describe.
	PackageManager string
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
sl=$(look stormlight) && printf 'stormlight=%s\n' "$sl"
[ -n "$sl" ] && printf 'stormlight_version=%s\n' "$("$sl" --version 2>/dev/null | head -1)"
yz=$(look yazi) && printf 'yazi=%s\n' "$yz"
for manager in brew apt-get dnf pacman apk; do
  if command -v "$manager" >/dev/null 2>&1; then printf 'package_manager=%s\n' "$manager"; break; fi
done
`

// Probe asks a host what it has. It is the first thing that touches a
// machine, so its errors are the ones that have to be legible: an
// unaccepted host key, a refused login, a name that does not resolve.
func Probe(ctx context.Context, transport *Transport) (Report, error) {
	command := transport.ShellCommand(ctx, probeScript)
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

// YaziInstallCommand is how this host would install Yazi, or empty when
// Stormlight has no business guessing. Package management belongs to the
// machine's owner; offering to run the one command its own package
// manager would use is help, and inventing one is damage.
func (r Report) YaziInstallCommand() string {
	switch r.PackageManager {
	case "brew":
		return "brew install yazi"
	case "apt-get":
		return "sudo apt-get install -y yazi"
	case "dnf":
		return "sudo dnf install -y yazi"
	case "pacman":
		return "sudo pacman -S --noconfirm yazi"
	case "apk":
		return "sudo apk add yazi"
	default:
		return ""
	}
}

// InstallStormlight copies this machine's binary to the host and puts it
// on the path the bridge will look for.
//
// Copying rather than downloading is deliberate where the platforms
// match: it is the build the dashboard is already running, so the two
// ends cannot disagree about the protocol between them — which is the one
// failure this whole exercise exists to avoid.
func InstallStormlight(
	ctx context.Context,
	transport *Transport,
	report Report,
	binary string,
	progress io.Writer,
) error {
	if !report.CanCopyBinary() {
		return fmt.Errorf(
			"%s is %s and this machine is not; install Stormlight there from its own "+
				"release (https://github.com/trentkm/stormlight/releases) and set "+
				"bin = in [hosts.%s]",
			report.Host, report.Platform, report.Host)
	}
	file, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer file.Close()

	target := report.InstallPath()
	fmt.Fprintf(progress, "copying this machine's stormlight to %s:%s\n", report.Host, target)

	// Three steps rather than one script, because the middle one needs
	// stdin for the binary and a script needs stdin for itself.
	prepare := transport.ShellCommand(ctx, fmt.Sprintf(
		"set -e\nmkdir -p %q\n", pathDir(target)))
	prepare.Stdout, prepare.Stderr = progress, progress
	if err := prepare.Run(); err != nil {
		return fmt.Errorf("prepare %s on %s: %w", pathDir(target), report.Host, err)
	}

	// Written beside the target and moved into place, so a half-copied
	// binary is never the one a hook tries to run.
	copyIn := transport.PipeCommand(ctx, file, "tee", target+".new")
	copyIn.Stdout, copyIn.Stderr = io.Discard, progress
	if err := copyIn.Run(); err != nil {
		return fmt.Errorf("copy stormlight to %s: %w", report.Host, err)
	}

	finish := transport.ShellCommand(ctx, fmt.Sprintf(
		"set -e\nchmod 0755 %[1]q.new\nmv %[1]q.new %[1]q\n%[1]q --version\n", target))
	finish.Stdout, finish.Stderr = progress, progress
	if err := finish.Run(); err != nil {
		return fmt.Errorf("install stormlight on %s: %w", report.Host, err)
	}
	return nil
}

// pathDir is filepath.Dir for a path on another machine, which is always
// slash-separated whatever this one uses.
func pathDir(path string) string {
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return "."
}

// InstallYazi runs the host's own package manager.
func InstallYazi(
	ctx context.Context,
	transport *Transport,
	report Report,
	progress io.Writer,
) error {
	install := report.YaziInstallCommand()
	if install == "" {
		return fmt.Errorf(
			"%s has no package manager Stormlight recognises; install yazi there yourself, "+
				"or use Enter a path instead of browsing", report.Host)
	}
	fmt.Fprintf(progress, "running on %s: %s\n", report.Host, install)
	// A login shell, because the package manager is usually on the PATH
	// a profile sets rather than the one ssh hands a bare command.
	command := transport.ShellCommand(ctx, `"$SHELL" -lc `+shellQuote([]string{install})+"\n")
	command.Stdout = progress
	command.Stderr = progress
	return command.Run()
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
