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
`, helperSentinel, helperEnv, socket, helperBinEnv, "/remote/bin/stormlight", self)
	if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	runtime, err := NewRemoteRuntime(remote.Host{Name: "testhost", SSHProgram: fakeSSH})
	if err != nil {
		t.Fatalf("NewRemoteRuntime: %v", err)
	}
	return runtime
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
	for _, want := range []string{
		"id=" + dispatched.ID,
		// The bridge's answers, not this machine's.
		"bin=/remote/bin/stormlight",
		"dir=" + helperSocketDir,
		// The daemon's own environment came through underneath.
		"marker=from-the-far-side",
	} {
		if !strings.Contains(screen, want) {
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
	remoteOnly := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(remoteOnly, []byte(
		"#!/bin/sh\nprintf 'ran %s\\nend\\n' \"$0\"; sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(remoteOnly)+":"+os.Getenv("PATH"))

	dispatched, err := runtime.Dispatch(context.Background(), session.DispatchRequest{
		Provider: agent.Provider("codex"),
		Cwd:      t.TempDir(),
		Launch: session.Launch{
			Path:    "/Users/someone/.toolbox/bin/codex",
			Program: "codex",
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
	path, err := local.providerPath(context.Background(), session.Launch{
		Path: "/usr/local/bin/codex", Program: "codex",
	})
	if err != nil || path != "/usr/local/bin/codex" {
		t.Fatalf("providerPath = %q, %v", path, err)
	}
}
