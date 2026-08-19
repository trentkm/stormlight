package windrun

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trentkm/windrunner"
	"github.com/trentkm/windrunner/server"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/remote"
	"github.com/trentkm/stormlight/internal/session"
)

// The bridge's far side, when this test binary is standing in for a
// remote stormlight. It reads the socket from its environment because
// the fake ssh below cannot pass it any other way.
const (
	helperEnv       = "STORMLIGHT_BRIDGE_HELPER_SOCKET"
	helperSentinel  = "STORMLIGHT_BRIDGE_HELPER"
	helperBinEnv    = "STORMLIGHT_BRIDGE_HELPER_BIN"
	helperSocketDir = "/remote/state/windrunner"
)

// TestMain lets this binary play the remote host. `ssh <host> stormlight
// _wrbridge` runs a program over there that ensures a daemon and splices
// its stdio onto the socket; here the daemon is already running and the
// splice is the part under test.
func TestMain(m *testing.M) {
	// The far side's Stormlight, as the launch prelude invokes it: the
	// login shell execs `$STORMLIGHT_BIN _exec`, and on this harness
	// that binary is this one.
	if len(os.Args) > 1 && os.Args[1] == "_exec" {
		if err := remote.Exec(); err != nil {
			fmt.Fprintln(os.Stderr, "exec:", err)
			os.Exit(127)
		}
		os.Exit(0)
	}
	if os.Getenv(helperSentinel) != "" {
		hello := remote.Hello{
			Protocol:  remote.Protocol,
			Version:   "v-remote",
			Bin:       os.Getenv(helperBinEnv),
			SocketDir: helperSocketDir,
			Hostname:  "testhost",
		}
		if err := remote.Serve(os.Getenv(helperEnv), hello, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "bridge:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// testBinary is this test binary, which the harness casts as the remote
// host's Stormlight: the bridge reports it as $STORMLIGHT_BIN, and the
// launch prelude runs it as `_exec`.
func testBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return self
}

// remoteRuntime stands a daemon up, points a fake ssh at this binary's
// bridge mode, and returns a Runtime that believes it is talking to
// another machine — because everything it can observe says so.
func remoteRuntime(t *testing.T) *Runtime {
	t.Helper()
	// Not t.TempDir(): a unix socket path caps near 104 bytes and test
	// names push a per-test directory past it.
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	engine := windrunner.NewEngine()
	t.Cleanup(engine.Close)
	go server.Serve(engine, listener)
	t.Cleanup(func() { listener.Close() })

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	// A stand-in for ssh: it drops every option and destination, and runs
	// the "remote" program itself. Argv construction is asserted
	// separately, in the remote package.
	fakeSSH := filepath.Join(dir, "ssh")
	// A stand-in for ssh. It answers two kinds of request the way a real
	// host does: a script on stdin runs in a shell, and anything else is
	// the remote Stormlight — here, this binary in bridge mode.
	script := fmt.Sprintf(`#!/bin/sh
for last; do :; done
if [ "$last" = "-s" ]; then exec /bin/sh -s; fi
%s=1 %s=%q %s=%q exec %q -test.run=TestMainBridgeHelperNeverRuns
`, helperSentinel, helperEnv, socket, helperBinEnv, self, self)
	if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	runtime, err := NewRemoteRuntime(remote.Host{Name: "testhost", SSHProgram: fakeSSH})
	if err != nil {
		t.Fatalf("NewRemoteRuntime: %v", err)
	}
	return runtime
}

// withShell re-points a runtime's host at a configured login shell,
// which is the one thing about a host that changes how its providers are
// found and run.
func withShell(t *testing.T, runtime *Runtime, shell string) *Runtime {
	t.Helper()
	host := runtime.transport.Host()
	host.Shell = shell
	replaced, err := NewRemoteRuntime(host)
	if err != nil {
		t.Fatalf("NewRemoteRuntime: %v", err)
	}
	return replaced
}

// fakeLoginShell stands in for the shell a host is configured with — a
// fish, a Homebrew zsh, whatever the person there actually works in. It
// does what such a shell does that matters here: it answers to -lc, and
// it brings an environment of its own that the daemon did not have.
func fakeLoginShell(t *testing.T, providerDir string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "loginsh")
	script := "#!/bin/sh\n" +
		"PATH=" + providerDir + ":$PATH\n" +
		"LOGIN_SHELL_ENV=only-this-shell-has-it\n" +
		"export PATH LOGIN_SHELL_ENV\n" +
		// -lc <command>: the one calling convention every shell shares.
		"exec /bin/sh -c \"$2\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// providerOnly writes a provider that exists nowhere but the directory
// returned, so that finding it proves which PATH was searched.
func providerOnly(t *testing.T, name, body string) (dir, path string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "prov")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

// TestRemoteDispatchTellsTheAgentAboutItsOwnMachine is the whole point of
// the bridge. An agent's hooks find their way back through STORMLIGHT_BIN
// and WINDRUNNER_DIR, and on a remote host this machine's answers are
// wrong for both — so the values have to be the ones the bridge reported,
// and the environment underneath has to be the far side's, not a copy of
// this one shipped across.
func TestRemoteDispatchTellsTheAgentAboutItsOwnMachine(t *testing.T) {
	runtime := remoteRuntime(t)

	// A marker only the daemon's own environment has. In production that
	// is PATH and HOME; here it is something this test can be sure of.
	t.Setenv("STORMLIGHT_TEST_DAEMON_MARKER", "from-the-far-side")

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Name:     "over-the-bridge",
		Task:     "prove the environment",
		Cwd:      t.TempDir(),
		Launch: session.Launch{
			Path: "/bin/sh",
			// One per line: the terminal is 80 columns and a single
			// line of these would wrap through the middle of a value.
			Args: []string{"-c", `printf 'id=%s\nbin=%s\ndir=%s\nmarker=%s\nend\n' ` +
				`"$STORMLIGHT_ID" "$STORMLIGHT_BIN" "$WINDRUNNER_DIR" ` +
				`"$STORMLIGHT_TEST_DAEMON_MARKER"; sleep 60`},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	screen := waitForScreen(t, runtime, dispatched.ID, "end")
	// The terminal is 80 columns and these values are longer, so what it
	// printed is wrapped: the comparison is against the text, not the
	// layout.
	unwrapped := strings.NewReplacer("\r", "", "\n", "").Replace(screen)
	for _, want := range []string{
		"id=" + dispatched.ID,
		// The bridge's answers, not this machine's.
		"bin=" + testBinary(t),
		"dir=" + helperSocketDir,
		// The daemon's own environment came through underneath.
		"marker=from-the-far-side",
	} {
		if !strings.Contains(unwrapped, want) {
			t.Fatalf("agent environment missing %q:\n%s", want, screen)
		}
	}
}

// TestRemoteRosterAndTerminalCrossTheBridge: everything the dashboard
// does on every refresh — list the roster, read a screen, type into an
// agent — over the tunnel.
func TestRemoteRosterAndTerminalCrossTheBridge(t *testing.T) {
	runtime := remoteRuntime(t)

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Name:     "listener",
		Task:     "answer",
		Cwd:      t.TempDir(),
		Launch: session.Launch{
			Path: "/bin/sh",
			Args: []string{"-c", `printf 'ready\n'; read line; printf 'heard:%s\n' "$line"; sleep 60`},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	agents, err := runtime.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != dispatched.ID || agents[0].Name != "listener" {
		t.Fatalf("roster did not survive the tunnel: %+v", agents)
	}
	if !agents[0].ProcessLive {
		t.Fatalf("liveness is the daemon's fact and must cross: %+v", agents[0])
	}

	waitForScreen(t, runtime, dispatched.ID, "ready")

	// Input goes back the other way, over the control plane.
	if err := runtime.Send(context.Background(), dispatched.ID, "over the bridge"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForScreen(t, runtime, dispatched.ID, "heard:over the bridge")

	// An attachment is its own connection through the tunnel, and its
	// seed is the exact state rather than a re-render.
	stream, err := runtime.AttachTerminal(context.Background(), dispatched.ID, 80, 24)
	if err != nil {
		t.Fatalf("AttachTerminal: %v", err)
	}
	defer stream.Close()
	if !strings.Contains(string(stream.Seed().Resync), "heard:over the bridge") {
		t.Fatalf("attachment seed lost the story:\n%s", stream.Seed().Resync)
	}

	if err := runtime.Delete(context.Background(), dispatched.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if agents, _ := runtime.ListAgents(context.Background()); len(agents) != 0 {
		t.Fatalf("agent survived deletion: %+v", agents)
	}
}

// TestRemoteAttachRunsWhereTheDaemonIs: `F` suspends the dashboard and
// runs a command in the user's own terminal. For a remote agent that
// command has to be an ssh into the host, carrying the socket directory
// explicitly — a tty session runs a login shell where a bridge does not,
// and the two can disagree about where XDG state lives.
func TestRemoteAttachRunsWhereTheDaemonIs(t *testing.T) {
	runtime := remoteRuntime(t)
	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("claude"),
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 60"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	result, err := runtime.Attach(context.Background(), dispatched.ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	argv := strings.Join(result.Command.Args, " ")
	if !strings.Contains(argv, "-t") {
		t.Fatalf("an interactive attachment needs a tty: %v", result.Command.Args)
	}
	if !strings.Contains(argv, "_wrattach") {
		t.Fatalf("attachment command lost its subcommand: %v", result.Command.Args)
	}
	if !strings.Contains(argv, "WINDRUNNER_DIR="+shellQuoted(helperSocketDir)) {
		t.Fatalf("the far side's socket directory must be explicit: %v", result.Command.Args)
	}
}

func shellQuoted(value string) string { return "'" + value + "'" }

func waitForScreen(t *testing.T, runtime *Runtime, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		screen, err := runtime.Capture(context.Background(), id, 200)
		if err == nil && strings.Contains(screen, want) {
			return screen
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; last screen:\n%s", want, screen)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestARemoteOverlayCanReachTheDaemonHoldingIt: an overlay program leaves
// its answer in its own session, which means finding the daemon on the
// machine it is running on. WINDRUNNER_SESSION names the session; without
// WINDRUNNER_DIR there is nothing to say it to.
func TestARemoteOverlayCanReachTheDaemonHoldingIt(t *testing.T) {
	runtime := remoteRuntime(t)
	overlay, err := runtime.StartOverlay(context.Background(), session.OverlayRequest{
		Path: "/bin/sh",
		Args: []string{"-c", `printf 'dir=%s\nsession=%s\nend\n' ` +
			`"$WINDRUNNER_DIR" "$WINDRUNNER_SESSION"; sleep 60`},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("StartOverlay: %v", err)
	}
	defer overlay.Close()

	screen := drainOverlay(t, overlay, "end")
	if !strings.Contains(screen, "dir="+helperSocketDir) {
		t.Fatalf("the overlay was not told which daemon holds it:\n%s", screen)
	}
	if !strings.Contains(screen, "session=") ||
		strings.Contains(screen, "session=\n") {
		t.Fatalf("the overlay was not told its own session:\n%s", screen)
	}
}

// TestAnOverlayAnswersThroughItsSession: the channel the picker uses. The
// program writes into the session's metadata on its own machine, and the
// dashboard reads it from wherever it is.
func TestAnOverlayAnswersThroughItsSession(t *testing.T) {
	runtime := remoteRuntime(t)
	overlay, err := runtime.StartOverlay(context.Background(), session.OverlayRequest{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 60"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("StartOverlay: %v", err)
	}
	defer overlay.Close()

	if answer, err := overlay.Result(context.Background()); err != nil || answer != "" {
		t.Fatalf("an overlay that has answered nothing = %q, %v", answer, err)
	}

	// Stand in for the program: write the answer into its session, the
	// way `stormlight _pick` does from inside.
	answerTheOverlay(t, runtime, "/srv/api")

	answer, err := overlay.Result(context.Background())
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if answer != "/srv/api" {
		t.Fatalf("answer = %q", answer)
	}
}

// answerTheOverlay finds the overlay session and writes a result into it,
// which is what the program inside does through the daemon on its own
// machine.
func answerTheOverlay(t *testing.T, runtime *Runtime, answer string) {
	t.Helper()
	sessions, err := runtime.client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, info := range sessions {
		if _, ours := info.Metadata[overlayMetadataKey]; !ours {
			continue
		}
		metadata := info.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata[OverlayResultKey] = answer
		if err := runtime.client.SetMetadata(info.ID, metadata); err != nil {
			t.Fatalf("SetMetadata: %v", err)
		}
		return
	}
	t.Fatal("no overlay session to answer")
}

func drainOverlay(t *testing.T, overlay session.Overlay, want string) string {
	t.Helper()
	collected := string(overlay.Seed().Resync)
	deadline := time.After(10 * time.Second)
	for !strings.Contains(collected, want) {
		select {
		case message, ok := <-overlay.Output():
			if !ok {
				t.Fatalf("overlay stream closed before %q; got:\n%s", want, collected)
			}
			if message.Resync != nil {
				// State replaces the replica, here as everywhere.
				collected = string(message.Resync)
				continue
			}
			collected += string(message.Bytes)
		case <-deadline:
			t.Fatalf("timed out waiting for %q; got:\n%s", want, collected)
		}
	}
	return collected
}

// TestTheProviderIsResolvedOnTheMachineThatRunsIt: the dashboard's own
// path for a provider is a fact about the dashboard's machine.
// /Users/someone/.toolbox/bin/codex handed to a Linux daemon can only
// come back as no such file — which is exactly what it did.
func TestTheProviderIsResolvedOnTheMachineThatRunsIt(t *testing.T) {
	runtime := remoteRuntime(t)

	// A provider that exists on the far side under a path this machine
	// has never had, dispatched with a local path that is nonsense there.
	// The name is its own: a provider anyone might actually have
	// installed would make this pass or fail on the developer's machine
	// rather than on the code.
	remoteOnly := filepath.Join(t.TempDir(), "codex-under-test")
	if err := os.WriteFile(remoteOnly, []byte(
		"#!/bin/sh\nprintf 'ran %s\\nend\\n' \"$0\"; sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(remoteOnly)+":"+os.Getenv("PATH"))

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch: session.Launch{
			Path:    "/Users/someone/.toolbox/bin/codex-under-test",
			Program: "codex-under-test",
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	screen := waitForScreen(t, runtime, dispatched.ID, "end")
	// The terminal is 80 columns and a temporary path is longer, so the
	// line it printed is wrapped: the comparison is against the text, not
	// the layout.
	unwrapped := strings.NewReplacer("\r", "", "\n", "").Replace(screen)
	if !strings.Contains(unwrapped, "ran "+remoteOnly) {
		t.Fatalf("the far side's own copy should have run:\n%s", screen)
	}
	// And nothing from this machine's filesystem was sent.
	if strings.Contains(unwrapped, "/Users/someone") {
		t.Fatalf("a dashboard path reached the remote spawn:\n%s", screen)
	}
}

// TestAProviderMissingThereSaysSo: naming the host and the provider,
// rather than a path from the machine that is not running it.
func TestAProviderMissingThereSaysSo(t *testing.T) {
	runtime := remoteRuntime(t)
	_, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("nowhere"),
		Cwd:      t.TempDir(),
		Launch: session.Launch{
			Path:    "/Users/someone/.toolbox/bin/nowhere-provider",
			Program: "nowhere-provider-that-is-not-installed",
		},
	})
	if err == nil {
		t.Fatal("a provider the host does not have must not dispatch")
	}
	if !strings.Contains(err.Error(), "nowhere-provider-that-is-not-installed") ||
		!strings.Contains(err.Error(), "testhost") {
		t.Fatalf("the error should name the provider and the host: %v", err)
	}
	if strings.Contains(err.Error(), "/Users/someone") {
		t.Fatalf("it should not name a path from this machine: %v", err)
	}
}

// TestLocalDispatchStillUsesTheResolvedPath: nothing about this changes
// the machine the dashboard is on, where the path was already right.
func TestLocalDispatchStillUsesTheResolvedPath(t *testing.T) {
	local := &Runtime{}
	command, args, err := local.launchCommand(context.Background(), session.Launch{
		Path: "/usr/local/bin/codex", Program: "codex", Args: []string{"--go"},
	})
	if err != nil || command != "/usr/local/bin/codex" {
		t.Fatalf("launchCommand = %q, %v", command, err)
	}
	// No shell in the middle either: the dashboard's own machine is
	// already the environment the provider wants.
	if len(args) != 1 || args[0] != "--go" {
		t.Fatalf("local args should be the provider's own: %q", args)
	}
}

// TestAConfiguredShellFindsAndRunsTheProvider is the whole of #185 in
// one test.
//
// The host's account shell knows nothing about the provider; the shell
// the person there actually works in has it on PATH. Both halves have to
// follow from naming that shell: it is what the lookup asks, so the
// provider is found, and it is what the launch goes through, so the
// provider runs with that shell's environment rather than the daemon's.
func TestAConfiguredShellFindsAndRunsTheProvider(t *testing.T) {
	dir, _ := providerOnly(t, "codex", "#!/bin/sh\n"+
		`printf 'env=%s\nend\n' "$LOGIN_SHELL_ENV"; sleep 60`+"\n")
	runtime := withShell(t, remoteRuntime(t), fakeLoginShell(t, dir))

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		// A path from the dashboard's own machine, which is what the
		// dashboard has and what the host has never had.
		Launch: session.Launch{Path: "/Users/someone/.toolbox/bin/codex", Program: "codex"},
	})
	if err != nil {
		t.Fatalf("a provider on the configured shell's PATH must dispatch: %v", err)
	}
	screen := waitForScreen(t, runtime, dispatched.ID, "end")
	// Resolving it was never the whole job: a provider found on a login
	// shell's PATH is regularly a shim that needs the rest of that
	// shell's environment to work.
	if !strings.Contains(screen, "env=only-this-shell-has-it") {
		t.Fatalf("the provider should have the configured shell's environment:\n%s", screen)
	}
}

// TestProviderArgumentsAreNotShellText: the launch goes through a shell,
// and a task is arbitrary text someone typed. If the two ever meet, a
// quote in a task becomes syntax on a machine whose shell this one does
// not choose. They must not meet.
func TestProviderArgumentsAreNotShellText(t *testing.T) {
	// Echoes each argument on its own line, bracketed, so that a lost
	// one, a split one, and a mangled one all look different.
	dir, _ := providerOnly(t, "codex", "#!/bin/sh\n"+
		`for a in "$@"; do printf '[%s]\n' "$a"; done; printf 'end\n'; sleep 60`+"\n")
	runtime := withShell(t, remoteRuntime(t), fakeLoginShell(t, dir))

	hostile := []string{
		"a task with spaces",
		`it's got a quote`,
		`"double" and $HOME and ${BRACED}`,
		"semi; colon && amp | pipe",
		"back`tick` and $(command sub)",
		`back\slash`,
		"star * and glob ?",
		"new\nline",
	}
	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Program: "codex", Args: hostile},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	screen := waitForScreen(t, runtime, dispatched.ID, "end")
	// The terminal wraps at 80 columns, and a newline inside an argument
	// is a real line break in the output — so the comparison is against
	// the run of characters, not the lines.
	flat := strings.NewReplacer("\r", "", "\n", "").Replace(screen)
	for _, arg := range hostile {
		want := "[" + strings.ReplaceAll(arg, "\n", "") + "]"
		if !strings.Contains(flat, want) {
			t.Fatalf("argument %q did not arrive intact (wanted %q):\n%s", arg, want, screen)
		}
	}
}

// TestAShellThatIsNotThereSaysWhichSetting: with a bad shell configured,
// every provider on the host looks missing. That points at the wrong
// thing entirely, so the shell is named as the fault instead.
func TestAShellThatIsNotThereSaysWhichSetting(t *testing.T) {
	runtime := withShell(t, remoteRuntime(t), "/opt/nothing/here/fish")
	_, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Program: "codex"},
	})
	if err == nil {
		t.Fatal("a configured shell that is not there must not dispatch")
	}
	for _, want := range []string{"/opt/nothing/here/fish", "shell", "testhost"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error should name %q: %v", want, err)
		}
	}
}

// TestWithNoConfiguredShellTheAccountsOwnIsUsed: naming a shell is how a
// host differs from its default, not a thing every host must do.
func TestWithNoConfiguredShellTheAccountsOwnIsUsed(t *testing.T) {
	dir, _ := providerOnly(t, "codex", "#!/bin/sh\n"+
		`printf 'env=%s\nend\n' "$LOGIN_SHELL_ENV"; sleep 60`+"\n")
	// $SHELL is what an unconfigured host falls back to, and here it is
	// the same stand-in — reached by the other route.
	t.Setenv("SHELL", fakeLoginShell(t, dir))

	runtime := remoteRuntime(t)
	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Program: "codex"},
	})
	if err != nil {
		t.Fatalf("the account's own shell should still be used: %v", err)
	}
	screen := waitForScreen(t, runtime, dispatched.ID, "end")
	if !strings.Contains(screen, "env=only-this-shell-has-it") {
		t.Fatalf("the account's shell should have supplied the environment:\n%s", screen)
	}
}

// TestAMissingProviderPointsAtTheSetting: the message someone gets when
// their provider is installed and Stormlight says it is not. It has to
// carry the way out, because nothing about the machine reveals that the
// shell it was asked about is not the shell its owner works in.
func TestAMissingProviderPointsAtTheSetting(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	runtime := remoteRuntime(t)
	_, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch:   session.Launch{Program: "a-provider-no-machine-has"},
	})
	if err == nil {
		t.Fatal("a provider nothing has must not dispatch")
	}
	t.Logf("the message is:\n%s", err)
	for _, want := range []string{
		"a-provider-no-machine-has", // what
		"testhost",                  // where
		"/bin/zsh",                  // what was asked
		"shell =",                   // and what to do about it
		"[hosts.testhost]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the message should carry %q:\n%s", want, err)
		}
	}
}
