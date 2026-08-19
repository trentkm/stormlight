package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
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
	// AccountShell is the shell the host's account records — the default
	// providers are looked for and launched under.
	AccountShell string
	// Shells is what each of the host's login shells can see, in a
	// stable order with the account's own first. Empty when the probe
	// was not asked about any providers.
	Shells []ShellReport
	// Asked are the providers the survey asked about, which is what
	// "this shell sees everything" is measured against.
	Asked []string
}

// ShellReport is one login shell on a host, and which of the providers
// asked about its login PATH can find.
//
// It is evidence rather than advice. A machine cannot say which shell
// its owner meant to work in — that is why [hosts.*].shell exists — but
// it can say which shells can see the programs, and that turns an
// unanswerable question into a list.
type ShellReport struct {
	Path string
	// Finds are the providers this shell's login PATH can see, named as
	// they were asked about.
	Finds []string
}

// Sees reports whether this shell can find a provider.
func (s ShellReport) Sees(program string) bool {
	return slices.Contains(s.Finds, program)
}

// Account is the report for the shell this host's account records, which
// is the one Stormlight uses when nothing says otherwise.
func (r Report) Account() (ShellReport, bool) {
	for _, shell := range r.Shells {
		if shell.Path == r.AccountShell {
			return shell, true
		}
	}
	return ShellReport{}, false
}

// SuggestedShell is the shell worth naming in [hosts.*], if any.
//
// The rule is narrow on purpose. The account's own shell is the default
// and stays the default whenever it can see everything — a host that
// works is not improved by being configured. A suggestion is made only
// when some other shell can see strictly more, which is the situation
// the setting exists for and the one nothing else can diagnose: the
// provider is installed, it works when its owner runs it, and the
// account shell cannot find it.
//
// Nothing here decides anything. What it returns is the shell that saw
// the most, for a caller to act on or offer.
func (r Report) SuggestedShell() (ShellReport, bool) {
	if len(r.Asked) == 0 || len(r.Shells) == 0 {
		return ShellReport{}, false
	}
	account, known := r.Account()
	if known && len(account.Finds) == len(r.Asked) {
		return ShellReport{}, false
	}
	best := ShellReport{}
	for _, shell := range r.Shells {
		if shell.Path == r.AccountShell || len(shell.Finds) <= len(account.Finds) {
			continue
		}
		if len(shell.Finds) > len(best.Finds) {
			best = shell
		}
	}
	return best, best.Path != ""
}

// Ready reports whether the host can host agents at all.
func (r Report) Ready() bool { return r.Stormlight.Present() }

// probeScript asks every question in one round trip, and asks each one
// twice: a command run over ssh gets a non-interactive shell whose PATH
// is often the bare system one, so a login shell is the difference
// between "not installed" and "not on this particular PATH".
const probeScript = `
shell=${STORMLIGHT_SHELL:-$SHELL}
look() {
  command -v "$1" 2>/dev/null && return 0
  [ -n "$shell" ] && "$shell" -lc "command -v $1" 2>/dev/null && return 0
  return 1
}
# A file that will not start is not an installed program. A binary built
# against a newer libc than this machine has exists, is executable, and
# fails before main — reporting it present is how a host looks ready and
# then cannot browse.
runs() {
  [ -n "$1" ] && "$1" --version >/dev/null 2>&1
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
runs "$sl" || sl=""
[ -n "$sl" ] && printf 'stormlight=%s\n' "$sl"
[ -n "$sl" ] && printf 'stormlight_version=%s\n' "$("$sl" --version 2>/dev/null | head -1)"
yz=$(look yazi)
# Beside Stormlight, which is where setup puts it and is regularly on no
# PATH at all — without this, a machine reports yazi missing immediately
# after being given one.
if [ -z "$yz" ] && [ -n "$sl" ] && [ -x "$(dirname "$sl")/yazi" ]; then
  yz="$(dirname "$sl")/yazi"
fi
runs "$yz" || yz=""
[ -n "$yz" ] && printf 'yazi=%s\n' "$yz"
for manager in brew apt-get dnf pacman apk; do
  if command -v "$manager" >/dev/null 2>&1; then printf 'package_manager=%s\n' "$manager"; break; fi
done
`

// shellSurveyScript asks the host which of its shells can see the
// providers. It runs only when there are providers to ask about, because
// it starts a login shell per candidate and a login shell is not cheap —
// the dashboard's own reachability check has no use for the answer.
//
// Candidates are the account's own shell, whatever /etc/shells
// registers, and a short list of names that may be installed without
// being registered. Nothing here picks one: the answer is a list of
// shells and what each can see, and choosing is somebody else's job.
const shellSurveyScript = `
NL='
'
shells="$NL"
add_shell() {
  [ -n "$1" ] || return 0
  [ -x "$1" ] || return 0
  case "$shells" in *"$NL$1$NL"*) return 0 ;; esac
  shells="$shells$1$NL"
}
add_shell "$SHELL"
if [ -r /etc/shells ]; then
  while IFS= read -r line; do
    case "$line" in ''|\#*) continue ;; esac
    add_shell "$line"
  done < /etc/shells
fi
for name in fish zsh bash ksh dash sh; do
  add_shell "$(command -v "$name" 2>/dev/null)"
done
printf 'account_shell=%s\n' "$SHELL"
# One invocation per shell, asking about every provider at once. The
# query is "command -v x >/dev/null 2>&1 && echo ..." repeated, which is
# the same program in sh, bash, zsh, ksh, and fish alike — a loop or an
# if would not be.
query=""
for program in $providers; do
  query="$query command -v $program >/dev/null 2>&1 && echo shell_found=$program;"
done
printf '%s' "$shells" | while IFS= read -r candidate; do
  [ -n "$candidate" ] || continue
  printf 'shell=%s\n' "$candidate"
  [ -n "$query" ] || continue
  # A login shell may greet, print a motd, or complain; only the lines
  # this asked for are read, and the rest is noise by construction.
  "$candidate" -lc "$query" 2>/dev/null | while IFS= read -r line; do
    case "$line" in shell_found=*) printf '%s\n' "$line" ;; esac
  done
done
`

// Probe asks a host what it has. It is the first thing that touches a
// machine, so its errors are the ones that have to be legible: an
// unaccepted host key, a refused login, a name that does not resolve.
func Probe(ctx context.Context, transport *Transport, providers ...string) (Report, error) {
	// The configured binary travels as an environment variable rather
	// than spliced into the script: it is a path someone typed, and a
	// script is not the place to find out it had a quote in it.
	script := transport.Host().shellAssignment() + probeScript
	if bin := transport.Host().Bin; bin != "" {
		script = "STORMLIGHT_CONFIGURED_BIN=" +
			shellQuote([]string{bin}) + "\n" + script
	}
	// The survey costs a login shell per candidate, so it is asked for
	// rather than assumed: the dashboard's reachability check wants an
	// answer in a frame and has no use for this one.
	asked := plainWords(providers)
	if len(asked) > 0 {
		script += "providers='" + strings.Join(asked, " ") + "'\n" + shellSurveyScript
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

	report := parseReport(stdout.String())
	report.Host = transport.Host().Name
	report.Asked = asked
	return report, nil
}

// parseReport reads back what the probe printed: one key=value per line,
// and for the survey a shell followed by what it found.
func parseReport(answered string) Report {
	report := Report{
		Stormlight: Requirement{Name: "stormlight", Required: true},
		Yazi:       Requirement{Name: "yazi"},
	}
	for _, line := range strings.Split(answered, "\n") {
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
		case "account_shell":
			report.AccountShell = value
		case "shell":
			report.Shells = append(report.Shells, ShellReport{Path: value})
		case "shell_found":
			// Belongs to the shell most recently named: the survey walks
			// one shell at a time and says so before it answers.
			if last := len(report.Shells) - 1; last >= 0 {
				report.Shells[last].Finds = append(report.Shells[last].Finds, value)
			}
		}
	}
	return report
}

// plainWords keeps the provider names that can be asked about without
// quoting, and drops the rest.
//
// A name with a space or a quote in it would have to survive being read
// by a shell this machine does not choose, and there is no spelling that
// means the same thing in POSIX and in fish. Rather than guess, such a
// provider is simply not surveyed — the host is still probed, and the
// answer is silent about that one rather than wrong about it.
func plainWords(names []string) []string {
	plain := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, " \t\n\r'\"$`\\;&|<>()*?[]{}#~!") {
			continue
		}
		plain = append(plain, name)
	}
	return plain
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
		// Started once before it is called installed: a binary the host
		// cannot run is not an installation, and finding that out here
		// beats finding it out inside a popup that has already closed.
		finish := transport.ShellCommand(ctx, fmt.Sprintf(
			"set -e\nchmod 0755 %[1]q.new\nmv %[1]q.new %[1]q\n%[1]q --version\n",
			destination))
		finish.Stdout, finish.Stderr = progress, progress
		if err := finish.Run(); err != nil {
			return fmt.Errorf(
				"%s cannot run the %s just installed at %s: %w",
				report.Host, name, destination, err)
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

// LookPath asks a host whether it has a program, in the environment that
// would run it.
//
// A command run over ssh gets a non-interactive shell whose PATH is
// often the bare system one, so what is asked is the host's login shell
// — the configured one, or the account's own. That is the difference
// between finding a provider a machine plainly has and reporting it
// absent.
//
// This is a check, not a resolution: what the dispatch does with the
// answer is report it. The path that actually runs is resolved on the
// far side at launch, in that same shell, because that is the only
// place the environment is real.
func LookPath(ctx context.Context, transport *Transport, program string) (string, error) {
	if strings.TrimSpace(program) == "" {
		return "", fmt.Errorf("no program to look for")
	}
	host := transport.Host()
	// Ends with a plain exit 0: not finding the program is an answer,
	// not a failure of the asking, and a script whose last command is a
	// test would exit non-zero and be read as one.
	script := host.shellAssignment() +
		"program=" + shellQuote([]string{program}) + `
shell=${STORMLIGHT_SHELL:-$SHELL}
if [ -n "$STORMLIGHT_SHELL" ] && [ ! -x "$STORMLIGHT_SHELL" ]; then
  printf 'noshell=%s\n' "$STORMLIGHT_SHELL"
  exit 0
fi
# The shell first, and not merely as a fallback. It is the environment
# the provider will be launched in, so its answer is the true one — and
# on a host where the bare SSH PATH holds a different build of the same
# name, asking that one first is how the check passes and the launch
# runs something else.
found=""
if [ -n "$shell" ]; then
  found=$("$shell" -lc "command -v $program" 2>/dev/null)
fi
if [ -z "$found" ]; then
  found=$(command -v "$program" 2>/dev/null)
fi
if [ -n "$found" ]; then printf 'found=%s\n' "$found"; fi
# Named in the failure, so that "not installed" can point at the shell
# that was asked rather than leaving that to be guessed at.
printf 'asked=%s\n' "$shell"
exit 0
`
	command := transport.ShellCommand(ctx, script)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return "", errors.New(Explain(host.Name, message))
		}
		return "", fmt.Errorf("%s: %w", host.Name, err)
	}
	answers := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			answers[key] = value
		}
	}
	// A configured shell that is not there is its own failure, and one
	// worth naming: every provider on the host would otherwise be
	// reported missing, which points at the wrong thing entirely.
	if bad, ok := answers["noshell"]; ok {
		return "", fmt.Errorf(
			"%s: shell = %q in [hosts.%s] is not an executable file there",
			host.Name, bad, host.Name)
	}
	if found := answers["found"]; found != "" {
		return found, nil
	}
	return "", fmt.Errorf("%s is not installed on %s, or not on the PATH %s sets.%s",
		program, host.Name, host.shellDescription(answers["asked"]), host.shellHint())
}
