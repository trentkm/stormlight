// Package windrun hosts agents on windrunner sessions: PTYs owned by a
// small daemon, with an authoritative terminal emulator per agent. Each
// agent's terminal is a persisted tab — the daemon outlives every dashboard, snapshots are
// exact, and attach is a byte stream, not a capture.
//
// Agent identity and state ride in the session's opaque metadata as one
// JSON document; the daemon never learns what an agent is, which is the
// windrunner library's whole boundary.
package windrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/remote"
	"github.com/trentkm/stormlight/internal/selfpath"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// metadataKey holds the serialized agent.Agent in a session's metadata.
const metadataKey = "stormlight_agent"

// agentEnviron is this machine's environment as an agent should receive
// it: os.Environ() with two classes of entry removed, then the caller's
// own on the end.
//
// The first class is another Claude session's identity. The daemon is
// auto-started from whatever shell first needed it — including a Claude
// Code session's own tool shell — and the ambient CLAUDE*/ANTHROPIC_*
// variables would wire every agent into that session: child-session mode,
// its messaging socket and token, its session id. An agent is nobody's
// child.
//
// The second is anything the caller is about to set. Appending onto an
// environment that already names the variable leaves two entries for one
// name, and POSIX does not say which a child reads — a shell here
// resolved the last, another libc may resolve the first. Dispatch would
// hand an agent its parent's STORMLIGHT_ID and the hooks would report the
// wrong agent, on some platforms only. One entry per name is the only
// portable answer.
func agentEnviron(overrides []string) []string {
	setting := make(map[string]bool, len(overrides))
	for _, entry := range overrides {
		if name, _, ok := strings.Cut(entry, "="); ok {
			setting[name] = true
		}
	}
	environ := os.Environ()
	kept := environ[:0]
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "CLAUDE") || strings.HasPrefix(name, "ANTHROPIC_") {
			continue
		}
		if setting[name] {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, overrides...)
}

// sendSubmitDelay separates pasted text from the Enter that submits it:
// providers need a beat to register the paste before the submit keypress.
const sendSubmitDelay = 150 * time.Millisecond

type Runtime struct {
	client *client.Client
	// transport is the SSH tunnel to another machine's daemon, and nil
	// for the daemon on this one. It is the only thing that differs: the
	// wire protocol, the agent document, and every operation below are
	// the same either way. What it changes is the handful of places that
	// were quietly speaking about this machine — where the binary is,
	// which socket directory a hook should reach, whose environment an
	// agent inherits, and what "attach in your own terminal" runs.
	transport *remote.Transport
}

// Remote reports the host this runtime's daemon runs on, or the zero
// value when it is this machine.
func (r *Runtime) Remote() (remote.Host, bool) {
	if r.transport == nil {
		return remote.Host{}, false
	}
	return r.transport.Host(), true
}

// SocketDir is where the daemon's socket lives: WINDRUNNER_DIR when set
// (isolation for tests), the windrunner library's default location
// otherwise — so `windrunner ls` shows Stormlight's agents too.
func SocketDir() string {
	if dir := os.Getenv("WINDRUNNER_DIR"); dir != "" {
		return dir
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "windrunner")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "windrunner")
	}
	return filepath.Join(os.TempDir(), "windrunner")
}

// SocketPath is the daemon socket inside SocketDir.
func SocketPath() string {
	dir := SocketDir()
	os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "daemon.sock")
}

// NewRuntime connects to the windrunner daemon, starting one if nothing
// answers. The daemon is this same binary (`stormlight _wrdaemon`), so
// nothing else needs installing.
func NewRuntime() (*Runtime, error) {
	binPath, err := selfpath.Resolve()
	if err != nil {
		return nil, err
	}
	c, err := client.EnsureDaemon(SocketPath(), []string{binPath, "_wrdaemon"}, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reach windrunner daemon: %w", err)
	}
	return &Runtime{client: c}, nil
}

// NewRemoteRuntime connects to the daemon on another machine, over SSH.
// The daemon runs where the agents run — that is what makes them outlive
// this dashboard, this terminal, and this laptop's next lid close.
//
// There is no EnsureDaemon here: starting one is the bridge's job,
// because it is the only part of Stormlight standing on that host.
func NewRemoteRuntime(host remote.Host) (*Runtime, error) {
	transport := remote.NewTransport(host)
	c, err := client.DialWith(transport.Dial)
	if err != nil {
		return nil, err
	}
	return &Runtime{client: c, transport: transport}, nil
}

// dispatchTarget answers the two questions a spawn asks about the machine
// it is spawning on: where Stormlight is, and which socket directory its
// daemon listens in. A hook subprocess needs both to report anything, and
// on a remote host the local answers are simply wrong.
func (r *Runtime) dispatchTarget() (binPath, socketDir string, err error) {
	if r.transport == nil {
		binPath, err = selfpath.Resolve()
		return binPath, SocketDir(), err
	}
	hello := r.transport.Hello()
	if hello.Bin == "" {
		return "", "", fmt.Errorf("%s has not reported its stormlight", r.transport.Host().Name)
	}
	return hello.Bin, hello.SocketDir, nil
}

func (r *Runtime) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	sessions, err := r.client.List()
	if err != nil {
		return nil, err
	}
	var agents []agent.Agent
	for _, info := range sessions {
		managedAgent, ok := decodeAgent(info)
		if !ok {
			// Not ours; the daemon may host other products' sessions.
			continue
		}
		agents = append(agents, managedAgent)
	}
	return agents, nil
}

func decodeAgent(info wire.SessionInfo) (agent.Agent, bool) {
	raw, ok := info.Metadata[metadataKey]
	if !ok {
		return agent.Agent{}, false
	}
	var managedAgent agent.Agent
	if err := json.Unmarshal([]byte(raw), &managedAgent); err != nil {
		return agent.Agent{}, false
	}
	// Liveness and exit are the daemon's facts, never stale metadata.
	managedAgent.ProcessLive = info.Alive
	if !info.Alive {
		code := info.ExitCode
		managedAgent.ExitCode = &code
		if managedAgent.Activity != agent.ActivityCompleted &&
			managedAgent.Activity != agent.ActivityFailed {
			if code == 0 {
				managedAgent.Activity = agent.ActivityCompleted
			} else {
				managedAgent.Activity = agent.ActivityFailed
			}
		}
	}
	managedAgent.PaneTitle = info.Title
	// The session id doubles as the pane/window identity for display
	// paths that expect one.
	managedAgent.PaneID = info.ID
	managedAgent.WindowID = info.ID
	return managedAgent, true
}

func (r *Runtime) Dispatch(ctx context.Context, request session.DispatchRequest) (agent.Agent, error) {
	id, err := newID()
	if err != nil {
		return agent.Agent{}, err
	}
	binPath, socketDir, err := r.dispatchTarget()
	if err != nil {
		return agent.Agent{}, err
	}
	managedAgent := agent.Agent{
		ID:          id,
		Provider:    request.Provider,
		Name:        request.Name,
		Task:        request.Task,
		Cwd:         request.Cwd,
		CreatedAt:   time.Now(),
		Activity:    agent.ActivityWorking,
		ProcessLive: true,
		Mode:        request.Mode,
		Command:     request.Launch.Path,
		Workspace:   request.Workspace,
	}
	encoded, err := json.Marshal(managedAgent)
	if err != nil {
		return agent.Agent{}, fmt.Errorf("encode agent metadata: %w", err)
	}
	// The agent's own environment is how its hook subprocesses find their
	// way back: the id names the session, the binary answers
	// $STORMLIGHT_BIN, and the socket dir rebuilds this same service
	// inside `stormlight _provider-event`. All three describe the machine
	// the agent runs on, which is not always this one.
	overrides := []string{
		"STORMLIGHT_ID=" + managedAgent.ID,
		"STORMLIGHT_BIN=" + binPath,
		"WINDRUNNER_DIR=" + socketDir,
		// The terminal the agent runs in is the daemon's emulator, not
		// whatever terminal the daemon was started from — and the daemon
		// is auto-started from shells, scripts, and hooks, so the
		// inherited TERM is junk as often as not. Under an impoverished
		// TERM, TUIs drop styling: Claude Code renders its input-box
		// prompt suggestions without the faint attribute, which makes
		// hint text indistinguishable from typed input. Name the
		// emulator's real capabilities.
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		// Claude Code's input-box prompt suggestions render without their
		// faint attribute under this emulator, which makes hint text
		// pixel-identical to typed input — a day was lost to that ghost.
		// Off by default for hosted agents; harmless to providers that
		// don't read it.
		"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false",
		// Claude Code's terminal-title machinery misbehaves under this
		// emulator: after a turn, the conversation title lands in the
		// input box as real buffer content (verified byte-for-byte — no
		// stdin write carries it; CC inserts it itself). The title has no
		// reader here anyway — the window bar carries agent identity —
		// so the machinery is switched off rather than chased.
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1",
	}
	// The provider runs where it lives, which for another machine means
	// neither the path nor the environment this one has. Launch.Path
	// answers "where is it here", and a macOS toolbox path handed to a
	// Linux daemon can only come back as no such file.
	command, args, err := r.launchCommand(ctx, request.Launch)
	if err != nil {
		return agent.Agent{}, err
	}
	if r.transport != nil {
		argv, err := remote.ExecEnvFor(
			append([]string{providerName(request.Launch)}, request.Launch.Args...))
		if err != nil {
			return agent.Agent{}, err
		}
		overrides = append(overrides, argv)
		if shell := r.transport.Host().Shell; shell != "" {
			overrides = append(overrides, remote.ShellEnv+"="+shell)
		}
	}
	spawn := wire.Request{
		Command: command,
		Args:    args,
		Dir:     request.Cwd,
		// Peer input is how the dashboard and CLI speak to an agent
		// without an attachment — Send, Interrupt, `stormlight send` —
		// and how agents will speak to each other over the daemon's
		// control plane.
		Peer:     true,
		Cols:     80,
		Rows:     24,
		Metadata: map[string]string{metadataKey: string(encoded)},
	}
	if r.transport == nil {
		spawn.Env = agentEnviron(overrides)
	} else {
		// This machine's environment describes this machine: its PATH,
		// its home directory, its secrets. None of it belongs on another
		// host, and sending it would leave the agent without the PATH
		// that finds its own provider. The daemon over there supplies the
		// environment; these few entries go on top.
		spawn.EnvOverride = overrides
	}
	info, err := r.client.Spawn(spawn)
	if err != nil {
		return agent.Agent{}, fmt.Errorf("start provider: %w", err)
	}
	// The daemon named the session; adopt its id as the agent's id would
	// invite two names for one thing. Instead the metadata carries the
	// stormlight id, and the session id lives in the pane fields.
	managedAgent.PaneID = info.ID
	managedAgent.WindowID = info.ID
	return managedAgent, nil
}

// launchCommand is what the daemon on the far side is asked to start.
//
// Locally that is the provider, at the path already resolved here.
// Remotely it is a POSIX script, because resolving the provider is only
// half of running it: one found on a login shell's PATH is regularly a
// shim — mise, asdf, a Homebrew wrapper — that needs the rest of that
// shell's environment to work, and the daemon over there has whatever
// environment it was started with instead. The script hands off to the
// host's shell, which hands off to Stormlight, which becomes the
// provider. The argv travels in the environment rather than through the
// shell, so nothing about it is ever parsed as syntax.
//
// The check that the host has the provider at all happens here, before
// an agent exists to be a corpse: a machine without it should say so in
// the dashboard, not in a terminal nobody asked for.
func (r *Runtime) launchCommand(ctx context.Context, launch session.Launch) (string, []string, error) {
	if r.transport == nil {
		return launch.Path, launch.Args, nil
	}
	if _, err := remote.LookPath(ctx, r.transport, providerName(launch)); err != nil {
		return "", nil, err
	}
	return "/bin/sh", []string{"-c", remote.ExecPrelude}, nil
}

// providerName is the provider as something to look up, rather than as a
// path this machine happens to have resolved.
func providerName(launch session.Launch) string {
	if launch.Program != "" {
		return launch.Program
	}
	// A launch with no name to resolve — a custom spec from an older
	// record — leaves nothing better than the path it carries.
	return launch.Path
}

// sessionIDFor maps an agent id to the daemon's session id.
func (r *Runtime) sessionIDFor(id string) (string, error) {
	if id == "" {
		// An empty id would prefix-match the first agent listed —
		// destructive commands must never guess.
		return "", fmt.Errorf("agent id is required")
	}
	sessions, err := r.client.List()
	if err != nil {
		return "", err
	}
	match := ""
	for _, info := range sessions {
		managedAgent, ok := decodeAgent(info)
		if !ok {
			continue
		}
		if managedAgent.ID == id {
			return info.ID, nil
		}
		if strings.HasPrefix(managedAgent.ID, id) {
			if match != "" {
				return "", fmt.Errorf("agent id %q is ambiguous", id)
			}
			match = info.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("agent %q not found", id)
	}
	return match, nil
}

func (r *Runtime) Capture(ctx context.Context, id string, lines int) (string, error) {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return "", err
	}
	snapshot, err := r.client.Snapshot(sessionID)
	if err != nil {
		return "", err
	}
	return string(snapshot.ANSI), nil
}

// Attach hands back the command that becomes the agent's real terminal:
// an interactive windrunner attachment in the caller's own terminal,
// ctrl+q to return. The terminal comes to you.
func (r *Runtime) Attach(ctx context.Context, id string) (session.AttachResult, error) {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return session.AttachResult{}, err
	}
	if r.transport != nil {
		// The attachment runs where the daemon is, over a tty ssh
		// allocates. WINDRUNNER_DIR is passed rather than left to the far
		// side's own resolution: a tty session runs a login shell and a
		// bridge does not, so the two can disagree about XDG_STATE_HOME —
		// and then `F` would attach to a daemon nobody else is talking
		// to. The bridge already reported the answer.
		hello := r.transport.Hello()
		return session.AttachResult{Command: r.transport.TTYCommand(
			[]string{"WINDRUNNER_DIR=" + hello.SocketDir},
			"_wrattach", sessionID,
		)}, nil
	}
	binPath, err := selfpath.Resolve()
	if err != nil {
		return session.AttachResult{}, err
	}
	command := exec.Command(binPath, "_wrattach", sessionID)
	command.Env = append(os.Environ(), "WINDRUNNER_DIR="+SocketDir())
	return session.AttachResult{Command: command}, nil
}

func (r *Runtime) Send(ctx context.Context, id, message string) error {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, "\n") {
		// Slash commands are typed, not pasted: providers ignore
		// bracketed-paste slash commands.
		if err := r.client.Input(sessionID, []byte(trimmed)); err != nil {
			return err
		}
	} else {
		// Bracketed paste keeps multi-line messages one message.
		payload := "\x1b[200~" + message + "\x1b[201~"
		if err := r.client.Input(sessionID, []byte(payload)); err != nil {
			return err
		}
	}
	time.Sleep(sendSubmitDelay)
	if err := r.client.Input(sessionID, []byte("\r")); err != nil {
		return err
	}
	return r.Update(ctx, id, session.Update{Activity: agent.ActivityWorking})
}

func (r *Runtime) Interrupt(ctx context.Context, id string) error {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return err
	}
	if err := r.client.Input(sessionID, []byte{0x03}); err != nil {
		return err
	}
	return r.Update(ctx, id, session.Update{Activity: agent.ActivityIdle})
}

func (r *Runtime) Delete(ctx context.Context, id string) error {
	sessionID, err := r.sessionIDFor(id)
	if err != nil {
		return err
	}
	return r.client.Remove(sessionID)
}

func (r *Runtime) Rename(ctx context.Context, id, name string) error {
	return r.mutateAgent(id, func(managedAgent *agent.Agent) {
		managedAgent.Name = name
	})
}

func (r *Runtime) SetWorkspace(ctx context.Context, id string, value workspace.Context) error {
	return r.mutateAgent(id, func(managedAgent *agent.Agent) {
		managedAgent.Workspace = value
	})
}

func (r *Runtime) Update(ctx context.Context, id string, update session.Update) error {
	return r.mutateAgent(id, func(managedAgent *agent.Agent) {
		*managedAgent = applyUpdate(*managedAgent, update)
	})
}

// mutateAgent is the read-modify-write on the metadata document. Two
// near-simultaneous writers (a hook event racing the dashboard) can lose
// one update; events are sparse enough that the next one repairs it, and
// the daemon-side CAS this wants is noted for later.
func (r *Runtime) mutateAgent(id string, mutate func(*agent.Agent)) error {
	sessions, err := r.client.List()
	if err != nil {
		return err
	}
	for _, info := range sessions {
		managedAgent, ok := decodeAgent(info)
		if !ok {
			continue
		}
		if managedAgent.ID != id && !strings.HasPrefix(managedAgent.ID, id) {
			continue
		}
		mutate(&managedAgent)
		encoded, err := json.Marshal(managedAgent)
		if err != nil {
			return fmt.Errorf("encode agent metadata: %w", err)
		}
		metadata := info.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata[metadataKey] = string(encoded)
		return r.client.SetMetadata(info.ID, metadata)
	}
	return fmt.Errorf("agent %q not found", id)
}

func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate agent id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
