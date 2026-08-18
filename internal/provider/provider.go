package provider

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
)

type Launch = session.Launch

type Info struct {
	ID        agent.Provider
	Label     string
	Available bool
	Path      string
}

// Adapter builds the launches for one provider. Resolve starts a fresh
// conversation; Resume reopens a recorded one by its session id, with the
// same lifecycle wiring a fresh dispatch gets and no prompt — reopening
// hands the conversation back to the human at its composer, it does not
// start a turn. A machine that reboots overnight has to wake up to agents
// that are back, not to agents that are working unattended.
type Adapter interface {
	ID() agent.Provider
	Label() string
	Resolve(prompt string, mode agent.PermissionMode) (Launch, error)
	// CanResume reports whether this adapter has a resume path at all.
	// Capability is a value, not a type: one adapter implementation serves
	// every command-line provider, and whether a given one can be reopened
	// is a fact about that provider rather than about the code.
	CanResume() bool
	Resume(sessionID string, mode agent.PermissionMode) (Launch, error)
	// SessionFromTranscript reads the provider's conversation id out of a
	// transcript path. The recorded session id is the primary handle —
	// both providers report it on every lifecycle event — but a record
	// written before that capture existed holds only the transcript path,
	// and the adapter is the party that knows how its own transcripts are
	// named. An empty result means the path names nothing resumable.
	SessionFromTranscript(transcriptPath string) string
}

type commandAdapter struct {
	id     agent.Provider
	label  string
	binary string
	// extra is the user's configured extra_args, applied by the adapter
	// rather than folded into the builders — both dispatch and resume have
	// to place them, and only the adapter knows both.
	extra   []string
	argsFor func(prompt string, mode agent.PermissionMode) ([]string, error)
	// resumeFor builds the arguments that reopen a conversation, or is nil
	// for a provider that cannot — custom specs declare how to start a
	// conversation, not how to reopen one. Like argsFor it keeps the
	// variable argument last, so extra args slot in the same way for both.
	resumeFor func(sessionID string, mode agent.PermissionMode) ([]string, error)
	// sessionFor reads the provider's conversation id out of a transcript
	// path; an empty result means this path names nothing resumable.
	sessionFor func(transcriptPath string) string
}

func (a commandAdapter) ID() agent.Provider {
	return a.id
}

func (a commandAdapter) Label() string {
	return a.label
}

func (a commandAdapter) Resolve(prompt string, mode agent.PermissionMode) (Launch, error) {
	return a.launch(a.argsFor, prompt, mode)
}

func (a commandAdapter) CanResume() bool {
	return a.resumeFor != nil
}

func (a commandAdapter) Resume(
	sessionID string,
	mode agent.PermissionMode,
) (Launch, error) {
	if !a.CanResume() {
		return Launch{}, fmt.Errorf("%s cannot resume a conversation", a.label)
	}
	return a.launch(a.resumeFor, sessionID, mode)
}

func (a commandAdapter) SessionFromTranscript(transcriptPath string) string {
	if a.sessionFor == nil {
		return ""
	}
	return a.sessionFor(transcriptPath)
}

func (a commandAdapter) launch(
	build func(string, agent.PermissionMode) ([]string, error),
	value string,
	mode agent.PermissionMode,
) (Launch, error) {
	path, err := exec.LookPath(a.binary)
	if err != nil {
		return Launch{}, fmt.Errorf("%s is not installed or not on PATH", a.binary)
	}
	args, err := build(value, mode)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Path: path, Args: a.withExtra(args)}, nil
}

// withExtra slots the user's extra args in just before the final argument.
// Both builders keep the variable part — the prompt, the `--resume=<id>`
// that stands in for it, or the positional session id — last, so one rule
// places extras for both.
func (a commandAdapter) withExtra(args []string) []string {
	if len(a.extra) == 0 || len(args) == 0 {
		return args
	}
	combined := slices.Clone(args[:len(args)-1])
	combined = append(combined, a.extra...)
	return append(combined, args[len(args)-1])
}

// Spec customizes or declares a provider from user configuration. A Spec
// whose ID matches a built-in provider may override its binary and label and
// append ExtraArgs; Args and ModeArgs are only honored for new providers, so
// configuration can refine but never remove the built-in lifecycle wiring.
type Spec struct {
	ID        agent.Provider
	Label     string
	Binary    string
	Args      []string
	ExtraArgs []string
	ModeArgs  map[agent.PermissionMode][]string
}

// TaskPlaceholder in a custom provider's Args is replaced with the task
// text. Args are always exec-style — no shell interpolation happens.
const TaskPlaceholder = "{task}"

type Registry struct {
	adapters map[agent.Provider]Adapter
	order    []agent.Provider
}

func NewRegistry() *Registry {
	return NewRegistryWithSpecs(nil)
}

func NewRegistryWithSpecs(specs []Spec) *Registry {
	adapters := []Adapter{
		commandAdapter{
			id:         agent.ProviderCodex,
			label:      "Codex",
			binary:     "codex",
			argsFor:    codexArgs,
			resumeFor:  codexResumeArgs,
			sessionFor: codexSessionID,
		},
		commandAdapter{
			id:         agent.ProviderClaude,
			label:      "Claude",
			binary:     "claude",
			argsFor:    claudeArgs,
			resumeFor:  claudeResumeArgs,
			sessionFor: claudeSessionID,
		},
	}

	r := &Registry{
		adapters: make(map[agent.Provider]Adapter, len(adapters)+len(specs)),
		order:    make([]agent.Provider, 0, len(adapters)+len(specs)),
	}
	for _, adapter := range adapters {
		r.adapters[adapter.ID()] = adapter
		r.order = append(r.order, adapter.ID())
	}
	for _, spec := range specs {
		if builtin, ok := r.adapters[spec.ID]; ok {
			r.adapters[spec.ID] = customizeBuiltin(builtin.(commandAdapter), spec)
			continue
		}
		r.adapters[spec.ID] = customAdapter(spec)
		r.order = append(r.order, spec.ID)
	}
	return r
}

func customizeBuiltin(builtin commandAdapter, spec Spec) commandAdapter {
	if spec.Binary != "" {
		builtin.binary = spec.Binary
	}
	if spec.Label != "" {
		builtin.label = spec.Label
	}
	builtin.extra = slices.Clone(spec.ExtraArgs)
	return builtin
}

func customAdapter(spec Spec) commandAdapter {
	label := spec.Label
	if label == "" {
		label = string(spec.ID)
	}
	binary := spec.Binary
	if binary == "" {
		binary = string(spec.ID)
	}
	template := slices.Clone(spec.Args)
	modeArgs := make(map[agent.PermissionMode][]string, len(spec.ModeArgs))
	for mode, args := range spec.ModeArgs {
		modeArgs[mode] = slices.Clone(args)
	}
	return commandAdapter{
		id:     spec.ID,
		label:  label,
		binary: binary,
		argsFor: func(prompt string, mode agent.PermissionMode) ([]string, error) {
			args := slices.Clone(modeArgs[mode])
			substituted := false
			for _, arg := range template {
				if strings.Contains(arg, TaskPlaceholder) {
					arg = strings.ReplaceAll(arg, TaskPlaceholder, prompt)
					substituted = true
				}
				args = append(args, arg)
			}
			if !substituted {
				args = append(args, prompt)
			}
			return args, nil
		},
	}
}

func (r *Registry) Resolve(
	id agent.Provider,
	prompt string,
	mode agent.PermissionMode,
) (Launch, error) {
	adapter, ok := r.adapters[id]
	if !ok {
		return Launch{}, fmt.Errorf("unsupported provider %q", id)
	}
	if strings.TrimSpace(prompt) == "" {
		return Launch{}, fmt.Errorf("task cannot be empty")
	}
	if mode == "" {
		mode = agent.DefaultMode
	}
	return adapter.Resolve(prompt, mode)
}

// Resume builds the launch that reopens a provider's recorded session.
func (r *Registry) Resume(
	id agent.Provider,
	sessionID string,
	mode agent.PermissionMode,
) (Launch, error) {
	adapter, ok := r.adapters[id]
	if !ok {
		return Launch{}, fmt.Errorf("unsupported provider %q", id)
	}
	if !adapter.CanResume() {
		return Launch{}, fmt.Errorf(
			"%s cannot resume a conversation",
			adapter.Label(),
		)
	}
	if strings.TrimSpace(sessionID) == "" {
		return Launch{}, fmt.Errorf("session id cannot be empty")
	}
	if mode == "" {
		mode = agent.DefaultMode
	}
	return adapter.Resume(sessionID, mode)
}

// SessionID resolves the conversation id a record holds: the recorded id
// when one was captured, else whatever the provider's transcript naming
// gives back. Empty means the record names nothing resumable.
func (r *Registry) SessionID(
	id agent.Provider,
	recordedID string,
	transcriptPath string,
) string {
	if strings.TrimSpace(recordedID) != "" {
		return strings.TrimSpace(recordedID)
	}
	adapter, ok := r.adapters[id]
	if !ok {
		return ""
	}
	return adapter.SessionFromTranscript(transcriptPath)
}

// CanResume reports whether a provider can reopen its own conversations at
// all — the question a restore listing asks before it asks about any
// particular agent.
func (r *Registry) CanResume(id agent.Provider) bool {
	adapter, ok := r.adapters[id]
	if !ok {
		return false
	}
	return adapter.CanResume()
}

// Label names a provider for a human, falling back to the bare id for one
// that is configured out of existence — a snapshot outlives the config that
// dispatched it, so restore has to be able to talk about a provider that is
// no longer registered.
func (r *Registry) Label(id agent.Provider) string {
	if adapter, ok := r.adapters[id]; ok {
		return adapter.Label()
	}
	return string(id)
}

func (r *Registry) Infos() []Info {
	infos := make([]Info, 0, len(r.order))
	for _, id := range r.order {
		adapter := r.adapters[id]
		launch, err := adapter.Resolve("availability check", agent.DefaultMode)
		infos = append(infos, Info{
			ID:        id,
			Label:     adapter.Label(),
			Available: err == nil,
			Path:      launch.Path,
		})
	}
	return infos
}

func (r *Registry) IDs() []agent.Provider {
	return slices.Clone(r.order)
}

// codexArgs wires both of Codex's lifecycle surfaces, because neither one
// is sufficient alone.
//
// The hooks are what Stormlight actually wants: `notify` fires on exactly
// one event — agent-turn-complete — so a turn started in the agent's own
// pane never reached Stormlight and the row went on claiming `idle` while
// Codex worked. UserPromptSubmit is that missing turn-start signal.
//
// But hooks injected this way are inert until a human trusts them. Codex
// hashes each handler and holds it at "installed, not active" behind a
// startup review prompt, so a first-run agent reports nothing at all.
// `notify` carries no such gate, and an agent that only reports turn ends
// is the behavior Stormlight had all along — where an agent that reports
// nothing would sit at `working` until its process exited. So `notify`
// stays as the floor, and the hooks raise the ceiling once trusted. When
// both are live a turn end arrives twice, which is harmless: the two
// events carry the same state and applying it twice is idempotent.
//
// Codex's PermissionRequest hook is deliberately not registered. It is an
// approval resolver rather than an observer — its reply decides whether the
// tool call proceeds — and Stormlight answers prompts in the agent's own
// terminal, exactly as it declines to intercept Claude's.
func codexArgs(prompt string, mode agent.PermissionMode) ([]string, error) {
	args, err := codexLifecycleArgs(mode)
	if err != nil {
		return nil, err
	}
	return append(args, prompt), nil
}

// codexResumeArgs reopens a recorded session in place of starting one:
// `codex resume <session-id>` continues the conversation the rollout file
// holds, with the same lifecycle wiring a fresh dispatch gets. No prompt
// rides along — the reopened conversation is handed to the human. The id
// stays the final argument, positional after the flags, which is where
// withExtra slots the user's extra args in front of.
func codexResumeArgs(sessionID string, mode agent.PermissionMode) ([]string, error) {
	lifecycle, err := codexLifecycleArgs(mode)
	if err != nil {
		return nil, err
	}
	args := append([]string{"resume"}, lifecycle...)
	return append(args, sessionID), nil
}

// codexSessionID reads the conversation id out of a Codex rollout path.
// Codex names rollouts `rollout-<timestamp>-<uuid>.jsonl`, so the trailing
// uuid of the stem is the id `codex resume` takes; anything shaped
// differently names no conversation.
func codexSessionID(transcriptPath string) string {
	stem, found := strings.CutSuffix(
		filepath.Base(strings.TrimSpace(transcriptPath)),
		".jsonl",
	)
	if !found || !strings.HasPrefix(stem, "rollout-") || len(stem) < 36 {
		return ""
	}
	id := stem[len(stem)-36:]
	for _, index := range []int{8, 13, 18, 23} {
		if id[index] != '-' {
			return ""
		}
	}
	return id
}

func codexLifecycleArgs(mode agent.PermissionMode) ([]string, error) {
	notify, err := tomlOverride(struct {
		Notify []string `toml:"notify"`
	}{
		// notify hands the payload to the command as an argument rather
		// than on stdin, which is why it is spelled as a shell command.
		Notify: []string{
			"/bin/sh",
			"-c",
			`exec "${STORMLIGHT_BIN:-stormlight}" _provider-event codex "$0"`,
		},
	})
	if err != nil {
		return nil, err
	}
	hooks, err := tomlOverride(hookSettings{
		Hooks: map[string][]hookGroup{
			"UserPromptSubmit": reportGroup(agent.ProviderCodex),
			"Stop":             reportGroup(agent.ProviderCodex),
		},
	})
	if err != nil {
		return nil, err
	}
	args := []string{"-c", notify, "-c", hooks}
	return append(args, codexModeArgs(mode)...), nil
}

// codexModeArgs maps Stormlight permission modes onto Codex's approval and
// sandbox axes.
func codexModeArgs(mode agent.PermissionMode) []string {
	switch mode {
	case agent.ModeAsk:
		return []string{
			"--ask-for-approval", "untrusted",
			"--sandbox", "workspace-write",
		}
	case agent.ModeAuto:
		return []string{
			"--ask-for-approval", "never",
			"--sandbox", "danger-full-access",
		}
	default:
		return []string{
			"--ask-for-approval", "on-request",
			"--sandbox", "workspace-write",
		}
	}
}

// claudeSessionID reads the conversation id out of a Claude transcript path.
// Claude names the file for the session — `<projects>/<slug>/<uuid>.jsonl` —
// so the basename is the id, and a path that is not a `.jsonl` names no
// conversation Stormlight can hand back to `--resume`.
func claudeSessionID(transcriptPath string) string {
	base := filepath.Base(strings.TrimSpace(transcriptPath))
	id, found := strings.CutSuffix(base, ".jsonl")
	if !found || id == "" || id == "." {
		return ""
	}
	return id
}

// claudeResumeArgs reopens a recorded session in place of starting one,
// with the same hooks and permission mode a dispatch would give it and no
// prompt: Claude loads the transcript and idles at its composer.
//
// `--resume=<id>` rather than `--resume <id>` because the value has to stay
// one argument. It is the last one, which is where withExtra slots the
// user's extra_args in front of — the same rule the prompt gets — and a
// two-argument spelling would let an extra arg land between the flag and
// its value.
func claudeResumeArgs(sessionID string, mode agent.PermissionMode) ([]string, error) {
	args, err := claudeLifecycleArgs(mode)
	if err != nil {
		return nil, err
	}
	return append(args, "--resume="+sessionID), nil
}

func claudeArgs(prompt string, mode agent.PermissionMode) ([]string, error) {
	args, err := claudeLifecycleArgs(mode)
	if err != nil {
		return nil, err
	}
	return append(args, prompt), nil
}

func claudeLifecycleArgs(mode agent.PermissionMode) ([]string, error) {
	// Prompts are answered in the agent's own terminal, not re-implemented
	// in the dashboard: the Notification hook raises attention so the
	// dashboard can point at the pane, and nothing intercepts the request
	// itself. Auto mode is the recommended way to run agents anyway.
	settings := hookSettings{
		Hooks: map[string][]hookGroup{
			"UserPromptSubmit": reportGroup(agent.ProviderClaude),
			"Notification": {
				{
					Matcher: "permission_prompt",
					Hooks:   []hookCommand{reportEvent(agent.ProviderClaude)},
				},
			},
			"Stop": reportGroup(agent.ProviderClaude),
		},
	}
	encoded, err := settings.json()
	if err != nil {
		return nil, err
	}
	args := []string{"--settings", encoded}
	switch mode {
	case agent.ModeEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.ModeAuto:
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	return args, nil
}
