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
	ResolveNamed(prompt, name string, mode agent.PermissionMode) (Launch, error)
	// Binary is the program this provider runs, as a name to look up
	// rather than a path some machine already resolved. It is what a
	// host is asked about when the question is whether that machine can
	// run this provider at all.
	Binary() string
	// CanResume reports whether this adapter has a resume path at all.
	// Capability is a value, not a type: one adapter implementation serves
	// every command-line provider, and whether a given one can be reopened
	// is a fact about that provider rather than about the code.
	CanResume() bool
	Resume(sessionID string, mode agent.PermissionMode) (Launch, error)
	ResumeNamed(sessionID, name string, mode agent.PermissionMode) (Launch, error)
	// SessionFromTranscript reads the provider's conversation id out of a
	// transcript path. The recorded session id is the primary handle —
	// both providers report it on every lifecycle event — but a record
	// written before that capture existed holds only the transcript path,
	// and the adapter is the party that knows how its own transcripts are
	// named. An empty result means the path names nothing resumable.
	SessionFromTranscript(transcriptPath string) string
	// Preview is the argument list a dispatch in mode would run, with
	// TaskPlaceholder standing where the task goes and the user's
	// extra_args in the position they land. It is the launch without the
	// lookup: a provider that is not installed here still has a command
	// line worth reading, which is the whole reason to ask for one. A
	// dispatch that carries a name adds the provider's naming flags just
	// ahead of the task; the preview is the nameless launch and leaves
	// them out.
	Preview(mode agent.PermissionMode) ([]string, error)
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
	// nameFor returns provider arguments that apply a human-facing name to
	// the session. Providers without a launch-time naming surface leave it
	// nil and may synchronize later through SetSessionName.
	nameFor func(name string) []string
	// sessionFor reads the provider's conversation id out of a transcript
	// path; an empty result means this path names nothing resumable.
	sessionFor func(transcriptPath string) string
}

func (a commandAdapter) ID() agent.Provider {
	return a.id
}

func (a commandAdapter) Binary() string {
	return a.binary
}

func (a commandAdapter) Label() string {
	return a.label
}

func (a commandAdapter) Resolve(prompt string, mode agent.PermissionMode) (Launch, error) {
	return a.ResolveNamed(prompt, "", mode)
}

func (a commandAdapter) ResolveNamed(
	prompt string,
	name string,
	mode agent.PermissionMode,
) (Launch, error) {
	return a.launch(a.argsFor, prompt, name, mode)
}

func (a commandAdapter) CanResume() bool {
	return a.resumeFor != nil
}

func (a commandAdapter) Resume(
	sessionID string,
	mode agent.PermissionMode,
) (Launch, error) {
	return a.ResumeNamed(sessionID, "", mode)
}

func (a commandAdapter) ResumeNamed(
	sessionID string,
	name string,
	mode agent.PermissionMode,
) (Launch, error) {
	if !a.CanResume() {
		return Launch{}, fmt.Errorf("%s cannot resume a conversation", a.label)
	}
	return a.launch(a.resumeFor, sessionID, name, mode)
}

func (a commandAdapter) Preview(mode agent.PermissionMode) ([]string, error) {
	args, err := a.argsFor(TaskPlaceholder, mode)
	if err != nil {
		return nil, err
	}
	return a.withExtra(args), nil
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
	name string,
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
	args = a.withName(args, name)
	return Launch{Path: path, Program: a.binary, Args: a.withExtra(args)}, nil
}

// withName slots launch-time naming flags before the prompt or resume id.
// The final argument is deliberately kept final for the same reason as
// withExtra: it is the one variable value both launch forms share.
func (a commandAdapter) withName(args []string, name string) []string {
	if a.nameFor == nil || len(args) == 0 {
		return args
	}
	nameArgs := a.nameFor(name)
	if len(nameArgs) == 0 {
		return args
	}
	combined := slices.Clone(args[:len(args)-1])
	combined = append(combined, nameArgs...)
	return append(combined, args[len(args)-1])
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

// builtinAdapters are the providers Stormlight ships. A Spec naming one of
// them customizes it; anything else declares a new provider.
func builtinAdapters() []Adapter {
	return []Adapter{
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
			nameFor:    claudeNameArgs,
			sessionFor: claudeSessionID,
		},
	}
}

// IsBuiltin reports whether id names an adapter Stormlight ships, which a
// Spec can tune but never replace. Config consults it to say so when a
// block carries keys only a declared provider honors.
func IsBuiltin(id agent.Provider) bool {
	for _, adapter := range builtinAdapters() {
		if adapter.ID() == id {
			return true
		}
	}
	return false
}

func NewRegistryWithSpecs(specs []Spec) *Registry {
	adapters := builtinAdapters()

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
	return r.ResolveNamed(id, prompt, "", mode)
}

// ResolveNamed builds a fresh launch and applies a provider-native session
// name when the provider exposes one at startup.
func (r *Registry) ResolveNamed(
	id agent.Provider,
	prompt string,
	name string,
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
	return adapter.ResolveNamed(prompt, name, mode)
}

// Resume builds the launch that reopens a provider's recorded session.
func (r *Registry) Resume(
	id agent.Provider,
	sessionID string,
	mode agent.PermissionMode,
) (Launch, error) {
	return r.ResumeNamed(id, sessionID, "", mode)
}

// ResumeNamed reopens a provider session and reapplies its stored name when
// the provider accepts names on resumed launches.
func (r *Registry) ResumeNamed(
	id agent.Provider,
	sessionID string,
	name string,
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
	return adapter.ResumeNamed(sessionID, name, mode)
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

// Description is one provider as `stormlight config providers` reports it:
// what it is, where it runs from on this machine, and exactly what it runs
// in each permission mode. When a provider CLI changes its flags, this is
// what shows the argv that broke — without reading the adapter's source.
type Description struct {
	ID        agent.Provider `json:"id"`
	Label     string         `json:"label"`
	Builtin   bool           `json:"builtin"`
	Binary    string         `json:"binary"`
	Path      string         `json:"path,omitempty"`
	Available bool           `json:"available"`
	Modes     []ModePreview  `json:"modes"`
}

// ModePreview is the command line one permission mode produces, with
// TaskPlaceholder where the task goes.
type ModePreview struct {
	Mode agent.PermissionMode `json:"mode"`
	Args []string             `json:"args"`
	// Error is set when the adapter could not build the mode's arguments,
	// which is a fact about the provider worth reporting in its place.
	Error string `json:"error,omitempty"`
}

// Describe reports every registered provider in registration order.
func (r *Registry) Describe() []Description {
	descriptions := make([]Description, 0, len(r.order))
	for _, id := range r.order {
		adapter := r.adapters[id]
		description := Description{
			ID:      id,
			Label:   adapter.Label(),
			Builtin: IsBuiltin(id),
			Binary:  adapter.Binary(),
		}
		description.Path, description.Available = locate(adapter)
		for _, mode := range agent.Modes() {
			preview := ModePreview{Mode: mode}
			args, err := adapter.Preview(mode)
			if err != nil {
				preview.Error = err.Error()
				// An empty list rather than nothing, so the JSON shape
				// holds for a consumer that iterates it.
				args = []string{}
			}
			preview.Args = args
			description.Modes = append(description.Modes, preview)
		}
		descriptions = append(descriptions, description)
	}
	return descriptions
}

// locate answers where a provider's binary is on this machine, which is
// the one fact "available" means. Infos and Describe both report it, and
// they have to agree.
func locate(adapter Adapter) (path string, available bool) {
	path, err := exec.LookPath(adapter.Binary())
	if err != nil {
		return "", false
	}
	return path, true
}

func (r *Registry) Infos() []Info {
	infos := make([]Info, 0, len(r.order))
	for _, id := range r.order {
		adapter := r.adapters[id]
		path, available := locate(adapter)
		infos = append(infos, Info{
			ID:        id,
			Label:     adapter.Label(),
			Available: available,
			Path:      path,
		})
	}
	return infos
}

// Binaries are the programs the configured providers run, deduplicated
// and in registration order. A host is asked about these — which of its
// shells can see them is the question that decides whether it can host
// an agent at all.
func (r *Registry) Binaries() []string {
	seen := make(map[string]bool, len(r.order))
	binaries := make([]string, 0, len(r.order))
	for _, id := range r.order {
		binary := r.adapters[id].Binary()
		if binary == "" || seen[binary] {
			continue
		}
		seen[binary] = true
		binaries = append(binaries, binary)
	}
	return binaries
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
	// --no-alt-screen keeps Codex on the main screen. In the alternate
	// screen its history lives only inside Codex, so the terminal Stormlight
	// hosts has no scrollback and the wheel has nothing to move through.
	args := []string{"-c", notify, "-c", hooks, "--no-alt-screen"}
	return append(args, codexModeArgs(mode)...), nil
}

// codexModeArgs maps Stormlight permission modes onto Codex's approval and
// sandbox axes.
//
// Codex's approval flag is down to `on-request` and `never`: 0.154 retired
// `untrusted` (openai/codex#39630) and rejects it outright, on the command
// line and in config alike, so the two prompting modes cannot differ on
// that axis any more. They differ on the sandbox instead. `ask` runs
// read-only — Codex's own "read only" preset — where every edit, every
// write, and any network access is an escalation the model has to request.
// That is narrower than `untrusted` was: a command that only reads runs
// inside the sandbox without a prompt, where before anything off Codex's
// safe-list asked. No surviving CLI value prompts on every command, so
// read-only is the most cautious launch Codex still offers. `edits` widens
// the sandbox to the workspace, so edits land and only network and anything
// outside it still ask. Every value here is accepted by Codex releases
// before and after the retirement.
func codexModeArgs(mode agent.PermissionMode) []string {
	switch mode {
	case agent.ModeAsk:
		return []string{
			"--ask-for-approval", "on-request",
			"--sandbox", "read-only",
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

func claudeNameArgs(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return []string{"--name", name}
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
