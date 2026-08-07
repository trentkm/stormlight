package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
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
	// The keys are chrome, not news: the cap carries the band's full
	// brightness because it is the thing you press, and the word explaining
	// it recedes, so the counters stay what the eye lands on.
	statusKeyColor     = "#C8D6F2"
	statusDividerColor = "#55698F"
	// statusDivider closes the tally. Two sections of small text with no
	// rule between them read as one run, which is how a count of agents
	// ended up looking like part of a key hint.
	statusDivider = "  #[fg=" + statusDividerColor + "]│"
	// statusWidthOption holds the columns the right section will print at
	// the client's current width, so status-left yields exactly that much
	// and no more. It is a format rather than a number because the right
	// section is not one width: see statusSummary.
	statusWidthOption = "@stormlight_status_width"
	// statusLineageMinWidth is what the left keeps before the counters may
	// spell themselves out. The bar's first job is naming where you are, so
	// the words are what yields: below this much room for the lineage the
	// counters fall back to glyph and number, which the dashboard header is
	// the legend for. The key hints have no such fallback and never drop.
	//
	// The figure is what a full lineage occupies — the glint, two capped
	// segments, their separators, and a name — because status-left is
	// truncated from the right, so anything short comes out of the agent's
	// own name, which is the one thing on the bar that never drops.
	statusLineageMinWidth = 48
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

// statusSummary renders the tally for the right of the band.
//
// The vocabulary is the dashboard header's, down to the glyphs and the
// order, so the two read as one instrument rather than two reports of the
// same thing. Empty tiers are left out — a bar that says "0 waiting" spends
// columns to say nothing — and a session with no agents in it renders
// nothing at all, divider included, so the keys are not left hanging off a
// rule with nothing behind it.
func (r *Runtime) statusSummary(stats agent.Stats) string {
	return statusByWidth(
		r.statusWordsMinWidth(stats),
		statusCounters(stats, true),
		statusCounters(stats, false),
	)
}

// statusWordsMinWidth is the client width at which the counters can afford
// their words: what the spelled-out right section takes, plus the lineage's
// floor. It is computed per tally rather than fixed, because a bar carrying
// one counter has room for words long before a bar carrying four does.
func (r *Runtime) statusWordsMinWidth(stats agent.Stats) int {
	return r.statusRightWidth(statusCounters(stats, true)) +
		statusLineageMinWidth
}

// statusByWidth picks between two renderings at the client's width. tmux
// evaluates it per client, which matters: two terminals of different sizes
// can share a session, and the option is written once for both.
func statusByWidth(threshold int, wide, narrow string) string {
	if wide == narrow {
		return wide
	}
	return "#{?#{e|>=:#{client_width}," + strconv.Itoa(threshold) +
		"}," + wide + "," + narrow + "}"
}

func statusCounters(stats agent.Stats, words bool) string {
	if stats.Total() == 0 {
		return ""
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
	format.WriteString(" ")
	for index, count := range counts {
		if index > 0 {
			format.WriteString("  ")
		}
		labelColor := statusLabelColor
		emphasis := ""
		if count.loud {
			labelColor = count.color
			// A comma inside #[...] ends the enclosing conditional branch
			// unless it is escaped; status-right holds none today, but this
			// string is one edit away from living inside one.
			emphasis = "#,bold"
		}
		text := strconv.Itoa(count.value)
		if words {
			text += " " + count.label
		}
		format.WriteString("#[fg=" + count.color + emphasis + "]" + count.glyph)
		format.WriteString("#[fg=" + labelColor + emphasis + "] " + text)
	}
	// tmux attributes latch until something clears them; without this the
	// key hints would inherit whichever tier happened to render last.
	return format.String() + statusDivider + "#[default]"
}

// PublishStatus writes the agent tally onto the managed session's status
// bar. It is a no-op when the tally has not moved, so the dashboard can call
// it on every poll and only the changes reach tmux.
func (r *Runtime) PublishStatus(ctx context.Context, stats agent.Stats) error {
	summary := r.statusSummary(stats)
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
		{statusWidthOption, r.statusWidth(stats)},
		{"status-right-length", strconv.Itoa(
			r.statusRightWidth(statusCounters(stats, true)))},
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

// statusRight is the whole right section: the tally, then the standing hints
// for the keys that leave this window.
func (r *Runtime) statusRight() string {
	return statusSummaryFormat + r.statusRightKeys()
}

// statusRightKeys names the ways out of an agent: back to the dashboard, and
// either way along the queue. The two queue keys share one label — spelling
// both out costs more of the band than the direction is worth, and they sit
// in the order they move, back on the left.
func (r *Runtime) statusRightKeys() string {
	queue := "#[fg=" + statusKeyColor + "]" +
		r.effectivePreviousKeys()[0] + " " + r.effectiveNextKeys()[0] +
		"#[fg=" + statusLabelColor + "] ↻ queue#[default]"
	return "  " +
		statusKeyHint(r.effectiveReturnKeys()[0], "⏎ dashboard") + "  " +
		queue + " "
}

func statusKeyHint(key, label string) string {
	return "#[fg=" + statusKeyColor + "]" + key +
		"#[fg=" + statusLabelColor + "] " + label + "#[default]"
}

// statusWidth is the budget status-left yields to the right section, as a
// format tmux resolves per client. status-right-length cannot serve: it is a
// cap on the widest form, and reserving that on a narrow client would cut
// the agent's own name short for space nothing occupies.
func (r *Runtime) statusWidth(stats agent.Stats) string {
	return statusByWidth(
		r.statusWordsMinWidth(stats),
		strconv.Itoa(r.statusRightWidth(statusCounters(stats, true))),
		strconv.Itoa(r.statusRightWidth(statusCounters(stats, false))),
	)
}

// statusRightWidth is the columns a rendering of the right section prints.
// It is measured rather than counted: #[...] tags cost bytes and no columns.
func (r *Runtime) statusRightWidth(counters string) int {
	return formatWidth(counters) + formatWidth(r.statusRightKeys())
}

func formatWidth(format string) int {
	var text strings.Builder
	for index := 0; index < len(format); index++ {
		if format[index] == '#' && index+1 < len(format) &&
			format[index+1] == '[' {
			if end := strings.IndexByte(format[index:], ']'); end >= 0 {
				index += end
				continue
			}
		}
		text.WriteByte(format[index])
	}
	return ansi.StringWidth(text.String())
}
