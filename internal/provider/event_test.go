package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

// Codex reports through the same hook schema Claude does, including a
// turn start — the signal its old `notify` callback never carried, and
// without which an agent prompted in its own pane stayed `idle` (#66).
func TestParseCodexLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		activity  agent.Activity
		attention agent.Attention
		summary   string
		turnEnded bool
	}{
		{
			name: "prompt",
			payload: `{"hook_event_name":"UserPromptSubmit",` +
				`"prompt":"Run the tests","transcript_path":"/tmp/codex.jsonl"}`,
			activity: agent.ActivityWorking,
			summary:  "Run the tests",
		},
		{
			name: "stop",
			payload: `{"hook_event_name":"Stop",` +
				`"last_assistant_message":"Tests pass.","stop_hook_active":false}`,
			activity:  agent.ActivityIdle,
			attention: agent.AttentionWaiting,
			summary:   "Tests pass.",
			turnEnded: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, handled, err := ParseEvent(agent.ProviderCodex, []byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !handled ||
				event.Activity != test.activity ||
				event.Attention != test.attention ||
				event.Summary != test.summary ||
				event.TurnEnded != test.turnEnded {
				t.Fatalf("event = %#v, handled = %v", event, handled)
			}
		})
	}
}

// Codex leaves transcript_path and last_assistant_message nullable, and a
// JSON null must decode to an absent value rather than fail the hook.
func TestParseCodexTolerantOfNullFields(t *testing.T) {
	event, handled, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"hook_event_name":"Stop","last_assistant_message":null,`+
			`"transcript_path":null,"stop_hook_active":false}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !handled || event.Activity != agent.ActivityIdle || !event.TurnEnded {
		t.Fatalf("event = %#v, handled = %v", event, handled)
	}
	if event.Summary != "" || event.TranscriptPath != "" {
		t.Fatalf("null fields leaked: %#v", event)
	}
}

// Codex holds injected hooks at "installed, not active" until a human
// trusts them. `notify` carries no such gate, so it remains the floor
// beneath the hooks: an agent whose hooks are untrusted still reports the
// end of a turn instead of sitting at `working` until its process exits.
func TestParseCodexNotifyReportsTurnEndWithoutHooks(t *testing.T) {
	event, handled, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"type":"agent-turn-complete","last-assistant-message":"Tests pass."}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !handled || event.Activity != agent.ActivityIdle ||
		event.Summary != "Tests pass." || !event.TurnEnded {
		t.Fatalf("event = %#v, handled = %v", event, handled)
	}
}

// With hooks trusted a turn end arrives on both surfaces. The two must
// agree, because the dashboard applies whichever lands second.
func TestCodexTurnEndIsIdenticalOnBothSurfaces(t *testing.T) {
	const message = "Implementation complete"
	viaNotify, _, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"type":"agent-turn-complete","last-assistant-message":"`+message+`"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	viaHook, _, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"hook_event_name":"Stop","last_assistant_message":"`+message+`"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if viaNotify != viaHook {
		t.Fatalf("notify = %#v, hook = %#v", viaNotify, viaHook)
	}
}

func TestEventSummaryIsBounded(t *testing.T) {
	event, handled, err := ParseEvent(agent.ProviderCodex, []byte(
		`{"hook_event_name":"Stop","last_assistant_message":"`+
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
	payload := []byte(`{"hook_event_name":"Stop",` +
		`"last_assistant_message":"Should I also update the docs?"}`)
	event, handled, err := ParseEvent(agent.ProviderCodex, payload)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if event.Attention != agent.AttentionQuestion {
		t.Fatalf("attention = %q", event.Attention)
	}
}
