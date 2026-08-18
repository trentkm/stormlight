package provider

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

// TestHooksSurviveAnEmptyBinaryVariable: $STORMLIGHT_BIN arrives in the
// agent's environment, and a daemon older than the field carrying it
// drops that field without a word. `exec ""` then fails as
// "exec: : not found" — an error three layers from anything explaining
// it. Any machine hosting agents has Stormlight on it by definition, so
// the name is a better last resort than nothing.
func TestHooksSurviveAnEmptyBinaryVariable(t *testing.T) {
	command := reportEvent(agent.ProviderClaude).Command
	if !strings.Contains(command, "${STORMLIGHT_BIN:-stormlight}") {
		t.Fatalf("hook command = %q", command)
	}

	// Prove the shell agrees: an unset variable resolves to the fallback,
	// and a set one still wins.
	for _, test := range []struct{ env, want string }{
		{"", "stormlight"},
		{"/opt/stormlight/bin/stormlight", "/opt/stormlight/bin/stormlight"},
	} {
		script := `printf '%s' "${STORMLIGHT_BIN:-stormlight}"`
		run := exec.Command("/bin/sh", "-c", script)
		run.Env = append(os.Environ(), "STORMLIGHT_BIN="+test.env)
		out, err := run.Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != test.want {
			t.Fatalf("STORMLIGHT_BIN=%q resolved to %q, want %q", test.env, out, test.want)
		}
	}
}
