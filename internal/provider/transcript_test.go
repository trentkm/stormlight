package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderClaudeTranscriptRendersConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := []string{
		`{"type":"mode","mode":4}`,
		`{"type":"user","message":{"role":"user","content":"fix the parser\nplease"}}`,
		`{"type":"assistant","message":{"content":[` +
			`{"type":"thinking","thinking":"hmm"},` +
			`{"type":"text","text":"Looking at the parser now."},` +
			`{"type":"tool_use","name":"Bash","input":{"command":"go test ./parser/"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result",` +
			`"content":"line one\nline two\nline three\nline four\nline five"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"meta noise"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rendered, ok := RenderClaudeTranscript(path)
	if !ok {
		t.Fatal("transcript was not rendered")
	}
	for _, want := range []string{
		"❯ fix the parser\n  please",
		"⏺ Looking at the parser now.",
		"⏺ Bash(go test ./parser/)",
		"  ⎿ line one",
		"    line three",
		"    … +2 lines",
		"⏺ Done.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "meta noise") || strings.Contains(rendered, "hmm") {
		t.Fatalf("meta or thinking content leaked:\n%s", rendered)
	}
}

func TestRenderClaudeTranscriptFallsBackWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"mode","mode":4}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := RenderClaudeTranscript(path); ok {
		t.Fatal("empty transcript reported ok")
	}
	if _, ok := RenderClaudeTranscript(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("missing file reported ok")
	}
}
