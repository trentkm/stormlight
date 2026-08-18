package remote

import (
	"encoding/json"
	"net"
	"os/exec"
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
