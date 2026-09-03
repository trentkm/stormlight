package ui

// One animation for one idea: a machine being contacted.
//
// The add-workspace modal has always had it — you name a machine, and the
// dots run while ssh finds out whether it is there. The roster had
// nothing, because the roster used to wait: a refresh dialled every host
// inline, so by the time a frame was drawn there was nothing left to be
// waiting for. What the user saw instead was a dashboard that said "No
// agents" for as long as the handshake took, then filled in.
//
// Now the refresh returns what answered and reports what did not, and
// this is what the panes draw in the gap. Same spinner, same words, same
// reason.

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
)

// reaching is the animation for a machine being contacted: Bubbles' own
// Points spinner, whose ∙ and ● are already this dashboard's vocabulary
// for quiet and working. The frames and the pace both come from the
// library rather than being invented here.
//
// It rides the tick the working glow already runs on — 90ms — rather
// than starting a second loop, so each frame is held for as many ticks
// as the spinner's own FPS asks for.
var reaching = spinner.Points

// reachingHold is how many 90ms ticks one frame lasts, from the
// spinner's declared rate.
var reachingHold = max(1, int(reaching.FPS/(90*time.Millisecond)))

// reachingFrame is the dots at whatever phase the shimmer is on, or the
// first frame when nothing is animating.
func (m Model) reachingFrame() string {
	if phase := m.shimmerPhaseOrRest(); phase >= 0 {
		return reaching.Frames[(phase/reachingHold)%len(reaching.Frames)]
	}
	return reaching.Frames[0]
}

// reachingNote is the line a pane draws where it would otherwise draw
// nothing: the dots, and the machines being waited on by name.
//
// Naming them matters more than it looks. "Reaching…" says the dashboard
// is busy; "Reaching mini…" says which machine is slow, which is the one
// fact worth knowing when a laptop is asleep and a workspace has not
// appeared. Empty when there is nothing outstanding — the caller then has
// an empty list on its hands and should say so plainly.
func (m Model) reachingNote(width int, hosts ...string) string {
	return m.reachingNoteForLoad(width, m.loaded, hosts...)
}

func (m Model) reachingNoteForLoad(
	width int,
	loaded bool,
	hosts ...string,
) string {
	if len(hosts) == 0 && loaded {
		return ""
	}
	dots := accentStyle().Render(m.reachingFrame()) + " "
	room := width - lipgloss.Width(dots)
	// The columns are narrow, and a machine name cut in half is worse
	// than no machine name: "Reaching slow…" reads as a host called
	// slow. So the names go in only when they fit whole, and a pane with
	// no room for them says the shorter true thing instead.
	if len(hosts) > 0 {
		named := "Reaching " + strings.Join(hosts, ", ") + "…"
		if lipgloss.Width(named) <= room {
			return dots + mutedStyle().Render(named)
		}
	}
	return truncate(dots+mutedStyle().Render("Reaching…"), width)
}

// reachingFor is the machines being waited on that matter to one host's
// worth of the dashboard. An empty host is this machine, which is never
// reached over anything and therefore never waits — except on the first
// refresh, when nothing at all has answered.
func (m Model) reachingFor(host string) []string {
	if host == "" {
		return nil
	}
	for _, waiting := range m.reachingHosts {
		if waiting == host {
			return []string{host}
		}
	}
	return nil
}

// reachingBar is every outstanding machine at once, for the workspaces
// pane — the one place that speaks for the whole dashboard rather than
// for the selected row. Empty once everything has answered.
func (m Model) reachingBar(width int) string {
	return m.reachingNote(width, m.reachingHosts...)
}
