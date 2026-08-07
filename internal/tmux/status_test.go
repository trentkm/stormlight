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
	stats := agent.Stats{Working: 3, Waiting: 2, Urgent: 1, Idle: 4}
	text := visibleText(statusCounters(stats, true))
	for _, want := range []string{
		"● 3 working", "○ 2 waiting", "! 1 needs input", "○ 4 idle",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("counters missing %q: %q", want, text)
		}
	}
	if !strings.HasSuffix(statusCounters(stats, true), "#[default]") {
		t.Fatalf("counters do not reset their styling: %q", text)
	}
}

// A narrow client keeps the glyph and the number and drops the word: the
// glyph is the dashboard's own and the header is its legend, while the key
// hints have no such fallback, so they are what the columns go to.
func TestStatusSummaryShedsWordsOnNarrowClients(t *testing.T) {
	stats := agent.Stats{Working: 3, Waiting: 2}
	narrow := visibleText(statusCounters(stats, false))
	if strings.Contains(narrow, "working") || strings.Contains(narrow, "waiting") {
		t.Fatalf("narrow counters kept their words: %q", narrow)
	}
	for _, want := range []string{"● 3", "○ 2"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow counters missing %q: %q", want, narrow)
		}
	}

	runtime := &Runtime{}
	wide := runtime.statusRightWidth(statusCounters(stats, true))
	// The words are affordable only once the lineage still has its floor
	// left over — a fixed threshold crushes the agent's name exactly at the
	// width where a four-tier tally spells itself out.
	threshold := runtime.statusWordsMinWidth(stats)
	if threshold != wide+statusLineageMinWidth {
		t.Fatalf("threshold = %d, want %d", threshold, wide+statusLineageMinWidth)
	}
	summary := runtime.statusSummary(stats)
	if !strings.HasPrefix(summary,
		"#{?#{e|>=:#{client_width},"+strconv.Itoa(threshold)+"},") {
		t.Fatalf("summary does not switch on the client width: %q", summary)
	}
	// The budget status-left yields has to switch on the same width, or the
	// name would be cut for space the narrow form never takes.
	if want := statusByWidth(threshold, strconv.Itoa(wide),
		strconv.Itoa(runtime.statusRightWidth(statusCounters(stats, false)))); runtime.statusWidth(stats) != want {
		t.Fatalf("width budget = %q, want %q", runtime.statusWidth(stats), want)
	}
}

// Empty tiers spend columns to say nothing, and the working count is the one
// the dashboard header always shows.
func TestStatusSummaryOmitsEmptyTiers(t *testing.T) {
	summary := statusCounters(agent.Stats{Working: 1}, true)
	if strings.Contains(summary, "waiting") ||
		strings.Contains(summary, "idle") ||
		strings.Contains(summary, "input") {
		t.Fatalf("summary carried an empty tier: %q", summary)
	}
	if !strings.Contains(visibleText(summary), "● 1 working") {
		t.Fatalf("summary = %q", summary)
	}
	if summary := (&Runtime{}).statusSummary(agent.Stats{}); summary != "" {
		t.Fatalf("empty session summary = %q", summary)
	}
}

// The width is what status-left is truncated against, so it counts printed
// columns rather than the bytes tmux markup takes to say them — a right
// section measured in bytes cuts the agent's own name short for space
// nothing occupies.
func TestStatusRightWidthCountsColumnsNotMarkup(t *testing.T) {
	runtime := &Runtime{}
	summary := statusCounters(agent.Stats{Working: 2, Urgent: 1}, true)
	width := runtime.statusRightWidth(summary)
	printed := visibleWidth(summary) + visibleWidth(runtime.statusRightKeys())
	if width != printed {
		t.Fatalf("width = %d, want %d", width, printed)
	}
	if bytes := len(summary) + len(runtime.statusRightKeys()); bytes <= width {
		t.Fatalf("markup was counted as columns: %d bytes, %d columns", bytes, width)
	}
}

// The counters and the key hints are two different kinds of thing — one is
// news, the other standing chrome — and with nothing between them they read
// as one run of small text.
func TestStatusRightSeparatesTheTallyFromTheKeys(t *testing.T) {
	runtime := &Runtime{}
	for _, words := range []bool{true, false} {
		counters := statusCounters(agent.Stats{Working: 1}, words)
		if !strings.HasSuffix(visibleText(counters), "│") {
			t.Fatalf("tally does not close with a divider: %q", counters)
		}
	}
	keys := visibleText(runtime.statusRightKeys())
	// The queue keys share one label and sit in the order they move.
	for _, want := range []string{"C-6 ⏎ dashboard", `C-\ C-] ↻ queue`} {
		if !strings.Contains(keys, want) {
			t.Fatalf("key hints missing %q: %q", want, keys)
		}
	}
	// Each queue hint claims a mouse range of its own, and hands the region
	// back to the return range afterwards so no column answers to nothing.
	format := runtime.statusRightKeys()
	for _, want := range []string{
		"#[range=user|" + queueBackRange + "]",
		"#[range=user|" + queueForwardRange + "]",
	} {
		if !strings.Contains(format, want) {
			t.Fatalf("key hints missing %q: %q", want, format)
		}
	}
	if strings.Contains(format, "#[norange]") {
		t.Fatalf("a hint left dead columns behind it: %q", format)
	}
	if count := strings.Count(format, "#[range=right]"); count != 2 {
		t.Fatalf("ranges reopened %d times, want 2: %q", count, format)
	}
	// With no agents there is no tally, so there is no rule to hang the
	// keys off either.
	if runtime.statusSummary(agent.Stats{}) != "" {
		t.Fatal("an empty session still drew a divider")
	}
}

func TestPublishStatusWritesTheTallyOnce(t *testing.T) {
	runner := &captureRunner{feedbackVersion: statusVersion}
	runtime := &Runtime{runner: runner, sessionName: "stormlight-agents"}
	stats := agent.Stats{Working: 1, Waiting: 2}

	if err := runtime.PublishStatus(context.Background(), stats); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"show-options", "-qv", "-t", "stormlight-agents", statusVersionOption},
		{"set-option", "-t", "stormlight-agents", statusSummaryOption,
			runtime.statusSummary(stats)},
		{"set-option", "-t", "stormlight-agents", statusWidthOption,
			runtime.statusWidth(stats)},
		{"set-option", "-t", "stormlight-agents", "status-right-length",
			strconv.Itoa(runtime.statusRightWidth(statusCounters(stats, true)))},
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
	if len(runner.calls) != len(want)+4 {
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

// tmux stores a user range name in a fixed 16-byte field and cuts the rest
// without complaint. Two names that survive the cut identically dispatch as
// one range, which reads exactly like the binding never fired.
func TestQueueRangeNamesSurviveTmuxTruncation(t *testing.T) {
	for _, name := range []string{queueForwardRange, queueBackRange} {
		if len(name) > queueRangeMaxLength {
			t.Fatalf("range name %q is %d bytes, over tmux's %d",
				name, len(name), queueRangeMaxLength)
		}
	}
	if queueForwardRange == queueBackRange {
		t.Fatal("the two queue ranges are indistinguishable")
	}
}
