package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/trentkm/runstead/internal/agent"
)

type Launch struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type Info struct {
	ID        agent.Provider
	Label     string
	Available bool
	Path      string
}

type Adapter interface {
	ID() agent.Provider
	Label() string
	Resolve(prompt string) (Launch, error)
}

type commandAdapter struct {
	id      agent.Provider
	label   string
	binary  string
	argsFor func(prompt string) ([]string, error)
}

func (a commandAdapter) ID() agent.Provider {
	return a.id
}

func (a commandAdapter) Label() string {
	return a.label
}

func (a commandAdapter) Resolve(prompt string) (Launch, error) {
	path, err := exec.LookPath(a.binary)
	if err != nil {
		return Launch{}, fmt.Errorf("%s is not installed or not on PATH", a.binary)
	}
	args, err := a.argsFor(prompt)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Path: path, Args: args}, nil
}

type Registry struct {
	adapters map[agent.Provider]Adapter
	order    []agent.Provider
}

func NewRegistry() *Registry {
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
			argsFor: func(prompt string) ([]string, error) {
				return []string{"-lc", prompt}, nil
			},
		},
	}

	r := &Registry{
		adapters: make(map[agent.Provider]Adapter, len(adapters)),
		order:    make([]agent.Provider, 0, len(adapters)),
	}
	for _, adapter := range adapters {
		r.adapters[adapter.ID()] = adapter
		r.order = append(r.order, adapter.ID())
	}
	return r
}

func (r *Registry) Resolve(id agent.Provider, prompt string) (Launch, error) {
	adapter, ok := r.adapters[id]
	if !ok {
		return Launch{}, fmt.Errorf("unsupported provider %q", id)
	}
	if strings.TrimSpace(prompt) == "" {
		return Launch{}, fmt.Errorf("task cannot be empty")
	}
	return adapter.Resolve(prompt)
}

func (r *Registry) Infos() []Info {
	infos := make([]Info, 0, len(r.order))
	for _, id := range r.order {
		adapter := r.adapters[id]
		launch, err := adapter.Resolve("availability check")
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

func codexArgs(prompt string) ([]string, error) {
	notify, err := json.Marshal([]string{
		"/bin/sh",
		"-c",
		`exec "${RUNSTEAD_BIN:-$AGENTMUX_BIN}" _provider-event codex "$0"`,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Codex notification command: %w", err)
	}
	return []string{"-c", "notify=" + string(notify), prompt}, nil
}

func claudeArgs(prompt string) ([]string, error) {
	const eventCommand = `exec "${RUNSTEAD_BIN:-$AGENTMUX_BIN}" _provider-event claude`
	const permissionCommand = `exec "${RUNSTEAD_BIN:-$AGENTMUX_BIN}" _provider-permission claude`
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
			},
			"PermissionRequest": {
				{
					Hooks: []claudeHook{{
						Type:          "command",
						Command:       permissionCommand,
						Timeout:       86400,
						StatusMessage: "Waiting for approval in Runstead",
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
	return []string{"--settings", string(encoded), prompt}, nil
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
