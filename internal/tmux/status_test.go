package tmux

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
)

func TestStatusSummarySpeaksTheHeadersLanguage(t *testing.T) {
	summary, _ := statusSummary(agent.Stats{
		Working: 3, Waiting: 2, Urgent: 1, Idle: 4,
	})
	text := visibleText(summary)
	for _, want := range []string{
		"● 3 working", "○ 2 waiting", "! 1 needs input", "○ 4 idle",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q: %q", want, summary)
		}
	}
	if !strings.HasSuffix(summary, "#[default]") {
		t.Fatalf("summary does not reset its styling: %q", summary)
	}
}

// Empty tiers spend columns to say nothing, and the working count is the one
// the dashboard header always shows.
func TestStatusSummaryOmitsEmptyTiers(t *testing.T) {
	summary, _ := statusSummary(agent.Stats{Working: 1})
	if strings.Contains(summary, "waiting") ||
		strings.Contains(summary, "idle") ||
		strings.Contains(summary, "input") {
		t.Fatalf("summary carried an empty tier: %q", summary)
	}
	if !strings.Contains(visibleText(summary), "● 1 working") {
		t.Fatalf("summary = %q", summary)
	}
	if summary, width := statusSummary(agent.Stats{}); summary != "" || width != 0 {
		t.Fatalf("empty session summary = %q (%d columns)", summary, width)
	}
}

// The width is what status-left is truncated against, so it counts printed
// columns rather than the bytes tmux markup takes to say them.
func TestStatusSummaryWidthCountsColumnsNotMarkup(t *testing.T) {
	summary, width := statusSummary(agent.Stats{Working: 2, Urgent: 1})
	if got := len(summary); got <= width {
		t.Fatalf("markup was counted as columns: %d bytes, %d columns", got, width)
	}
	if got := visibleWidth(summary); got != width {
		t.Fatalf("width = %d, want %d (%q)", width, got, summary)
	}
}

func TestPublishStatusWritesTheTallyOnce(t *testing.T) {
	runner := &captureRunner{feedbackVersion: statusVersion}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}
	stats := agent.Stats{Working: 1, Waiting: 2}

	if err := runtime.PublishStatus(context.Background(), stats); err != nil {
		t.Fatal(err)
	}
	summary, width := statusSummary(stats)
	want := [][]string{
		{"show-options", "-qv", "-t", "stormlight-agents", statusVersionOption},
		{"set-option", "-t", "stormlight-agents", statusSummaryOption, summary},
		{"set-option", "-t", "stormlight-agents", "status-right-length",
			strconv.Itoa(width + runtime.statusRightHintWidth())},
	}
	assertCalls(t, runner.calls, want)

	// The dashboard publishes on every poll; only movement reaches tmux.
	if err := runtime.PublishStatus(context.Background(), stats); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, runner.calls, want)

	if err := runtime.PublishStatus(context.Background(), agent.Stats{Working: 2}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != len(want)+3 {
		t.Fatalf("a changed tally was not published: %#v", runner.calls)
	}
}

// A session nobody has visited yet has tmux's own bar; the counters belong in
// Stormlight's chrome, so publishing dresses it first.
func TestPublishStatusDressesAnUnvisitedSession(t *testing.T) {
	runner := &captureRunner{}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}

	if err := runtime.PublishStatus(context.Background(), agent.Stats{Working: 1}); err != nil {
		t.Fatal(err)
	}
	dressed := false
	for _, call := range runner.calls {
		if slices.Equal(call, []string{
			"set-option", "-t", "stormlight-agents", "status-format[0]", statusFormat,
		}) {
			dressed = true
		}
	}
	if !dressed {
		t.Fatalf("status bar was not dressed before publishing: %#v", runner.calls)
	}
}

func visibleWidth(format string) int {
	return ansi.StringWidth(visibleText(format))
}

// visibleText is what a terminal prints of a tmux format: the text with
// every #[...] tag removed.
func visibleText(format string) string {
	var text strings.Builder
	for index := 0; index < len(format); index++ {
		if format[index] == '#' && index+1 < len(format) && format[index+1] == '[' {
			end := strings.IndexByte(format[index:], ']')
			if end >= 0 {
				index += end
				continue
			}
		}
		text.WriteByte(format[index])
	}
	return text.String()
}
