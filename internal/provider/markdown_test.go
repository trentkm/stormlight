package provider

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/trentkm/stormlight/internal/theme"
)

// runSGR returns the SGR parameters in effect over want. It walks the text
// runs rather than searching the string, because the parameters themselves
// are digits: looking for "1" in the raw output finds it inside a color.
//
// Every run is emitted as <SGR>text<reset>, which is also what lets the
// Spanreed pane reopen a run that wrapping cut in half.
func runSGR(t *testing.T, rendered, want string) string {
	t.Helper()
	active := ""
	for rest := rendered; rest != ""; {
		if strings.HasPrefix(rest, "\x1b[") {
			end := strings.IndexByte(rest, 'm')
			if end < 0 {
				break
			}
			active = rest[2:end]
			rest = rest[end+1:]
			continue
		}
		text, next := rest, strings.Index(rest[1:], "\x1b[")
		if next >= 0 {
			text = rest[:next+1]
		}
		if strings.Contains(text, want) {
			return active
		}
		if next < 0 {
			break
		}
		rest = rest[next+1:]
	}
	t.Fatalf("no styled run carries %q in %q", want, rendered)
	return ""
}

// paletteSGR is the foreground sequence a palette color reaches the terminal
// as, resolved the same way the renderer resolves it.
func paletteSGR(color lipgloss.TerminalColor) string {
	return termenv.TrueColor.Color(theme.Hex(color)).Sequence(false)
}

func TestRenderMarkdownReadsBackMarkdown(t *testing.T) {
	withColor(t)
	rendered := renderMarkdown("## Plan\n" +
		"Call `parse()` and make it **fast**, but *only* if needed.\n" +
		"See [the docs](https://example.com) and ~~ignore~~ that.\n")

	for _, want := range []struct{ text, sgr, why string }{
		{"Plan", paletteSGR(theme.Accent) + ";1", "heading"},
		{"parse()", paletteSGR(theme.Code), "inline literal"},
		{"fast", paletteSGR(theme.Text) + ";1", "strong"},
		{"only", paletteSGR(theme.Text) + ";3", "emphasis"},
		{"ignore", paletteSGR(theme.Text) + ";9", "strikethrough"},
		{"the docs", paletteSGR(theme.Working), "link text"},
		{"https://example.com", paletteSGR(theme.Accent) + ";4", "link"},
	} {
		if got := runSGR(t, rendered, want.text); got != want.sgr {
			t.Errorf("%s %q: SGR %q, want %q", want.why, want.text, got, want.sgr)
		}
	}

	// Delimiters were instructions to a renderer, and this is the renderer.
	// Each of these leaked through the hand-rolled painter this replaced.
	plain := ansi.Strip(rendered)
	for _, delimiter := range []string{"**", "*only*", "~~", "`", "[the docs]", "(https"} {
		if strings.Contains(plain, delimiter) {
			t.Errorf("delimiter %q survived in %q", delimiter, plain)
		}
	}
	for _, want := range []string{"Plan", "parse()", "fast", "only", "ignore", "the docs"} {
		if !strings.Contains(plain, want) {
			t.Errorf("content %q was dropped from %q", want, plain)
		}
	}
}

func TestRenderMarkdownPaintsFencedCodeFromThePalette(t *testing.T) {
	withColor(t)
	rendered := renderMarkdown("```go\nx := 1 // note\n```\n")

	for _, want := range []struct{ text, sgr, why string }{
		{"x", paletteSGR(theme.Code), "identifier"},
		{"1", paletteSGR(theme.Waiting), "number"},
		{"// note", paletteSGR(theme.Muted), "comment"},
	} {
		if got := runSGR(t, rendered, want.text); got != want.sgr {
			t.Errorf("%s %q: SGR %q, want %q", want.why, want.text, got, want.sgr)
		}
	}
	if plain := ansi.Strip(rendered); strings.Contains(plain, "```") {
		t.Errorf("fence survived in %q", plain)
	}
}

// Wrapping happens in the Spanreed pane, against the current column width,
// so the renderer must leave each block on one line and must not pad rows
// out to a wrap width.
func TestRenderMarkdownLeavesWrappingToThePane(t *testing.T) {
	withColor(t)
	long := strings.Repeat("a fairly long clause that keeps going ", 8)
	rendered := ansi.Strip(renderMarkdown(long))
	if lines := strings.Split(strings.TrimSpace(rendered), "\n"); len(lines) != 1 {
		t.Fatalf("paragraph was wrapped into %d lines: %q", len(lines), rendered)
	}
	if strings.HasSuffix(rendered, "  ") {
		t.Errorf("row was padded to a wrap width: %q", rendered)
	}
}

// A message that is not markdown at all still has to render.
func TestRenderMarkdownPassesPlainProseThrough(t *testing.T) {
	withColor(t)
	rendered := ansi.Strip(renderMarkdown("Just a sentence."))
	if strings.TrimSpace(rendered) != "Just a sentence." {
		t.Errorf("plain prose was mangled: %q", rendered)
	}
}
