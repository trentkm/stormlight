package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/theme"
)

// Claude Code appends every completed message of a session to a JSONL
// transcript (reported by its hooks as transcript_path). Rendering the
// conversation from that file gives Spanreed the entire session history —
// the terminal screen alone cannot: Claude runs in the alternate screen,
// so the terminal never accumulates scrollback for it.
//
// The file carries no styling, so the renderer supplies it: a transcript
// that reads as one gray slab is a worse trade than the scrollback it buys.
// Structure is colored where the JSONL states it (prompt, reply, tool call,
// result) and prose is colored where Claude's own markdown says so — see
// markdown.go for the stylesheet that reads it back.
//
// Parsing and styling are two passes on purpose. The parse produces
// TranscriptEntry values, the shared form every renderer consumes: the
// TUI paints them with lipgloss below, and the web API ships them as
// JSON for the browser to paint its own way. When they were one pass,
// the only way to read a conversation was in ANSI.

const (
	// transcriptResultLines caps how many lines of a tool result are
	// kept; results routinely run to hundreds of lines of noise.
	transcriptResultLines = 3
	// transcriptScanBuffer must fit the largest JSONL line; tool results
	// carry whole files.
	transcriptScanBuffer = 16 * 1024 * 1024
	// liveDividerText separates the rendered history from the live pane
	// capture appended while a turn is in flight.
	liveDividerText = "──── live ────"
)

// The transcript's styles are built per call rather than held in package
// variables: a palette entry cannot resolve until the terminal has answered
// which background it draws on, and a variable would capture whatever the
// palette guessed at package initialization.
func promptMarkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Accent)).Bold(true)
}

func promptTextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Text)).Bold(true)
}

func replyMarkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Done)).Bold(true)
}

func toolMarkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Working)).Bold(true)
}

func toolNameStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Text)).Bold(true)
}

func toolArgStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Muted))
}

func resultStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Muted))
}

func dividerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Border))
}

// LiveDivider is the rule drawn between the rendered transcript and the
// live pane capture.
func LiveDivider() string {
	return dividerStyle().Render(liveDividerText)
}

type transcriptLine struct {
	Type        string          `json:"type"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type transcriptMessage struct {
	Content json.RawMessage `json:"content"`
}

type transcriptBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   map[string]any  `json:"input"`
	Content json.RawMessage `json:"content"`
}

// A TranscriptEntry is one conversation event, parsed and unstyled.
type TranscriptEntry struct {
	// Kind is "prompt", "reply", "tool", or "result".
	Kind string `json:"kind"`
	// Text is the prompt, the reply's markdown, or the result's kept
	// lines. Empty for tool calls.
	Text string `json:"text,omitempty"`
	// Tool and Arg identify a tool call: the name, and the one input
	// that distinguishes it.
	Tool string `json:"tool,omitempty"`
	Arg  string `json:"arg,omitempty"`
	// Hidden counts result lines elided at parse. The trim happens
	// here, once, so every renderer shows the same conversation and
	// none holds a whole pasted file in memory.
	Hidden int `json:"hidden,omitempty"`
}

// ParseClaudeTranscript parses a Claude Code session transcript into
// conversation entries. ok is false when the file cannot be read or
// holds no conversation, so callers fall back to the pane capture.
func ParseClaudeTranscript(path string) (entries []TranscriptEntry, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	return ParseClaudeTranscriptFrom(file)
}

// ParseClaudeTranscriptFrom is ParseClaudeTranscript over an already-open
// transcript, for the ones that are not on this machine.
func ParseClaudeTranscriptFrom(source io.Reader) (entries []TranscriptEntry, ok bool) {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptScanBuffer)
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.IsMeta || line.IsSidechain {
			continue
		}
		var message transcriptMessage
		if len(line.Message) > 0 {
			_ = json.Unmarshal(line.Message, &message)
		}
		switch line.Type {
		case "user":
			entries = append(entries, parseTranscriptUser(message.Content)...)
		case "assistant":
			entries = append(entries, parseTranscriptAssistant(message.Content)...)
		}
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

// RenderClaudeTranscript renders a Claude Code session transcript into the
// conversation form Spanreed displays: ❯ user prompts, ⏺ assistant text and
// tool calls, ⎿ trimmed tool results. ok is false when the file cannot be
// read or holds no conversation, so callers fall back to the pane capture.
func RenderClaudeTranscript(path string) (rendered string, ok bool) {
	entries, ok := ParseClaudeTranscript(path)
	if !ok {
		return "", false
	}
	return RenderTranscriptEntries(entries), true
}

// RenderClaudeTranscriptFrom is RenderClaudeTranscript over an already-open
// transcript, for the ones that are not on this machine.
func RenderClaudeTranscriptFrom(source io.Reader) (rendered string, ok bool) {
	entries, ok := ParseClaudeTranscriptFrom(source)
	if !ok {
		return "", false
	}
	return RenderTranscriptEntries(entries), true
}

// RenderTranscriptEntries paints parsed entries for the terminal.
func RenderTranscriptEntries(entries []TranscriptEntry) string {
	var out strings.Builder
	for _, entry := range entries {
		switch entry.Kind {
		case "prompt":
			out.WriteString("\n")
			writeEntry(&out, promptMarkStyle().Render("❯ "),
				paintLines(entry.Text, promptTextStyle()))
		case "reply":
			out.WriteString("\n")
			writeEntry(&out, replyMarkStyle().Render("⏺ "), renderMarkdown(entry.Text))
		case "tool":
			out.WriteString("\n" +
				toolMarkStyle().Render("⏺ ") +
				toolNameStyle().Render(entry.Tool) +
				toolArgStyle().Render("("+entry.Arg+")") +
				"\n")
		case "result":
			writeResult(&out, entry)
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

// parseTranscriptUser yields a prompt (string content) or the trimmed
// tool results a user message carries (block content).
func parseTranscriptUser(content json.RawMessage) []TranscriptEntry {
	var prompt string
	if err := json.Unmarshal(content, &prompt); err == nil {
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			return nil
		}
		return []TranscriptEntry{{Kind: "prompt", Text: prompt}}
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		if result := transcriptResultText(block.Content); result != "" {
			entries = append(entries, trimTranscriptResult(result))
		}
	}
	return entries
}

func parseTranscriptAssistant(content json.RawMessage) []TranscriptEntry {
	var blocks []transcriptBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			entries = append(entries, TranscriptEntry{Kind: "reply", Text: text})
		case "tool_use":
			entries = append(entries, TranscriptEntry{
				Kind: "tool",
				Tool: block.Name,
				Arg:  transcriptToolArgument(block.Input),
			})
		}
	}
	return entries
}

// transcriptToolArgument picks the one input that identifies a tool call.
func transcriptToolArgument(input map[string]any) string {
	for _, key := range []string{
		"command", "file_path", "path", "pattern", "url", "description",
		"prompt", "query",
	} {
		if value, ok := input[key].(string); ok && value != "" {
			return transcriptEllipsis(strings.Join(strings.Fields(value), " "), 80)
		}
	}
	return ""
}

// transcriptResultText flattens a tool_result content payload, which is
// either a plain string or a list of text blocks.
func transcriptResultText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// trimTranscriptResult caps a tool result at transcriptResultLines lines
// of at most 200 runes each.
func trimTranscriptResult(result string) TranscriptEntry {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	shown := lines
	if len(shown) > transcriptResultLines {
		shown = shown[:transcriptResultLines]
	}
	kept := make([]string, len(shown))
	for index, line := range shown {
		kept[index] = transcriptEllipsis(line, 200)
	}
	return TranscriptEntry{
		Kind:   "result",
		Text:   strings.Join(kept, "\n"),
		Hidden: len(lines) - len(shown),
	}
}

// writeResult paints a result entry as an indented ⎿ block.
func writeResult(out *strings.Builder, entry TranscriptEntry) {
	for index, line := range strings.Split(entry.Text, "\n") {
		prefix := "    "
		if index == 0 {
			prefix = "  ⎿ "
		}
		out.WriteString(resultStyle().Render(prefix+line) + "\n")
	}
	if entry.Hidden > 0 {
		out.WriteString(resultStyle().Render(
			fmt.Sprintf("    … +%d lines", entry.Hidden)) + "\n")
	}
}

// writeEntry writes one conversation entry: marker then first line, with
// continuations indented under it. text arrives already styled, one rendered
// row per line, because each row carries — and closes — its own styling:
// the transcript pane wraps and highlights rows independently, and a run of
// color left open at a row's end would bleed into the pane beside it.
func writeEntry(out *strings.Builder, marker, text string) {
	for index, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		prefix := "  "
		if index == 0 {
			prefix = marker
		}
		out.WriteString(prefix + line + "\n")
	}
}

func transcriptEllipsis(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
