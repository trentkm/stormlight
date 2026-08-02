package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/pending"
)

const maxPermissionDetailRunes = 6000

type PermissionBridge struct {
	Action        pending.Action
	suggestions   []json.RawMessage
	questionInput json.RawMessage
	question      *claudeQuestion
}

func ParsePermissionRequest(
	providerID agent.Provider,
	agentID string,
	payload []byte,
) (PermissionBridge, error) {
	switch providerID {
	case agent.ProviderClaude:
		return parseClaudePermissionRequest(agentID, payload)
	default:
		return PermissionBridge{}, fmt.Errorf(
			"unsupported permission provider %q",
			providerID,
		)
	}
}

func (p PermissionBridge) Response(
	resolution pending.Resolution,
) ([]byte, bool, error) {
	if resolution.ActionID != p.Action.ID && p.Action.ID != "" {
		return nil, false, fmt.Errorf("permission resolution does not match request")
	}

	decision := claudePermissionDecision{}
	switch {
	case strings.HasPrefix(resolution.OptionID, pending.OptionChoicePrefix):
		updated, err := p.answeredQuestionInput(resolution.OptionID)
		if err != nil {
			return nil, false, err
		}
		decision.Behavior = "allow"
		decision.UpdatedInput = updated
	case resolution.OptionID == pending.OptionAllowOnce:
		decision.Behavior = "allow"
	case strings.HasPrefix(resolution.OptionID, pending.OptionAlwaysPrefix):
		index, err := strconv.Atoi(strings.TrimPrefix(
			resolution.OptionID,
			pending.OptionAlwaysPrefix,
		))
		if err != nil || index < 0 || index >= len(p.suggestions) {
			return nil, false, fmt.Errorf("invalid always-allow permission option")
		}
		decision.Behavior = "allow"
		decision.UpdatedPermissions = []json.RawMessage{p.suggestions[index]}
	case resolution.OptionID == pending.OptionDeny:
		decision.Behavior = "deny"
		decision.Message = "The user denied this request in Stormlight."
	case resolution.OptionID == pending.OptionTerminal:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf(
			"unsupported permission option %q",
			resolution.OptionID,
		)
	}

	output := claudePermissionOutput{}
	output.HookSpecificOutput.HookEventName = "PermissionRequest"
	output.HookSpecificOutput.Decision = decision
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, false, fmt.Errorf("encode Claude permission response: %w", err)
	}
	return encoded, true, nil
}

type claudePermissionInput struct {
	SessionID             string            `json:"session_id"`
	Cwd                   string            `json:"cwd"`
	HookEventName         string            `json:"hook_event_name"`
	ToolName              string            `json:"tool_name"`
	ToolInput             json.RawMessage   `json:"tool_input"`
	PermissionSuggestions []json.RawMessage `json:"permission_suggestions"`
}

type claudePermissionOutput struct {
	HookSpecificOutput struct {
		HookEventName string                   `json:"hookEventName"`
		Decision      claudePermissionDecision `json:"decision"`
	} `json:"hookSpecificOutput"`
}

type claudePermissionDecision struct {
	Behavior           string            `json:"behavior"`
	UpdatedInput       json.RawMessage   `json:"updatedInput,omitempty"`
	UpdatedPermissions []json.RawMessage `json:"updatedPermissions,omitempty"`
	Message            string            `json:"message,omitempty"`
}

func parseClaudePermissionRequest(
	agentID string,
	payload []byte,
) (PermissionBridge, error) {
	var input claudePermissionInput
	if err := json.Unmarshal(payload, &input); err != nil {
		return PermissionBridge{}, fmt.Errorf(
			"decode Claude permission request: %w",
			err,
		)
	}
	if input.HookEventName != "PermissionRequest" {
		return PermissionBridge{}, fmt.Errorf(
			"unexpected Claude hook event %q",
			input.HookEventName,
		)
	}
	input.ToolName = compactPermissionText(input.ToolName)
	if input.ToolName == "" {
		return PermissionBridge{}, fmt.Errorf(
			"Claude permission request is missing a tool name",
		)
	}

	if input.ToolName == "AskUserQuestion" {
		if bridge, ok := parseClaudeQuestionRequest(agentID, input); ok {
			return bridge, nil
		}
	}

	description, detail := describeClaudeToolInput(input.ToolName, input.ToolInput)
	options := []pending.Option{{
		ID:       pending.OptionAllowOnce,
		Label:    "Allow once",
		Shortcut: "y",
	}}
	for index, suggestion := range input.PermissionSuggestions {
		options = append(options, pending.Option{
			ID: pending.OptionAlwaysPrefix + strconv.Itoa(index),
			Label: permissionSuggestionLabel(
				suggestion,
				input.ToolName,
			),
			Shortcut: firstShortcut(index, "a"),
		})
	}
	options = append(options,
		pending.Option{
			ID:       pending.OptionDeny,
			Label:    "Deny",
			Shortcut: "n",
		},
		pending.Option{
			ID:       pending.OptionTerminal,
			Label:    "Review in terminal",
			Shortcut: "t",
		},
	)

	return PermissionBridge{
		Action: pending.Action{
			AgentID:     agentID,
			Provider:    agent.ProviderClaude,
			Kind:        pending.KindApproval,
			Title:       "Claude requests " + input.ToolName,
			Description: description,
			Detail:      detail,
			ToolName:    input.ToolName,
			Cwd:         compactPermissionText(input.Cwd),
			Options:     options,
		},
		suggestions: input.PermissionSuggestions,
	}, nil
}

type claudeQuestionInput struct {
	Questions []claudeQuestion `json:"questions"`
}

type claudeQuestion struct {
	Question    string                 `json:"question"`
	Header      string                 `json:"header"`
	MultiSelect bool                   `json:"multiSelect"`
	Options     []claudeQuestionOption `json:"options"`
}

type claudeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// parseClaudeQuestionRequest maps an AskUserQuestion permission request onto
// a question action whose options are the question's own choices. It reports
// false when the payload does not match the single-select, single-question
// shape Stormlight can answer, leaving the generic approval flow in place.
func parseClaudeQuestionRequest(
	agentID string,
	input claudePermissionInput,
) (PermissionBridge, bool) {
	var question claudeQuestionInput
	if err := json.Unmarshal(input.ToolInput, &question); err != nil {
		return PermissionBridge{}, false
	}
	if len(question.Questions) != 1 || question.Questions[0].MultiSelect {
		return PermissionBridge{}, false
	}
	first := question.Questions[0]
	title := compactPermissionText(first.Question)
	if title == "" || len(first.Options) == 0 {
		return PermissionBridge{}, false
	}

	options := make([]pending.Option, 0, len(first.Options)+1)
	descriptions := make([]string, 0, len(first.Options))
	for index, choice := range first.Options {
		label := compactPermissionText(choice.Label)
		if label == "" {
			return PermissionBridge{}, false
		}
		options = append(options, pending.Option{
			ID:       pending.OptionChoicePrefix + strconv.Itoa(index),
			Label:    label,
			Shortcut: questionShortcut(index),
		})
		description := compactPermissionText(choice.Description)
		if description != "" {
			descriptions = append(descriptions, label+" — "+description)
		}
	}
	options = append(options, pending.Option{
		ID:       pending.OptionTerminal,
		Label:    "Answer in terminal",
		Shortcut: "t",
	})

	return PermissionBridge{
		Action: pending.Action{
			AgentID:     agentID,
			Provider:    agent.ProviderClaude,
			Kind:        pending.KindQuestion,
			Title:       title,
			Description: compactPermissionText(first.Header),
			Detail:      boundedPermissionDetail(strings.Join(descriptions, "\n")),
			ToolName:    input.ToolName,
			Cwd:         compactPermissionText(input.Cwd),
			Options:     options,
		},
		questionInput: input.ToolInput,
		question:      &first,
	}, true
}

// answeredQuestionInput echoes the original AskUserQuestion tool input with
// an answers object mapping the question text to the chosen option label,
// which is how the PermissionRequest hook contract selects an option.
func (p PermissionBridge) answeredQuestionInput(
	optionID string,
) (json.RawMessage, error) {
	index, err := strconv.Atoi(strings.TrimPrefix(
		optionID,
		pending.OptionChoicePrefix,
	))
	if p.question == nil || err != nil ||
		index < 0 || index >= len(p.question.Options) {
		return nil, fmt.Errorf("invalid question choice option")
	}

	var input map[string]any
	if err := json.Unmarshal(p.questionInput, &input); err != nil {
		return nil, fmt.Errorf("decode AskUserQuestion input: %w", err)
	}
	input["answers"] = map[string]string{
		p.question.Question: p.question.Options[index].Label,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode AskUserQuestion answer: %w", err)
	}
	return encoded, nil
}

func questionShortcut(index int) string {
	if index < 9 {
		return strconv.Itoa(index + 1)
	}
	return ""
}

func describeClaudeToolInput(
	toolName string,
	raw json.RawMessage,
) (string, string) {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return "Allow this tool call?", boundedPermissionDetail(string(raw))
	}

	description, _ := input["description"].(string)
	description = compactPermissionText(description)
	switch toolName {
	case "Bash":
		command, _ := input["command"].(string)
		if description == "" {
			description = "Run this command?"
		}
		return description, boundedPermissionDetail(command)
	case "Edit", "Write", "NotebookEdit":
		path, _ := input["file_path"].(string)
		if path == "" {
			path, _ = input["notebook_path"].(string)
		}
		if description == "" && path != "" {
			description = "Modify " + compactPermissionText(path) + "?"
		}
	}
	if description == "" {
		description = "Allow this tool call?"
	}

	formatted, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		formatted = raw
	}
	return description, boundedPermissionDetail(string(formatted))
}

func permissionSuggestionLabel(raw json.RawMessage, toolName string) string {
	var suggestion struct {
		Type        string `json:"type"`
		Behavior    string `json:"behavior"`
		Destination string `json:"destination"`
		Rules       []struct {
			ToolName    string `json:"toolName"`
			RuleContent string `json:"ruleContent"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &suggestion); err != nil {
		return "Always allow " + toolName
	}

	targets := make([]string, 0, len(suggestion.Rules))
	for _, rule := range suggestion.Rules {
		target := compactPermissionText(rule.ToolName)
		if target == "" {
			target = toolName
		}
		if content := compactPermissionText(rule.RuleContent); content != "" {
			target += " (" + content + ")"
		}
		targets = append(targets, target)
	}
	target := toolName
	if len(targets) > 0 {
		target = strings.Join(targets, ", ")
	}

	scope := ""
	switch suggestion.Destination {
	case "session":
		scope = " for this session"
	case "localSettings", "projectSettings":
		scope = " for this project"
	case "userSettings":
		scope = " for all projects"
	}
	return "Always allow " + target + scope
}

func boundedPermissionDetail(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxPermissionDetailRunes {
		return value
	}
	return string(runes[:maxPermissionDetailRunes-1]) + "…"
}

func compactPermissionText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func firstShortcut(index int, shortcut string) string {
	if index == 0 {
		return shortcut
	}
	return ""
}
