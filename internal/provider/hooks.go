package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/trentkm/stormlight/internal/agent"
)

// Claude and Codex configure lifecycle hooks with the same schema — an
// event name mapping to matcher groups, each group holding command
// handlers — so one set of types describes both. They differ only in how
// the settings reach the CLI: Claude takes JSON through --settings, Codex
// takes TOML through a -c config override.
type hookSettings struct {
	Hooks map[string][]hookGroup `json:"hooks" toml:"hooks"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty" toml:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks" toml:"hooks"`
}

type hookCommand struct {
	Type          string `json:"type" toml:"type"`
	Command       string `json:"command" toml:"command"`
	Timeout       int    `json:"timeout,omitempty" toml:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty" toml:"statusMessage,omitempty"`
}

// hookTimeoutSeconds bounds how long a provider waits on Stormlight before
// carrying on. Reporting state is never worth stalling an agent for.
const hookTimeoutSeconds = 5

// reportEvent builds the handler that hands a hook payload to Stormlight.
// The provider passes the payload on stdin, and $STORMLIGHT_BIN is read at
// hook time rather than baked in so the value comes from the environment
// the managed process was started with.
func reportEvent(providerID agent.Provider) hookCommand {
	return hookCommand{
		Type:    "command",
		Command: `exec "$STORMLIGHT_BIN" _provider-event ` + string(providerID),
		Timeout: hookTimeoutSeconds,
	}
}

// reportGroup wraps reportEvent in the single unmatched group that every
// Stormlight hook uses: state is reported for every occurrence of an event,
// never a subset of tools.
func reportGroup(providerID agent.Provider) []hookGroup {
	return []hookGroup{{Hooks: []hookCommand{reportEvent(providerID)}}}
}

func (s hookSettings) json() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode hook settings: %w", err)
	}
	return string(encoded), nil
}

// tomlOverride renders a settings struct as a single `key = value`
// assignment suitable for `codex -c`. Codex parses the value as TOML and
// rejects JSON, and reads the override from one argument, so the encoding
// has to be inline and single-line rather than the table-per-section layout
// a TOML document would normally use.
func tomlOverride(settings any) (string, error) {
	var buffer bytes.Buffer
	encoder := toml.NewEncoder(&buffer)
	encoder.SetTablesInline(true)
	encoder.SetArraysMultiline(false)
	if err := encoder.Encode(settings); err != nil {
		return "", fmt.Errorf("encode Codex override: %w", err)
	}
	override := strings.TrimSpace(buffer.String())
	if strings.ContainsAny(override, "\r\n") {
		return "", fmt.Errorf("Codex override did not encode to a single line")
	}
	return override, nil
}
