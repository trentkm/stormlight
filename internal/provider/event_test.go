package provider

import (
	"encoding/json"
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
			name:      "stop",
			payload:   `{"hook_event_name":"Stop","last_assistant_message":"Implementation complete"}`,
			activity:  agent.ActivityIdle,
			attention: agent.AttentionWaiting,
			summary:   "Implementation complete",
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

func TestStopClassifiesQuestionsAsUrgentAttention(t *testing.T) {
	cases := []struct {
		message string
		want    agent.Attention
	}{
		{"Which approach do you prefer?", agent.AttentionQuestion},
		{"Ready to help — what would you like to work on?", agent.AttentionQuestion},
		{"Should I proceed with the refactor?**", agent.AttentionQuestion},
		{"All tests pass.\n\nWant me to commit?", agent.AttentionQuestion},
		// Finished turns without a question are unseen results.
		{"Done, all tests pass.", agent.AttentionWaiting},
		{"Fixed the bug. See `internal/ui`.", agent.AttentionWaiting},
		{"Is it a bug? I checked — it is, and it's fixed now.", agent.AttentionWaiting},
		{"", agent.AttentionWaiting},
	}
	for _, c := range cases {
		payload := map[string]string{
			"hook_event_name":        "Stop",
			"last_assistant_message": c.message,
		}
		encoded, _ := json.Marshal(payload)
		event, handled, err := ParseEvent(agent.ProviderClaude, encoded)
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if event.Attention != c.want {
			t.Errorf("attention for %q = %q, want %q", c.message, event.Attention, c.want)
		}
	}
}

func TestIdlePromptNotificationIsIgnored(t *testing.T) {
	// Unseen results are marked the moment the turn ends; the delayed idle
	// echo must not re-raise attention the human already cleared.
	payload := []byte(`{"hook_event_name":"Notification",` +
		`"message":"Claude is waiting for your input",` +
		`"notification_type":"idle_prompt"}`)
	_, handled, err := ParseEvent(agent.ProviderClaude, payload)
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v, want ignored", handled, err)
	}

	permission := []byte(`{"hook_event_name":"Notification",` +
		`"message":"Claude needs your permission to use Bash",` +
		`"notification_type":"permission_prompt"}`)
	event, handled, err := ParseEvent(agent.ProviderClaude, permission)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if event.Attention != agent.AttentionApproval {
		t.Fatalf("permission attention = %q", event.Attention)
	}
}

func TestCodexCompletionClassifiesQuestions(t *testing.T) {
	payload := []byte(`{"type":"agent-turn-complete",` +
		`"last-assistant-message":"Should I also update the docs?"}`)
	event, handled, err := ParseEvent(agent.ProviderCodex, payload)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if event.Attention != agent.AttentionQuestion {
		t.Fatalf("attention = %q", event.Attention)
	}
}
