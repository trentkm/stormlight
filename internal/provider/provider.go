package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

type Adapter interface {
	ID() agent.Provider
	Label() string
	Resolve(prompt string, mode agent.PermissionMode) (Launch, error)
}

type commandAdapter struct {
	id      agent.Provider
	label   string
	binary  string
	argsFor func(prompt string, mode agent.PermissionMode) ([]string, error)
}

func (a commandAdapter) ID() agent.Provider {
	return a.id
}

func (a commandAdapter) Label() string {
	return a.label
}

func (a commandAdapter) Resolve(prompt string, mode agent.PermissionMode) (Launch, error) {
	path, err := exec.LookPath(a.binary)
	if err != nil {
		return Launch{}, fmt.Errorf("%s is not installed or not on PATH", a.binary)
	}
	args, err := a.argsFor(prompt, mode)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Path: path, Args: args}, nil
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
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd.exe"
		} else {
			shell = "/bin/sh"
		}
	}

	adapters := []Adapter{
		commandAdapter{
			id:      agent.ProviderCodex,
			label:   "Codex",
			binary:  "codex",
			argsFor: codexArgs,
		},
		commandAdapter{
			id:      agent.ProviderClaude,
			label:   "Claude",
			binary:  "claude",
			argsFor: claudeArgs,
		},
		commandAdapter{
			id:     agent.ProviderShell,
			label:  "Shell",
			binary: shell,
			argsFor: func(prompt string, _ agent.PermissionMode) ([]string, error) {
				return []string{"-lc", prompt}, nil
			},
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
	if len(spec.ExtraArgs) == 0 {
		return builtin
	}
	base := builtin.argsFor
	extra := slices.Clone(spec.ExtraArgs)
	builtin.argsFor = func(prompt string, mode agent.PermissionMode) ([]string, error) {
		args, err := base(prompt, mode)
		if err != nil {
			return nil, err
		}
		// Built-in arg builders keep the prompt as the final argument;
		// extra args slot in just before it.
		prefix := slices.Clone(args[:len(args)-1])
		prefix = append(prefix, extra...)
		return append(prefix, args[len(args)-1]), nil
	}
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

func codexArgs(prompt string, mode agent.PermissionMode) ([]string, error) {
	notify, err := json.Marshal([]string{
		"/bin/sh",
		"-c",
		`exec "$STORMLIGHT_BIN" _provider-event codex "$0"`,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Codex notification command: %w", err)
	}
	args := []string{"-c", "notify=" + string(notify)}
	args = append(args, codexModeArgs(mode)...)
	return append(args, prompt), nil
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

func claudeArgs(prompt string, mode agent.PermissionMode) ([]string, error) {
	const eventCommand = `exec "$STORMLIGHT_BIN" _provider-event claude`
	const permissionCommand = `exec "$STORMLIGHT_BIN" _provider-permission claude`
	settings := claudeSettings{
		Hooks: map[string][]claudeHookGroup{
			"UserPromptSubmit": {
				{Hooks: []claudeHook{{Type: "command", Command: eventCommand, Timeout: 5}}},
			},
			"Notification": {
				{
					Matcher: "permission_prompt",
					Hooks:   []claudeHook{{Type: "command", Command: eventCommand, Timeout: 5}},
				},
				{
					Matcher: "idle_prompt",
					Hooks:   []claudeHook{{Type: "command", Command: eventCommand, Timeout: 5}},
				},
			},
			"PermissionRequest": {
				{
					Hooks: []claudeHook{{
						Type:          "command",
						Command:       permissionCommand,
						Timeout:       86400,
						StatusMessage: "Waiting for approval in Stormlight",
					}},
				},
			},
			"Stop": {
				{Hooks: []claudeHook{{Type: "command", Command: eventCommand, Timeout: 5}}},
			},
		},
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode Claude hook settings: %w", err)
	}
	args := []string{"--settings", string(encoded)}
	switch mode {
	case agent.ModeEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.ModeAuto:
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	return append(args, prompt), nil
}

type claudeSettings struct {
	Hooks map[string][]claudeHookGroup `json:"hooks"`
}

type claudeHookGroup struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []claudeHook `json:"hooks"`
}

type claudeHook struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout"`
	StatusMessage string `json:"statusMessage,omitempty"`
}
