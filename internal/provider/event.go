package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trentkm/runstead/internal/agent"
)

const maxEventSummaryRunes = 160

type Event struct {
	Activity  agent.Activity
	Attention agent.Attention
	Summary   string
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
		Activity: agent.ActivityIdle,
		Summary:  eventSummary(notification.LastAssistantMessage),
	}, true, nil
}

func parseClaudeEvent(payload []byte) (Event, bool, error) {
	var hook struct {
		Name                 string `json:"hook_event_name"`
		Prompt               string `json:"prompt"`
		Message              string `json:"message"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(payload, &hook); err != nil {
		return Event{}, false, fmt.Errorf("decode Claude hook event: %w", err)
	}

	switch hook.Name {
	case "UserPromptSubmit":
		return Event{
			Activity: agent.ActivityWorking,
			Summary:  eventSummary(hook.Prompt),
		}, true, nil
	case "Notification":
		summary := eventSummary(hook.Message)
		if summary == "" {
			summary = "Needs approval"
		}
		return Event{
			Activity:  agent.ActivityIdle,
			Attention: agent.AttentionApproval,
			Summary:   summary,
		}, true, nil
	case "Stop":
		return Event{
			Activity: agent.ActivityIdle,
			Summary:  eventSummary(hook.LastAssistantMessage),
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
