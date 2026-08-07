package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trentkm/stormlight/internal/agent"
)

// The status summary is the dashboard's header counters, shown on the band
// of the managed session so the tally is readable from inside an agent. An
// agent's window is where a human spends the minutes between glances at the
// dashboard, and the question that sends them back — is anything else
// waiting on me? — should not need a trip to find out.
//
// It is a session option that Stormlight writes rather than a format tmux
// evaluates: the counts follow the same precedence the dashboard's row
// glyphs do, marks included, which is inference no format language can
// carry. status-right expands the option with #{E:...} so the styling
// inside it applies.
const (
	statusSummaryOption = "@stormlight_status_summary"
	statusSummaryFormat = "#{E:" + statusSummaryOption + "}"
	// The band is a fixed sapphire whatever the terminal's background, so
	// these are literal rather than resolved from internal/theme: they are
	// the palette's dark-terminal entries, chosen against this band.
	statusWorkingColor = "#61AFEF"
	statusWaitingColor = "#E5C07B"
	statusIdleColor    = "#8FA6CC"
	statusLabelColor   = "#8FA6CC"
)

// statusCount is one counter on the band: the glyph the dashboard paints for
// that state, the count, and the word for it.
type statusCount struct {
	glyph string
	value int
	label string
	color string
	loud  bool
}

// statusSummary renders the tally for the right of the band, and the columns
// it occupies.
//
// The vocabulary is the dashboard header's, down to the glyphs and the
// order, so the two read as one instrument rather than two reports of the
// same thing. Empty tiers are left out — a bar that says "0 waiting" spends
// columns to say nothing — and a session with no agents in it renders
// nothing at all.
func statusSummary(stats agent.Stats) (string, int) {
	if stats.Total() == 0 {
		return "", 0
	}
	counts := []statusCount{{
		glyph: "●", value: stats.Working,
		label: "working", color: statusWorkingColor,
	}}
	if stats.Waiting > 0 {
		counts = append(counts, statusCount{
			glyph: "○", value: stats.Waiting,
			label: "waiting", color: statusWaitingColor,
		})
	}
	if stats.Urgent > 0 {
		label := "need input"
		if stats.Urgent == 1 {
			label = "needs input"
		}
		counts = append(counts, statusCount{
			glyph: "!", value: stats.Urgent,
			label: label, color: statusWaitingColor, loud: true,
		})
	}
	if stats.Idle > 0 {
		counts = append(counts, statusCount{
			glyph: "○", value: stats.Idle,
			label: "idle", color: statusIdleColor,
		})
	}

	var format strings.Builder
	width := 0
	for index, count := range counts {
		if index > 0 {
			format.WriteString("  ")
			width += 2
		}
		text := fmt.Sprintf("%d %s", count.value, count.label)
		labelColor := statusLabelColor
		emphasis := ""
		if count.loud {
			labelColor = count.color
			// A comma inside #[...] ends the enclosing conditional branch
			// unless it is escaped; status-right holds none today, but this
			// string is one edit away from living inside one.
			emphasis = "#,bold"
		}
		format.WriteString("#[fg=" + count.color + emphasis + "]" + count.glyph)
		format.WriteString("#[fg=" + labelColor + emphasis + "] " + text)
		width += utf8.RuneCountInString(count.glyph) + 1 + utf8.RuneCountInString(text)
	}
	// tmux attributes latch until something clears them; without this the
	// return hint would inherit whichever tier happened to render last.
	format.WriteString("#[default]")
	return " " + format.String(), width + 1
}

// PublishStatus writes the agent tally onto the managed session's status
// bar. It is a no-op when the tally has not moved, so the dashboard can call
// it on every poll and only the changes reach tmux.
func (r *Runtime) PublishStatus(ctx context.Context, stats agent.Stats) error {
	summary, width := statusSummary(stats)
	r.statusMu.Lock()
	unchanged := summary == r.publishedStatus
	r.statusMu.Unlock()
	if unchanged {
		return nil
	}
	// A session nobody has attached to yet is undressed, and writing the
	// summary onto tmux's default bar would put Stormlight's counters in a
	// stranger's chrome. Dressing it here means the band is right from the
	// first agent, not from the first visit.
	if err := r.configureStatusBar(ctx, r.sessionName); err != nil {
		return err
	}
	options := [][2]string{
		{statusSummaryOption, summary},
		{"status-right-length", strconv.Itoa(width + r.statusRightHintWidth())},
	}
	for _, option := range options {
		if _, err := r.runner.Run(ctx, nil,
			"set-option", "-t", r.sessionName, option[0], option[1],
		); err != nil {
			return fmt.Errorf("publish %s: %w", option[0], err)
		}
	}
	r.statusMu.Lock()
	r.publishedStatus = summary
	r.statusMu.Unlock()
	return nil
}

// statusRight is the whole right section: the tally, then the standing hint
// for the key that leads back to the dashboard.
func (r *Runtime) statusRight() string {
	return statusSummaryFormat + r.statusRightHint()
}

func (r *Runtime) statusRightHint() string {
	return " " + r.effectiveReturnKeys()[0] + " ⏎ dashboard "
}

func (r *Runtime) statusRightHintWidth() int {
	return utf8.RuneCountInString(r.statusRightHint())
}
