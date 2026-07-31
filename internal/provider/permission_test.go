package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trentkm/runstead/internal/agent"
	"github.com/trentkm/runstead/internal/pending"
)

const permissionActionID = "0123456789abcdef0123456789abcdef"

func TestParseClaudePermissionRequestBuildsApprovalAction(t *testing.T) {
	bridge, err := ParsePermissionRequest(
		agent.ProviderClaude,
		"agent-1",
		claudePermissionPayload(),
	)
	if err != nil {
		t.Fatal(err)
	}
	action := bridge.Action
	if action.AgentID != "agent-1" ||
		action.Provider != agent.ProviderClaude ||
		action.Kind != pending.KindApproval ||
		action.ToolName != "Bash" ||
		action.Cwd != "/tmp/project" {
		t.Fatalf("action = %#v", action)
	}
	if !strings.Contains(action.Title, "Bash") ||
		!strings.Contains(action.Description, "tests") ||
		action.Detail != "go test ./..." {
		t.Fatalf("approval copy = %#v", action)
	}
	if len(action.Options) != 4 {
		t.Fatalf("options = %#v", action.Options)
	}
	if action.Options[0].ID != pending.OptionAllowOnce ||
		action.Options[1].ID != pending.OptionAlwaysPrefix+"0" ||
		action.Options[2].ID != pending.OptionDeny ||
		action.Options[3].ID != pending.OptionTerminal {
		t.Fatalf("option order = %#v", action.Options)
	}
	if !strings.Contains(action.Options[1].Label, "this project") {
		t.Fatalf("always-allow label = %q", action.Options[1].Label)
	}
}

func TestClaudePermissionResponsesFollowHookContract(t *testing.T) {
	bridge, err := ParsePermissionRequest(
		agent.ProviderClaude,
		"agent-1",
		claudePermissionPayload(),
	)
	if err != nil {
		t.Fatal(err)
	}
	bridge.Action.ID = permissionActionID

	tests := []struct {
		name         string
		optionID     string
		wantBehavior string
		wantUpdated  int
		wantMessage  bool
		wantHandled  bool
	}{
		{
			name:         "allow once",
			optionID:     pending.OptionAllowOnce,
			wantBehavior: "allow",
			wantHandled:  true,
		},
		{
			name:         "always allow",
			optionID:     pending.OptionAlwaysPrefix + "0",
			wantBehavior: "allow",
			wantUpdated:  1,
			wantHandled:  true,
		},
		{
			name:         "deny",
			optionID:     pending.OptionDeny,
			wantBehavior: "deny",
			wantMessage:  true,
			wantHandled:  true,
		},
		{
			name:        "terminal fallback",
			optionID:    pending.OptionTerminal,
			wantHandled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, handled, err := bridge.Response(pending.Resolution{
				ActionID: permissionActionID,
				OptionID: test.optionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if handled != test.wantHandled {
				t.Fatalf("handled = %t, want %t", handled, test.wantHandled)
			}
			if !handled {
				if output != nil {
					t.Fatalf("fallback output = %q", output)
				}
				return
			}

			var decoded claudePermissionOutput
			if err := json.Unmarshal(output, &decoded); err != nil {
				t.Fatal(err)
			}
			hookOutput := decoded.HookSpecificOutput
			if hookOutput.HookEventName != "PermissionRequest" ||
				hookOutput.Decision.Behavior != test.wantBehavior ||
				len(hookOutput.Decision.UpdatedPermissions) != test.wantUpdated ||
				(hookOutput.Decision.Message != "") != test.wantMessage {
				t.Fatalf("response = %#v", decoded)
			}
		})
	}
}

func TestPermissionResponseRejectsMismatchedAction(t *testing.T) {
	bridge, err := ParsePermissionRequest(
		agent.ProviderClaude,
		"agent-1",
		claudePermissionPayload(),
	)
	if err != nil {
		t.Fatal(err)
	}
	bridge.Action.ID = permissionActionID
	_, _, err = bridge.Response(pending.Resolution{
		ActionID: "fedcba9876543210fedcba9876543210",
		OptionID: pending.OptionAllowOnce,
	})
	if err == nil {
		t.Fatal("expected a mismatched-action error")
	}
}

func TestParsePermissionRequestRejectsUnsupportedProvider(t *testing.T) {
	_, err := ParsePermissionRequest(
		agent.ProviderCodex,
		"agent-1",
		claudePermissionPayload(),
	)
	if err == nil {
		t.Fatal("expected an unsupported-provider error")
	}
}

func claudePermissionPayload() []byte {
	return []byte(`{
		"session_id": "session-1",
		"cwd": "/tmp/project",
		"hook_event_name": "PermissionRequest",
		"tool_name": "Bash",
		"tool_input": {
			"command": "go test ./...",
			"description": "Run focused tests"
		},
		"permission_suggestions": [{
			"type": "addRules",
			"behavior": "allow",
			"destination": "localSettings",
			"rules": [{
				"toolName": "Bash",
				"ruleContent": "go test:*"
			}]
		}]
	}`)
}
