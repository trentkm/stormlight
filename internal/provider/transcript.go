package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Claude Code appends every completed message of a session to a JSONL
// transcript (reported by its hooks as transcript_path). Rendering the
// conversation from that file gives Spanreed the entire session history —
// the terminal screen alone cannot: Claude runs in the alternate screen,
// so tmux never accumulates scrollback for it.

const (
	// transcriptResultLines caps how many lines of a tool result are
	// rendered; results routinely run to hundreds of lines of noise.
	transcriptResultLines = 3
	// transcriptScanBuffer must fit the largest JSONL line; tool results
	// carry whole files.
	transcriptScanBuffer = 16 * 1024 * 1024
)

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

// RenderClaudeTranscript renders a Claude Code session transcript into the
// conversation form Spanreed displays: ❯ user prompts, ⏺ assistant text and
// tool calls, ⎿ trimmed tool results. ok is false when the file cannot be
// read or holds no conversation, so callers fall back to the pane capture.
func RenderClaudeTranscript(path string) (rendered string, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	var out strings.Builder
	turns := 0
	scanner := bufio.NewScanner(file)
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
			turns += renderTranscriptUser(&out, message.Content)
		case "assistant":
			turns += renderTranscriptAssistant(&out, message.Content)
		}
	}
	if turns == 0 {
		return "", false
	}
	return strings.TrimRight(out.String(), "\n") + "\n", true
}

// renderTranscriptUser writes a prompt (string content) or the trimmed
// tool results a user message carries (block content), returning how many
// conversation entries it produced.
func renderTranscriptUser(out *strings.Builder, content json.RawMessage) int {
	var prompt string
	if err := json.Unmarshal(content, &prompt); err == nil {
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			return 0
		}
		out.WriteString("\n❯ " + indentContinuations(prompt, "  ") + "\n")
		return 1
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return 0
	}
	entries := 0
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		if result := transcriptResultText(block.Content); result != "" {
			out.WriteString(trimTranscriptResult(result))
			entries++
		}
	}
	return entries
}

func renderTranscriptAssistant(out *strings.Builder, content json.RawMessage) int {
	var blocks []transcriptBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return 0
	}
	entries := 0
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			out.WriteString("\n⏺ " + indentContinuations(text, "  ") + "\n")
			entries++
		case "tool_use":
			out.WriteString("\n⏺ " + block.Name +
				"(" + transcriptToolArgument(block.Input) + ")\n")
			entries++
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

// trimTranscriptResult renders a tool result as an indented ⎿ block capped
// at transcriptResultLines lines.
func trimTranscriptResult(result string) string {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	shown := lines
	if len(shown) > transcriptResultLines {
		shown = shown[:transcriptResultLines]
	}
	var out strings.Builder
	for index, line := range shown {
		prefix := "    "
		if index == 0 {
			prefix = "  ⎿ "
		}
		out.WriteString(prefix + transcriptEllipsis(line, 200) + "\n")
	}
	if hidden := len(lines) - len(shown); hidden > 0 {
		out.WriteString(fmt.Sprintf("    … +%d lines\n", hidden))
	}
	return out.String()
}

func indentContinuations(text, indent string) string {
	return strings.ReplaceAll(text, "\n", "\n"+indent)
}

func transcriptEllipsis(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
