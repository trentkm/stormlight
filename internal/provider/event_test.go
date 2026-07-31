package provider

import (
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func TestParseCodexCompletion(t *testing.T) {
	event, handled, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"type":"agent-turn-complete","last-assistant-message":"Tests pass."}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !handled || event.Activity != agent.ActivityIdle || event.Summary != "Tests pass." {
		t.Fatalf("event = %#v, handled = %v", event, handled)
	}
}

func TestParseClaudeLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		activity  agent.Activity
		attention agent.Attention
		summary   string
	}{
		{
			name:     "prompt",
			payload:  `{"hook_event_name":"UserPromptSubmit","prompt":"Run the tests"}`,
			activity: agent.ActivityWorking,
			summary:  "Run the tests",
		},
		{
			name:      "approval",
			payload:   `{"hook_event_name":"Notification","message":"Permission required"}`,
			activity:  agent.ActivityIdle,
			attention: agent.AttentionApproval,
			summary:   "Permission required",
		},
		{
			name:     "stop",
			payload:  `{"hook_event_name":"Stop","last_assistant_message":"Implementation complete"}`,
			activity: agent.ActivityIdle,
			summary:  "Implementation complete",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, handled, err := ParseEvent(agent.ProviderClaude, []byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !handled ||
				event.Activity != test.activity ||
				event.Attention != test.attention ||
				event.Summary != test.summary {
				t.Fatalf("event = %#v, handled = %v", event, handled)
			}
		})
	}
}

func TestEventSummaryIsBounded(t *testing.T) {
	event, handled, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"type":"agent-turn-complete","last-assistant-message":"`+
			strings.Repeat("a", maxEventSummaryRunes+20)+`"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !handled || len([]rune(event.Summary)) != maxEventSummaryRunes {
		t.Fatalf("summary length = %d", len([]rune(event.Summary)))
	}
}
