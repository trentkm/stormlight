package pending

import (
	"encoding/json"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

type Kind string

const (
	KindApproval Kind = "approval"
	KindChoice   Kind = "choice"
	KindQuestion Kind = "question"
)

const (
	OptionAllowOnce    = "allow_once"
	OptionAlwaysPrefix = "allow_always:"
	OptionChoicePrefix = "choice:"
	OptionDeny         = "deny"
	OptionTerminal     = "terminal"
)

type Option struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Shortcut string `json:"shortcut,omitempty"`
}

// Action is the provider-neutral interaction a running agent needs resolved.
type Action struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	Provider    agent.Provider  `json:"provider"`
	Kind        Kind            `json:"kind"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Detail      string          `json:"detail,omitempty"`
	ToolName    string          `json:"tool_name,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	Options     []Option        `json:"options"`
	CreatedAt   time.Time       `json:"created_at"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type Resolution struct {
	ActionID string    `json:"action_id"`
	OptionID string    `json:"option_id"`
	Created  time.Time `json:"created_at"`
}
