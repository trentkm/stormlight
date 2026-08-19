package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/theme"
)

// withColor marks the tests that assert exact color sequences.
//
// It used to force a profile. v1 rendered through a global renderer that
// downsampled to whatever profile it had detected, so with no terminal
// attached it dropped color entirely and these tests saw plain text. v2 has
// no global profile — a Style always emits full-fidelity ANSI and
// downsampling moved to Bubble Tea's renderer, which is not in play here —
// so there is nothing left to force.
func withColor(t *testing.T) { t.Helper() }

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
		{"❯ fix the parser", promptMarkStyle().Render("❯ ")},
		{"⏺ Bash(go test ./...)", toolNameStyle().Render("Bash")},
		{"⎿ ok", resultStyle().Render("  ⎿ ok")},
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
	// Prose reaches the markdown renderer on the way through, and the ⏺
	// marker still lands on the first row of what it produces.
	for _, want := range []struct{ text, sgr, why string }{
		{"Plan", paletteSGR(theme.Accent) + ";1", "heading"},
		{"parse()", paletteSGR(theme.Code), "inline literal"},
		{":=", paletteSGR(theme.Code), "fenced code"},
	} {
		if got := runSGR(t, rendered, want.text); got != want.sgr {
			t.Errorf("%s %q: SGR %q, want %q", want.why, want.text, got, want.sgr)
		}
	}
	if !strings.Contains(ansi.Strip(rendered), "⏺ Plan") {
		t.Errorf("marker did not land on the first rendered row:\n%s", ansi.Strip(rendered))
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

func TestParseClaudeTranscriptEntries(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","message":{"content":"do the thing"}}`,
		`{"type":"assistant","message":{"content":[` +
			`{"type":"text","text":"On it."},` +
			`{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"one\ntwo\nthree\nfour\nfive"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"skipped"}}`,
	}, "\n")

	entries, ok := ParseClaudeTranscriptFrom(strings.NewReader(transcript))
	if !ok {
		t.Fatal("expected a conversation")
	}

	want := []TranscriptEntry{
		{Kind: "prompt", Text: "do the thing"},
		{Kind: "reply", Text: "On it."},
		{Kind: "tool", Tool: "Bash", Arg: "go test ./..."},
		{Kind: "result", Text: "one\ntwo\nthree", Hidden: 2},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %#v", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d: got %#v, want %#v", i, entries[i], want[i])
		}
	}
}

// The rendered skeleton, pinned as a literal. RenderClaudeTranscriptFrom
// is itself parse-then-render now, so comparing the two would compare a
// function with itself; what this guards is the *shape* the dashboard
// has always shown — markers, indents, trims, the +N line — with the
// ANSI styling stripped so the pin is about layout, not palette. The
// styling itself is asserted by TestRenderClaudeTranscriptPaints
// Conversation above.
var testANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(text string) string { return testANSIPattern.ReplaceAllString(text, "") }

func TestRenderTranscriptEntriesGoldenSkeleton(t *testing.T) {
	entries := []TranscriptEntry{
		{Kind: "prompt", Text: "ask\nsecond line"},
		{Kind: "reply", Text: "answer"},
		{Kind: "tool", Tool: "Read", Arg: "/f.go"},
		{Kind: "result", Text: "one\ntwo\nthree", Hidden: 4},
	}

	rendered := stripANSI(RenderTranscriptEntries(entries))

	golden := "\n" +
		"❯ ask\n" +
		"  second line\n" +
		"\n" +
		"⏺ answer\n" +
		"\n" +
		"⏺ Read(/f.go)\n" +
		"  ⎿ one\n" +
		"    two\n" +
		"    three\n" +
		"    … +4 lines\n"
	if rendered != golden {
		t.Errorf("skeleton drifted:\ngot  %q\nwant %q", rendered, golden)
	}
}
