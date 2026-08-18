package windrun

import (
	"slices"
	"strings"
	"testing"
)

// TestAgentEnvironLeavesOneEntryPerName: dispatch appends its variables
// onto the inherited environment, and an inherited entry of the same name
// left in place means two entries for one variable. POSIX does not say
// which a child reads — a shell here resolves the last, another libc may
// resolve the first — so an agent dispatched from inside another agent
// could be handed its parent's STORMLIGHT_ID, and its hooks would report
// the wrong agent on some platforms and not others.
func TestAgentEnvironLeavesOneEntryPerName(t *testing.T) {
	t.Setenv("STORMLIGHT_ID", "the-parent-agent")
	t.Setenv("WINDRUNNER_DIR", "/parents/socket/dir")
	t.Setenv("PATH", "/usr/bin")

	environ := agentEnviron([]string{
		"STORMLIGHT_ID=the-new-agent",
		"WINDRUNNER_DIR=/its/own/socket/dir",
	})

	if count := countName(environ, "STORMLIGHT_ID"); count != 1 {
		t.Fatalf("STORMLIGHT_ID appears %d times, want 1: %v", count, environ)
	}
	if !slices.Contains(environ, "STORMLIGHT_ID=the-new-agent") {
		t.Fatalf("the new id must win: %v", environ)
	}
	if !slices.Contains(environ, "WINDRUNNER_DIR=/its/own/socket/dir") {
		t.Fatalf("the new socket dir must win: %v", environ)
	}
	if !slices.Contains(environ, "PATH=/usr/bin") {
		t.Fatalf("the rest of the environment must survive: %v", environ)
	}
}

// TestAgentEnvironDropsAnotherSessionsIdentity: the daemon is auto-started
// from whatever shell first needed it, including a Claude Code session's
// own tool shell. Those variables would enlist every agent into that
// session — child-session mode, its messaging socket and token, its
// session id. An agent is nobody's child.
func TestAgentEnvironDropsAnotherSessionsIdentity(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SSE_PORT", "4242")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("PATH", "/usr/bin")

	environ := agentEnviron([]string{"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1"})

	for _, unwanted := range []string{"CLAUDE_CODE_SSE_PORT", "CLAUDECODE", "ANTHROPIC_API_KEY"} {
		if countName(environ, unwanted) != 0 {
			t.Fatalf("%s should not reach an agent: %v", unwanted, environ)
		}
	}
	// The ones dispatch sets deliberately still arrive.
	if !slices.Contains(environ, "CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1") {
		t.Fatalf("deliberate settings must survive the scrub: %v", environ)
	}
	if !slices.Contains(environ, "PATH=/usr/bin") {
		t.Fatalf("the rest of the environment must survive: %v", environ)
	}
}

func countName(environ []string, name string) int {
	count := 0
	for _, entry := range environ {
		if entryName, _, _ := strings.Cut(entry, "="); entryName == name {
			count++
		}
	}
	return count
}
