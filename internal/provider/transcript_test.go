package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// withColor forces a color profile for the default renderer: tests run
// without a terminal, where lipgloss renders everything plain.
func withColor(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
}

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

	styled, ok := RenderClaudeTranscript(path)
	if !ok {
		t.Fatal("transcript was not rendered")
	}
	rendered := ansi.Strip(styled)
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

func TestRenderClaudeTranscriptPaintsConversation(t *testing.T) {
	withColor(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"fix the parser"}}`,
		`{"type":"assistant","message":{"content":[` +
			`{"type":"text","text":"## Plan\nCall ` + "`parse()`" +
			` and make it **fast**.\n` + "```go\\nx := 1\\n```" + `"},` +
			`{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result",` +
			`"content":"ok"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rendered, ok := RenderClaudeTranscript(path)
	if !ok {
		t.Fatal("transcript was not rendered")
	}
	byText := map[string]string{}
	for _, line := range strings.Split(rendered, "\n") {
		byText[strings.TrimSpace(ansi.Strip(line))] = line
	}
	for _, want := range []struct{ text, styled string }{
		{"❯ fix the parser", promptMarkStyle.Render("❯ ")},
		{"⏺ Plan", headingStyle.Render("Plan")},
		{"Call parse() and make it fast.", codeStyle.Render("parse()")},
		{"Call parse() and make it fast.", boldStyle.Render("fast")},
		{"x := 1", codeStyle.Render("x := 1")},
		{"⏺ Bash(go test ./...)", toolNameStyle.Render("Bash")},
		{"⎿ ok", resultStyle.Render("  ⎿ ok")},
	} {
		line, ok := byText[want.text]
		if !ok {
			t.Fatalf("missing line %q in:\n%q", want.text, rendered)
		}
		if !strings.Contains(line, want.styled) {
			t.Fatalf("line %q is unpainted: %q, want %q",
				want.text, line, want.styled)
		}
	}
	// Markdown delimiters were instructions to a renderer, not content.
	if strings.Contains(ansi.Strip(rendered), "**") {
		t.Fatalf("emphasis delimiters survived:\n%s", ansi.Strip(rendered))
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
