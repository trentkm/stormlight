package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultBin is what a host runs when it has not said otherwise. The
// bridge reports back where that resolved to, so the guess only has to be
// good enough to start.
const defaultBin = "stormlight"

// dialTimeout bounds everything between exec and the handshake: the TCP
// connect, the authentication, the remote daemon's own start-up. A host
// that has not said hello by then is a host to report unreachable.
const dialTimeout = 30 * time.Second

// Host is a machine whose agents this dashboard can see.
type Host struct {
	// Name is what the dashboard calls it, and what qualifies the
	// workspace IDs of everything running there.
	Name string
	// Destination is what ssh is given: user@host, or an ssh_config
	// alias, or a tailnet name. Empty means Name doubles as it, which is
	// the common case.
	Destination string
	// Bin is the remote stormlight, when it is not simply on PATH.
	// Non-interactive SSH shells often do not have ~/.local/bin, which
	// is the usual reason to set it.
	Bin string
	// Options are extra ssh flags, spliced in ahead of the destination.
	Options []string
	// Shell is the login shell that defines this host's execution
	// environment, absolute. Empty means the account's own $SHELL,
	// resolved on the far side because that is where it is known.
	Shell string
	// SSHProgram is the client to run, for a host reached through a
	// wrapper rather than the ssh on PATH.
	SSHProgram string
	// NoMultiplex turns off connection sharing. On by default because a
	// client opens one connection for the control plane, one for the
	// event feed, and one per attached terminal — without a shared
	// master that is an authentication handshake apiece.
	NoMultiplex bool
}

func (h Host) destination() string {
	if h.Destination != "" {
		return h.Destination
	}
	return h.Name
}

func (h Host) sshProgram() string {
	if h.SSHProgram != "" {
		return h.SSHProgram
	}
	return "ssh"
}

func (h Host) bin() string {
	if h.Bin != "" {
		return h.Bin
	}
	return defaultBin
}

// Env names the environment the far side reads. These are set on the
// agent's process by the daemon there, so they are the one channel that
// no shell parses: a value goes in as bytes and comes out as bytes,
// whatever quoting rules the login shell has.
const (
	// ShellEnv names the login shell to run providers under, when a host
	// has been configured with one.
	ShellEnv = "STORMLIGHT_SHELL"
	// ExecEnv carries the provider's argv, JSON-encoded, for `stormlight
	// _exec` to decode and become.
	ExecEnv = "STORMLIGHT_EXEC"
)

// ExecSubcommand is what the prelude runs on the far side. It is named
// once, here, because the prelude is shell text: a rename that missed it
// would leave a launch invoking a subcommand nothing registers, and the
// only report of that would be an agent dying with whatever the root
// command says about an argument it does not know.
const ExecSubcommand = "_exec"

// ExecPrelude is what the daemon on a host actually spawns, with the
// provider's argv waiting in ExecEnv.
//
// It is a POSIX script run by /bin/sh — the one shell every host has and
// the only one whose syntax may be assumed — and its whole job is to
// hand off to the shell that owns the host's environment. That shell
// then execs Stormlight, which execs the provider. Three execs, one
// process: what the daemon supervises is the provider itself, so signals,
// exit status, and lifetime are unchanged by the detour.
//
// Nothing here is interpolated. The provider's argv never appears in
// shell text at all — it travels in ExecEnv — because task text and
// custom provider arguments are arbitrary input, and the shell that
// would parse them is one this machine does not choose. The only
// variable syntax used, "$VAR", means the same thing in sh, bash, zsh,
// ksh, and fish alike.
const ExecPrelude = `shell=${` + ShellEnv + `:-$SHELL}
[ -n "$shell" ] || shell=/bin/sh
if [ ! -x "$shell" ]; then
  echo "stormlight: $shell is not an executable shell on $(hostname 2>/dev/null)" >&2
  exit 127
fi
exec "$shell" -lc 'exec "$STORMLIGHT_BIN" ` + ExecSubcommand + `'
`

// shellAssignment prefixes a script with the shell this host runs things
// under, for the scripts that ask questions about the host.
//
// It is an assignment rather than a substituted value because the
// fallback is only knowable on the far side: $SHELL is that machine's
// answer, and asking for it first would be a round trip to learn
// something the script is about to be told anyway.
func (h Host) shellAssignment() string {
	if h.Shell == "" {
		return ""
	}
	return ShellEnv + "=" + shellQuote([]string{h.Shell}) + "\n"
}

// shellHint is the sentence that turns "not installed" into something to
// do about it.
//
// It is for the case that produced this setting: a provider that is
// plainly there, on a host whose account shell is not the shell its
// owner works in. Nothing about the machine reveals that, so the message
// says the setting exists and lets the person say which. A host already
// configured with a shell needs no hint — it has been told, and the
// answer is that the provider really is not on that shell's PATH.
func (h Host) shellHint() string {
	if h.Shell != "" {
		return ""
	}
	return fmt.Sprintf(
		"\nIf providers live on another shell's PATH there, name it: "+
			"shell = \"/path/to/shell\" under [hosts.%s]", h.Name)
}

// shellDescription names the shell in a sentence about it: the
// configured one, or the account's own as the host reported it.
func (h Host) shellDescription(asked string) string {
	if h.Shell != "" {
		return h.Shell
	}
	if asked != "" {
		return asked
	}
	return "its login shell"
}

// Transport dials one host's daemon. It is the thing a windrunner client
// is built on, and it remembers what the far side said about itself.
type Transport struct {
	host Host

	mu    sync.Mutex
	hello Hello
}

func NewTransport(host Host) *Transport { return &Transport{host: host} }

func (t *Transport) Host() Host { return t.host }

// Hello is what the most recent successful dial reported. Its zero value
// means nothing has connected yet.
func (t *Transport) Hello() Hello {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hello
}

// Dial opens one connection to the host's daemon, as windrunner's client
// asks for a connection: for the control plane, for the event feed, and
// once per attached terminal.
func (t *Transport) Dial() (net.Conn, error) {
	conn, err := dialCommand(t.Command(nil, "_wrbridge"), t.host.destination())
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", t.host.Name, err)
	}
	hello, err := Handshake(conn, t.host.Name)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("reach %s: %w", t.host.Name, err)
	}
	t.mu.Lock()
	t.hello = hello
	t.mu.Unlock()
	return conn, nil
}

// Command builds an ssh invocation of the remote stormlight. The
// dashboard uses it for the bridge, and for the subcommands that have to
// run where the filesystem is: the full-screen attachment, workspace
// resolution, reading a transcript. env entries ride as a shell
// assignment prefix on the far side, for the variables that cannot be
// left to whatever that host's shell happens to set.
func (t *Transport) Command(env []string, args ...string) *exec.Cmd {
	return exec.Command(t.host.sshProgram(), t.sshArgs(false, env, args)...)
}

// ShellCommand runs a script on the host rather than Stormlight — for
// the questions asked before Stormlight is known to be there at all, and
// for putting it there.
//
// The script travels on stdin, to a POSIX shell asked for by name. Both
// halves of that matter. `ssh host <command>` hands the command to the
// account's *login* shell, which is as likely to be fish as sh, and a
// POSIX script is not fish — nor does any amount of quoting make it one,
// since the quoting itself is what fish parses differently. Reading the
// script from stdin means nothing quotes it at all.
func (t *Transport) ShellCommand(ctx context.Context, script string) *exec.Cmd {
	sshArgs := t.sshOptions(false)
	sshArgs = append(sshArgs, t.host.destination(), "/bin/sh", "-s")
	command := exec.CommandContext(ctx, t.host.sshProgram(), sshArgs...)
	command.Stdin = strings.NewReader(script)
	return command
}

// PipeCommand runs one command on the host with something of the
// caller's on its stdin — a file being copied there, most of the time.
// The arguments are quoted words rather than a script, which every shell
// agrees about.
func (t *Transport) PipeCommand(ctx context.Context, stdin io.Reader, args ...string) *exec.Cmd {
	sshArgs := t.sshOptions(false)
	sshArgs = append(sshArgs, t.host.destination(), shellQuote(args))
	command := exec.CommandContext(ctx, t.host.sshProgram(), sshArgs...)
	command.Stdin = stdin
	return command
}

// CommandContext is Command bound to a context, for the calls a caller
// gives up on: workspace resolution runs on the dashboard's refresh path,
// where an unreachable host must not hold the frame.
func (t *Transport) CommandContext(ctx context.Context, env []string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, t.host.sshProgram(), t.sshArgs(false, env, args)...)
}

// TTYCommand is Command for a program that needs a terminal on the far
// side — the full-screen attachment, which is one.
func (t *Transport) TTYCommand(env []string, args ...string) *exec.Cmd {
	return exec.Command(t.host.sshProgram(), t.sshArgs(true, env, args)...)
}

// sshOptions is everything before the destination.
func (t *Transport) sshOptions(tty bool) []string {
	// BatchMode is not about scripting convenience. The dashboard owns
	// the terminal, and ssh prompting for a password or a host key on
	// /dev/tty would paint over it with something nobody can answer. Fail
	// with a message instead.
	// ConnectTimeout bounds every call. Without it a host that is merely
	// asleep takes ssh's own default to fail — long enough that the
	// dashboard looks broken rather than patient — and this runs on the
	// refresh path.
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if tty {
		sshArgs = append(sshArgs, "-t")
	}
	if !t.host.NoMultiplex {
		sshArgs = append(sshArgs,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+t.controlPath(),
			"-o", "ControlPersist=60s",
		)
	}
	return append(sshArgs, t.host.Options...)
}

func (t *Transport) sshArgs(tty bool, env, args []string) []string {
	sshArgs := t.sshOptions(tty)
	sshArgs = append(sshArgs, t.host.destination())
	remote := make([]string, 0, len(args)+1)
	remote = append(remote, t.host.bin())
	remote = append(remote, args...)
	// The far side is a login shell, not an argv. Anything with a space
	// in it — a workspace path, a task — comes apart without this.
	command := shellQuote(remote)
	if len(env) > 0 {
		command = shellQuoteAssignments(env) + " " + command
	}
	sshArgs = append(sshArgs, command)
	return sshArgs
}

// controlPath names the shared connection's socket. It is hashed rather
// than spelled out because a unix socket path caps near 104 bytes and a
// destination can be long.
func (t *Transport) controlPath() string {
	sum := sha256.Sum256([]byte(t.host.destination()))
	dir := filepath.Join(stateDir(), "ssh")
	os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, hex.EncodeToString(sum[:6])+".sock")
}

// shellQuoteAssignments renders NAME=value entries as a command prefix
// the far shell applies to the command that follows: the name is bare
// (a quoted one would not be an assignment at all) and only the value is
// quoted.
func shellQuoteAssignments(env []string) string {
	assignments := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		assignments = append(assignments, name+"="+shellQuote([]string{value}))
	}
	return strings.Join(assignments, " ")
}

func shellQuote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}

// Handshake is the client half of Serve: it reads the bridge's greeting
// and decides whether the two ends can talk. Everything after the line it
// consumes belongs to windrunner.
func Handshake(conn net.Conn, host string) (Hello, error) {
	hello, err := readHello(conn)
	if err != nil {
		return Hello{}, err
	}
	if hello.Protocol != Protocol {
		return Hello{}, mismatch(host, hello)
	}
	return hello, nil
}

// mismatch says which of the two machines is behind, and what to do
// about that one.
//
// Which side is older is the whole of what the reader needs and the one
// thing the numbers alone do not say. "Upgrade the older side" leaves
// them to work out which that is, from two version strings that may both
// read "dev" — and then to remember that a host is updated by a command
// on this machine, not on that one.
func mismatch(host string, hello Hello) error {
	if host == "" {
		host = "that machine"
	}
	if hello.Protocol < Protocol {
		return fmt.Errorf(
			"%s runs an older Stormlight (%s) than this dashboard (%s): it speaks "+
				"bridge protocol %d and this one speaks %d. Update it with "+
				"`stormlight remote setup %s --install`",
			host, describeVersion(hello.Version), describeVersion(localVersion),
			hello.Protocol, Protocol, host)
	}
	return fmt.Errorf(
		"%s runs a newer Stormlight (%s) than this dashboard (%s): it speaks "+
			"bridge protocol %d and this one speaks %d. Update this machine",
		host, describeVersion(hello.Version), describeVersion(localVersion),
		hello.Protocol, Protocol)
}

// describeVersion keeps an unbuilt binary from being reported as a
// version anybody could look up.
func describeVersion(version string) string {
	if version == "" {
		return "unknown"
	}
	return version
}

// Explain turns ssh's own diagnostics into something with a next step.
// The dashboard runs ssh in BatchMode, which is right — a password prompt
// would paint over the TUI with something nobody can answer — but it
// turns "should I trust this host?" into a refusal with no way to say
// yes. The answer is to say yes once, in a terminal, and ssh remembers.
func Explain(host string, message string) string {
	if strings.Contains(message, "Host key verification failed") {
		return fmt.Sprintf(
			"%s: its host key is not known yet — run `ssh %s` once to accept it",
			host, host)
	}
	if strings.Contains(message, "Permission denied") {
		return fmt.Sprintf(
			"%s: %s (Stormlight cannot answer a password prompt; it needs key-based login)",
			host, message)
	}
	return host + ": " + message
}

// readHello consumes the greeting line, one byte at a time so that not a
// byte of the protocol behind it is buffered away. The line is short and
// this happens once per connection.
func readHello(conn net.Conn) (Hello, error) {
	if err := conn.SetReadDeadline(time.Now().Add(dialTimeout)); err != nil {
		return Hello{}, err
	}
	defer conn.SetReadDeadline(time.Time{})

	var line []byte
	buffer := make([]byte, 1)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			if buffer[0] == '\n' {
				break
			}
			line = append(line, buffer[0])
			if len(line) > 8192 {
				return Hello{}, fmt.Errorf("bridge greeting is not a greeting: %q", firstLine(line))
			}
			continue
		}
		if err != nil {
			if len(line) > 0 {
				// Something answered, but not the bridge: a login banner,
				// a shell complaining, a motd.
				return Hello{}, fmt.Errorf("no bridge answered: %s", firstLine(line))
			}
			return Hello{}, err
		}
	}

	var hello Hello
	if err := json.Unmarshal(line, &hello); err != nil {
		return Hello{}, fmt.Errorf("no bridge answered: %s", firstLine(line))
	}
	return hello, nil
}

func firstLine(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// Serve is the far side of Dial: the process ssh started. It has already
// ensured a daemon is running on this machine; here it says who it is and
// becomes a pipe onto that daemon's socket.
func Serve(socketPath string, hello Hello, in io.Reader, out io.Writer) error {
	encoded, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return err
	}
	if flusher, ok := out.(interface{ Sync() error }); ok {
		_ = flusher.Sync()
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("reach the local daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

	// Both directions, and the first one to end ends the bridge: a
	// closed stdin means the dashboard hung up, and a closed socket
	// means the daemon did.
	finished := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, in)
		if closer, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		finished <- err
	}()
	go func() {
		_, err := io.Copy(out, conn)
		finished <- err
	}()
	return <-finished
}

func stateDir() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "stormlight")
	}
	return filepath.Join(os.TempDir(), "stormlight")
}

// localVersion is this build's version, for the mismatch message. main
// sets it; "dev" until it does.
var localVersion = "dev"

// SetLocalVersion records the running binary's version so that a protocol
// mismatch can name both sides.
func SetLocalVersion(v string) { localVersion = v }
