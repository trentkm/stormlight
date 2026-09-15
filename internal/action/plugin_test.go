package action

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func writePlugin(t *testing.T, directory, name, source string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrepareRunsTheInstalledPluginBesideTheAgent(t *testing.T) {
	plugins := t.TempDir()
	cwd := t.TempDir()
	writePlugin(t, plugins, "review-diff", `#!/bin/sh
printf '{"phase":"%s","cwd":"%s","argument":"%s"}\n' "$1" "$PWD" "$2"
`)

	payload, err := NewRegistryAt(plugins).Prepare(
		context.Background(),
		"review-diff",
		cwd,
		[]string{"source tree"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	if value["phase"] != "prepare" ||
		value["cwd"] != cwd ||
		value["argument"] != "source tree" {
		t.Fatalf("payload = %#v", value)
	}
}

func TestPrepareRejectsInvalidJSON(t *testing.T) {
	plugins := t.TempDir()
	writePlugin(t, plugins, "broken", "#!/bin/sh\nprintf 'not json'\n")
	_, err := NewRegistryAt(plugins).Prepare(
		context.Background(),
		"broken",
		t.TempDir(),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestHandlePassesAnOpaquePayloadAndAgentContext(t *testing.T) {
	plugins := t.TempDir()
	captured := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("CAPTURE_ACTION_REQUEST", captured)
	writePlugin(t, plugins, "review-diff", `#!/bin/sh
test "$1" = handle || exit 9
cat > "$CAPTURE_ACTION_REQUEST"
`)

	request := agent.DashboardActionRequest{
		ID:      "request-one",
		Name:    "review-diff",
		Payload: json.RawMessage(`{"paths":["/remote/repo"]}`),
	}
	managedAgent := agent.Agent{
		ID:   "agent-one",
		Name: "review fixer",
		Cwd:  "/remote/repo",
		Host: "cloud",
	}
	if err := NewRegistryAt(plugins).Handle(
		context.Background(),
		managedAgent,
		request,
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	var input HandleInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if input.Protocol != ProtocolVersion ||
		input.Action != request.Name ||
		input.Host != "cloud" ||
		input.Agent.ID != managedAgent.ID ||
		input.Agent.Name != managedAgent.Name ||
		input.Agent.Cwd != managedAgent.Cwd ||
		string(input.Payload) != string(request.Payload) {
		t.Fatalf("handler input = %#v", input)
	}
}

func TestPluginNamesCannotEscapeTheActionsDirectory(t *testing.T) {
	for _, name := range []string{
		"",
		"../run",
		"Review",
		"-leading",
		"réview",
		strings.Repeat("a", 65),
	} {
		_, err := NewRegistryAt(t.TempDir()).Prepare(
			context.Background(),
			name,
			t.TempDir(),
			nil,
		)
		if err == nil {
			t.Fatalf("name %q was accepted", name)
		}
	}
}

func TestPluginMustBeAnExecutableFile(t *testing.T) {
	plugins := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(plugins, "review"),
		[]byte("#!/bin/sh\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, err := NewRegistryAt(plugins).Prepare(
		context.Background(),
		"review",
		t.TempDir(),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginFailureIncludesItsDiagnostic(t *testing.T) {
	plugins := t.TempDir()
	writePlugin(t, plugins, "review", `#!/bin/sh
printf 'repository has no changes\n' >&2
exit 7
`)
	_, err := NewRegistryAt(plugins).Prepare(
		context.Background(),
		"review",
		t.TempDir(),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "repository has no changes") {
		t.Fatalf("error = %v", err)
	}
}

func TestActionDirectoryHonorsXDGAndItsExplicitOverride(t *testing.T) {
	t.Setenv("STORMLIGHT_ACTIONS_DIR", "")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	want := filepath.Join(configHome, "stormlight", "actions")
	if got := actionDirectory(); got != want {
		t.Fatalf("directory = %q, want %q", got, want)
	}

	t.Setenv("STORMLIGHT_ACTIONS_DIR", "/tmp/custom-actions")
	if got := actionDirectory(); got != "/tmp/custom-actions" {
		t.Fatalf("override = %q", got)
	}
}
