package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
)

const maxEventSummaryRunes = 160

type Event struct {
	Activity  agent.Activity
	Attention agent.Attention
	Summary   string
	// TranscriptPath is the provider's transcript file for the session,
	// when the event carries one (Claude hook payloads always do).
	TranscriptPath string
	// TurnEnded is true for the provider's end-of-turn event, which is
	// allowed to downgrade urgent attention: a finished turn proves any
	// pending prompt was resolved.
	TurnEnded bool
}

func ParseEvent(providerID agent.Provider, payload []byte) (Event, bool, error) {
	switch providerID {
	case agent.ProviderCodex:
		return parseCodexEvent(payload)
	case agent.ProviderClaude:
		return parseClaudeEvent(payload)
	default:
		return Event{}, false, fmt.Errorf("unsupported provider event %q", providerID)
	}
}

func parseCodexEvent(payload []byte) (Event, bool, error) {
	var notification struct {
		Type                 string `json:"type"`
		LastAssistantMessage string `json:"last-assistant-message"`
	}
	if err := json.Unmarshal(payload, &notification); err != nil {
		return Event{}, false, fmt.Errorf("decode Codex notification: %w", err)
	}
	if notification.Type != "agent-turn-complete" {
		return Event{}, false, nil
	}
	return Event{
		Activity:  agent.ActivityIdle,
		Attention: turnEndAttention(notification.LastAssistantMessage),
		Summary:   eventSummary(notification.LastAssistantMessage),
		TurnEnded: true,
	}, true, nil
}

// turnEndAttention classifies why a turn ended from its final message: a
// message that reads as a question needs an answer (urgent); any other
// finished turn is an unseen result (soft waiting) until the human views
// it, replies, or marks it seen. Providers have no "asked a question"
// event — completion and question arrive as the same signal — so the
// content is the only instant discriminator.
func turnEndAttention(message string) agent.Attention {
	if asksQuestion(message) {
		return agent.AttentionQuestion
	}
	return agent.AttentionWaiting
}

// asksQuestion reports whether the final non-empty line of a message ends
// with a question mark, tolerating markdown emphasis and quote trailers.
func asksQuestion(message string) bool {
	lines := strings.Split(message, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		line = strings.TrimRight(line, " \t*_`\"')")
		return strings.HasSuffix(line, "?")
	}
	return false
}

func parseClaudeEvent(payload []byte) (Event, bool, error) {
	var hook struct {
		Name                 string `json:"hook_event_name"`
		Prompt               string `json:"prompt"`
		Message              string `json:"message"`
		NotificationType     string `json:"notification_type"`
		LastAssistantMessage string `json:"last_assistant_message"`
		TranscriptPath       string `json:"transcript_path"`
	}
	if err := json.Unmarshal(payload, &hook); err != nil {
		return Event{}, false, fmt.Errorf("decode Claude hook event: %w", err)
	}

	switch hook.Name {
	case "UserPromptSubmit":
		return Event{
			Activity:       agent.ActivityWorking,
			Summary:        eventSummary(hook.Prompt),
			TranscriptPath: hook.TranscriptPath,
		}, true, nil
	case "Notification":
		if hook.NotificationType == "idle_prompt" {
			// Turn-end events already mark unseen results the moment they
			// land; acting on this delayed echo would re-raise attention
			// the human has since cleared.
			return Event{}, false, nil
		}
		summary := eventSummary(hook.Message)
		if summary == "" {
			summary = "Needs approval"
		}
		return Event{
			Activity:       agent.ActivityIdle,
			Attention:      agent.AttentionApproval,
			Summary:        summary,
			TranscriptPath: hook.TranscriptPath,
		}, true, nil
	case "Stop":
		return Event{
			Activity:       agent.ActivityIdle,
			Attention:      turnEndAttention(hook.LastAssistantMessage),
			Summary:        eventSummary(hook.LastAssistantMessage),
			TranscriptPath: hook.TranscriptPath,
			TurnEnded:      true,
		}, true, nil
	default:
		return Event{}, false, nil
	}
}

func eventSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxEventSummaryRunes {
		return value
	}
	return string(runes[:maxEventSummaryRunes-1]) + "…"
}
