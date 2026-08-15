package main

import (
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func TestRootCommandUsesStormlightIdentity(t *testing.T) {
	command := newRootCommand()
	if command.Use != "stormlight [path]" {
		t.Fatalf("command use = %q", command.Use)
	}
}

func TestOpenWorkspacePathRequiresDirectory(t *testing.T) {
	directory := t.TempDir()
	resolved, err := openWorkspacePath(directory)
	if err != nil || resolved != directory {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
	if _, err := openWorkspacePath("/definitely/not/a/dir"); err == nil {
		t.Fatal("missing directory was accepted")
	}
}

func TestParseActivityAcceptsKnownStatesAndRejectsOthers(t *testing.T) {
	for _, value := range []string{
		"", "starting", "working", "idle", "completed", "failed", "stopped",
	} {
		got, err := parseActivity(value)
		if err != nil {
			t.Fatalf("parseActivity(%q) = %v", value, err)
		}
		if got != agent.Activity(value) {
			t.Fatalf("parseActivity(%q) = %q", value, got)
		}
	}

	for _, value := range []string{"running", "Working", "done", "idle "} {
		if _, err := parseActivity(value); err == nil {
			t.Fatalf("parseActivity(%q) was accepted", value)
		}
	}
}

func TestParseAttentionTreatsNoneAndEmptyAsCleared(t *testing.T) {
	for _, value := range []string{"none", ""} {
		got, err := parseAttention(value)
		if err != nil {
			t.Fatalf("parseAttention(%q) = %v", value, err)
		}
		if got != agent.AttentionNone {
			t.Fatalf("parseAttention(%q) = %q, want cleared", value, got)
		}
	}

	for _, value := range []string{"question", "approval", "auth", "waiting"} {
		got, err := parseAttention(value)
		if err != nil {
			t.Fatalf("parseAttention(%q) = %v", value, err)
		}
		if got != agent.Attention(value) {
			t.Fatalf("parseAttention(%q) = %q", value, got)
		}
	}

	for _, value := range []string{"urgent", "Question", "blocked"} {
		if _, err := parseAttention(value); err == nil {
			t.Fatalf("parseAttention(%q) was accepted", value)
		}
	}
}

func TestShortIDTruncatesToEightCharacters(t *testing.T) {
	for _, testCase := range []struct{ id, want string }{
		{"", ""},
		{"abc", "abc"},
		{"12345678", "12345678"},
		{"123456789", "12345678"},
	} {
		if got := shortID(testCase.id); got != testCase.want {
			t.Fatalf("shortID(%q) = %q, want %q", testCase.id, got, testCase.want)
		}
	}
}

func TestTruncatePlainCollapsesWhitespaceAndCountsRunes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		value  string
		length int
		want   string
	}{
		{"shorter than limit", "hello", 10, "hello"},
		{"exactly the limit", "abcde", 5, "abcde"},
		{"collapses runs of whitespace", "  a \n  b\tc  ", 10, "a b c"},
		{"ellipsis replaces the final rune", "abcdefghij", 5, "abcd…"},
		{"limit of one takes a bare rune", "abcde", 1, "a"},
		{"limit of zero is empty", "abcde", 0, ""},
		{"multibyte counted as runes not bytes", "héllo wörld", 5, "héll…"},
		{"wide runes are not split", "🌊🌊🌊🌊", 3, "🌊🌊…"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := truncatePlain(testCase.value, testCase.length)
			if got != testCase.want {
				t.Fatalf("truncatePlain(%q, %d) = %q, want %q",
					testCase.value, testCase.length, got, testCase.want)
			}
		})
	}
}
