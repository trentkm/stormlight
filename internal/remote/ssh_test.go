package remote

import (
	"encoding/json"
	"net"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSSHArgsCarryTheDashboardsConstraints(t *testing.T) {
	transport := NewTransport(Host{Name: "devbox"})
	args := transport.sshArgs(false, nil, []string{"_wrbridge"})
	joined := strings.Join(args, " ")

	// The dashboard owns the terminal; ssh must never try to prompt into
	// it. This one is a correctness constraint, not a preference.
	if !strings.Contains(joined, "-o BatchMode=yes") {
		t.Fatalf("BatchMode missing: %v", args)
	}
	// A client opens one connection for control, one for events, and one
	// per attached terminal. Without sharing that is an authentication
	// apiece.
	if !strings.Contains(joined, "ControlMaster=auto") {
		t.Fatalf("connection sharing missing: %v", args)
	}
	// The destination has to precede the remote command, or ssh reads the
	// command as the destination.
	destination, command := indexOf(args, "devbox"), indexOf(args, "'stormlight' '_wrbridge'")
	if destination < 0 || command < 0 || destination > command {
		t.Fatalf("destination must come before the command: %v", args)
	}
	if strings.Contains(joined, " -t ") {
		t.Fatalf("a bridge needs no tty: %v", args)
	}
	if tty := NewTransport(Host{Name: "devbox"}).sshArgs(true, nil, []string{"_wrattach", "abc"}); indexOf(tty, "-t") < 0 {
		t.Fatalf("an interactive attachment needs a tty: %v", tty)
	}
}

func TestSSHArgsRespectTheHost(t *testing.T) {
	transport := NewTransport(Host{
		Name:        "devbox",
		Destination: "trent@10.0.0.4",
		Bin:         "/opt/stormlight/bin/stormlight",
		Options:     []string{"-p", "2222"},
		NoMultiplex: true,
	})
	args := transport.sshArgs(false, nil, []string{"_wrbridge"})
	joined := strings.Join(args, " ")

	if indexOf(args, "trent@10.0.0.4") < 0 {
		t.Fatalf("destination overrides the name: %v", args)
	}
	if indexOf(args, "devbox") >= 0 {
		t.Fatalf("the display name is not an ssh destination: %v", args)
	}
	if !strings.Contains(joined, "'/opt/stormlight/bin/stormlight' '_wrbridge'") {
		t.Fatalf("explicit binary path missing: %v", args)
	}
	if strings.Contains(joined, "ControlMaster") {
		t.Fatalf("NoMultiplex should suppress sharing: %v", args)
	}
	if port := indexOf(args, "2222"); port < 0 || port > indexOf(args, "trent@10.0.0.4") {
		t.Fatalf("host options belong ahead of the destination: %v", args)
	}
}

// TestRemoteCommandSurvivesTheFarShell: ssh does not take an argv, it
// takes a string that the far side's shell re-parses. A workspace path
// with a space in it is the ordinary case that breaks without quoting.
func TestRemoteCommandSurvivesTheFarShell(t *testing.T) {
	transport := NewTransport(Host{Name: "devbox"})
	args := transport.sshArgs(false,
		[]string{"WINDRUNNER_DIR=/home/me/state dir/windrunner"},
		[]string{"_resolve", "/src/my project"})
	command := args[len(args)-1]

	// Reparse it the way the far shell does, and echo the argv it built.
	out, err := exec.Command("/bin/sh", "-c",
		"eval 'set -- '"+shellQuote([]string{command})+`; printf '[%s]' "$@"`).Output()
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	got := string(out)
	// The assignment prefix is consumed by the shell as an assignment, so
	// what remains as argv is the command and its arguments.
	for _, want := range []string{"[stormlight]", "[_resolve]", "[/src/my project]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("argv came apart: %s (from %s)", got, command)
		}
	}
	if !strings.Contains(command, `WINDRUNNER_DIR='/home/me/state dir/windrunner'`) {
		t.Fatalf("assignment prefix must quote the value, not the name: %s", command)
	}
}

func TestShellQuoteSurvivesQuotes(t *testing.T) {
	quoted := shellQuote([]string{`it's`})
	out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted).Output()
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if string(out) != `it's` {
		t.Fatalf("got %q", out)
	}
}

// TestHandshakeLeavesTheProtocolAlone is the one that matters most for
// the splice: the greeting is read from the same stream the wire protocol
// arrives on, and a reader that buffered ahead would eat the first frame
// and leave the connection looking healthy and mute.
func TestHandshakeLeavesTheProtocolAlone(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	go func() {
		encoded, _ := json.Marshal(Hello{Protocol: Protocol, Version: "v9.9.9", Bin: "/usr/bin/stormlight"})
		far.Write(append(encoded, '\n'))
		far.Write([]byte("the first protocol frame"))
	}()

	hello, err := Handshake(near)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if hello.Bin != "/usr/bin/stormlight" {
		t.Fatalf("greeting lost: %+v", hello)
	}
	near.SetReadDeadline(time.Now().Add(5 * time.Second))
	rest := make([]byte, len("the first protocol frame"))
	if _, err := readFull(near, rest); err != nil {
		t.Fatalf("reading what followed: %v", err)
	}
	if string(rest) != "the first protocol frame" {
		t.Fatalf("the handshake ate the protocol: %q", rest)
	}
}

func TestHandshakeRefusesAnotherProtocol(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	go func() {
		encoded, _ := json.Marshal(Hello{Protocol: Protocol + 1, Version: "v9.9.9"})
		far.Write(append(encoded, '\n'))
	}()

	_, err := Handshake(near)
	if err == nil {
		t.Fatal("a mismatched protocol must not connect")
	}
	// The message has to name both sides: the whole failure is that two
	// machines disagree, and only one of them is in front of the user.
	for _, want := range []string{"v9.9.9", localVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should name both versions, got: %v", err)
		}
	}
}

// TestHandshakeReportsWhatAnsweredInstead: the common first-run failures
// are a login banner, a motd, or a shell saying the binary is not there.
// None of them are JSON, and "invalid character 'b'" would send the user
// looking in the wrong place.
func TestHandshakeReportsWhatAnsweredInstead(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	go func() {
		far.Write([]byte("bash: stormlight: command not found\n"))
		far.Close()
	}()

	_, err := Handshake(near)
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Fatalf("want the far side's own complaint, got: %v", err)
	}
}

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func readFull(conn net.Conn, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		n, err := conn.Read(buffer[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// TestTheProvidersArgvIsNeverShellText: the launch goes through a shell
// this machine did not choose, and a task is arbitrary text someone
// typed. The two never meet — the argv travels as an environment value,
// which no shell parses — and this is the round trip that says so.
func TestTheProvidersArgvIsNeverShellText(t *testing.T) {
	argv := []string{
		"codex",
		"a task with spaces",
		`it's got a quote`,
		`"double" and $HOME and ${BRACED}`,
		"semi; colon && amp | pipe",
		"back`tick` and $(command sub)",
		`back\slash`,
		"star * and glob ?",
		"new\nline",
		"", // an empty argument is still an argument
	}
	entry, err := ExecEnvFor(argv)
	if err != nil {
		t.Fatalf("ExecEnvFor: %v", err)
	}
	name, value, ok := strings.Cut(entry, "=")
	if !ok || name != ExecEnv {
		t.Fatalf("ExecEnvFor should produce a %s entry: %q", ExecEnv, entry)
	}
	// An environment value cannot carry a NUL, and nothing else needs
	// escaping on the way — but a newline inside one would end the
	// wrong thing if this were ever a line-based format.
	if strings.ContainsRune(value, 0) {
		t.Fatal("an environment value cannot contain NUL")
	}
	back, err := ExecArgv(value)
	if err != nil {
		t.Fatalf("ExecArgv: %v", err)
	}
	if !slices.Equal(back, argv) {
		t.Fatalf("argv did not survive the round trip:\n got %q\nwant %q", back, argv)
	}
}

// TestNothingToRunIsAnError: an empty or malformed handoff must not be
// read as "run something plausible".
func TestNothingToRunIsAnError(t *testing.T) {
	for _, value := range []string{"", "[]", "not json", "{}", `"a string"`} {
		if _, err := ExecArgv(value); err == nil {
			t.Fatalf("ExecArgv(%q) should have failed", value)
		}
	}
}

// TestTheExecPreludeIsShellAgnostic: the prelude is POSIX and runs under
// /bin/sh, but the one line it asks another shell to run is text that
// sh, bash, zsh, ksh, and fish all read the same way. Anything more
// expressive would work on the author's machine and not on a host whose
// login shell is fish.
func TestTheExecPreludeIsShellAgnostic(t *testing.T) {
	handoff := ""
	for _, line := range strings.Split(ExecPrelude, "\n") {
		if strings.HasPrefix(line, "exec \"$shell\" -lc ") {
			handoff = strings.TrimPrefix(line, "exec \"$shell\" -lc ")
		}
	}
	if handoff == "" {
		t.Fatalf("the prelude should hand off to the host's shell:\n%s", ExecPrelude)
	}
	// Only "$VAR" and plain words: no command substitution, no
	// arithmetic, no arrays, no $0/$@ — the places the shells differ.
	for _, forbidden := range []string{"$(", "`", "${", "$@", "$*", "$0", "[[", "=("} {
		if strings.Contains(handoff, forbidden) {
			t.Fatalf("the handoff uses %q, which is not the same in every shell: %s",
				forbidden, handoff)
		}
	}
	// And the argv is not in it, which is the point.
	if strings.Contains(ExecPrelude, ExecEnv) {
		t.Fatalf("the argv must not be shell text:\n%s", ExecPrelude)
	}
}

// TestAConfiguredShellReachesTheScripts: the host's setting has to arrive
// as an assignment the script can read, quoted, because it is a path
// someone typed.
func TestAConfiguredShellReachesTheScripts(t *testing.T) {
	plain := Host{Name: "h"}
	if got := plain.shellAssignment(); got != "" {
		t.Fatalf("an unconfigured host should say nothing about a shell: %q", got)
	}
	if got := plain.shellDescription(""); got != "its login shell" {
		t.Fatalf("shellDescription = %q", got)
	}
	// When the host said which shell it asked, the message names it
	// rather than describing it.
	if got := plain.shellDescription("/bin/zsh"); got != "/bin/zsh" {
		t.Fatalf("shellDescription = %q", got)
	}
	if !strings.Contains(plain.shellHint(), "[hosts.h]") {
		t.Fatalf("an unconfigured host should be told the setting exists: %q", plain.shellHint())
	}
	configured := Host{Name: "h", Shell: "/home/linuxbrew/.linuxbrew/bin/fish"}
	want := ShellEnv + "='/home/linuxbrew/.linuxbrew/bin/fish'\n"
	if got := configured.shellAssignment(); got != want {
		t.Fatalf("shellAssignment = %q, want %q", got, want)
	}
	// A configured shell wins over whatever the host reported, and needs
	// no hint: it has been told, so the answer is that the provider
	// really is not on that shell's PATH.
	if got := configured.shellDescription("/bin/zsh"); got != configured.Shell {
		t.Fatalf("shellDescription = %q", got)
	}
	if got := configured.shellHint(); got != "" {
		t.Fatalf("a configured host needs no hint: %q", got)
	}
	// A path with a quote in it is a path, not an injection.
	odd := Host{Name: "h", Shell: `/opt/it's here/fish`}
	if !strings.Contains(odd.shellAssignment(), `'\''`) {
		t.Fatalf("a quote in the path should be quoted: %q", odd.shellAssignment())
	}
}
