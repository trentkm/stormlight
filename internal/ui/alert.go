package ui

// The failure surface: the card an error gets to itself, and the detail
// view behind it.

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// An error used to live in a third of the hint row — twenty-eight columns
// at the widest, its newlines flattened to spaces — and the next keystroke
// wiped it. Anything a failure had to say past "ssh: connect to host thai"
// was unreadable by construction, and the reader rarely got even that far.
//
// The alert is the correction. The message floats in its own card over the
// foot of the body, wrapped rather than cut, and the hint row keeps its own
// line. A card ends in one of five ways, and only the first counts as read:
//
//   - dismissed, by Esc or by opening it with `e`
//   - superseded, by a different failure
//   - answered, when the poll that raised it succeeds (see polled)
//   - swept, when the form that raised it closes (see raisedIn)
//   - timed out, where no key of ours can reach it (see expires)
//
// The distinction matters because only a card that was read may silence a
// failure that repeats. Everything else leaves it free to speak again.
type alert struct {
	err error
	// raisedIn is the surface the complaint belongs to. A form's
	// objection to a name or a path is spent the moment the form closes;
	// a failure raised on the dashboard belongs to nothing that closes,
	// so it outlives every overlay opened over it.
	raisedIn mode
	// polled marks a card the background refresh raised rather than
	// anything the human did. It is the one failure that reports itself
	// on a timer, so the next refresh that succeeds answers it and the
	// card clears itself when the daemon comes back.
	polled bool
	// at is when the failure arrived, which the detail view says out
	// loud: a message recalled later has to place itself in time.
	at time.Time
	// expires bounds the card's life in the two places the dashboard
	// cannot take an Esc on its behalf: inside the portal, where the
	// keyboard belongs to the agent, and under a floating program. Zero
	// means the card waits for a human, which is the usual case.
	expires time.Time
}

// alertDetail is the whole of the last failure, opened with `e`. It is a
// snapshot rather than a view onto the live alert, and it outlives the
// card: inside the portal no key can open anything, and under a form `e`
// belongs to the form, so the only way those messages are ever readable in
// full is for `e` to still find the last one after its card is gone.
type alertDetail struct {
	text string
	at   time.Time
	// polled remembers that this came from the self-repeating poll, so
	// reading it can hush it even when its card timed out first and took
	// the flag with it.
	polled bool
	offset int
}

const (
	// alertLinger is how long a card lives where nothing can dismiss it.
	alertLinger = 12 * time.Second
	// alertCardLines is how much of a message the card carries before the
	// rest becomes one keystroke away rather than lost.
	alertCardLines = 3
	alertCardWidth = 76
)

func (a alert) active() bool { return a.err != nil }

func (a alert) message() string {
	if a.err == nil {
		return ""
	}
	return strings.TrimSpace(a.err.Error())
}

// raise puts an error on screen. A failure repeating itself — a refresh
// against a daemon that is still down, arriving every tick — is the same
// card, not a new one, so its clock is not restarted and it can still age
// out where nothing can dismiss it.
func (m *Model) raise(err error) {
	m.raiseFailure(err, false)
}

// raisePolled is the refresh's way in: the same card, marked as the one
// thing that will report its own recovery.
func (m *Model) raisePolled(err error) {
	m.raiseFailure(err, true)
}

// complain is a form objecting to what is in it — an empty name, a path
// that is not there. Only these belong to the surface they were raised in,
// and only these are swept when it closes. A failure that merely *arrived*
// while a form happened to be open belongs to nothing on screen: an ssh
// setup that failed in the background is not answered by cancelling the
// dispatch form that was open at the time.
func (m *Model) complain(err error) {
	standing := m.alert.message()
	m.raiseFailure(err, false)
	if m.alert.message() != standing {
		// Only stamp a card this actually raised. A repeat of the same
		// objection is already stamped, and a card that was standing for
		// another reason is not the form's to claim — or to take with it
		// when it closes.
		m.alert.raisedIn = m.mode
	}
}

func (m *Model) raiseFailure(err error, polled bool) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	if m.alert.active() && m.alert.message() == message {
		if polled {
			// The same text arriving from the poll makes this the poll's
			// card too, and a card that never learns it is polled never
			// clears itself when the daemon comes back. The recall slot
			// is the same failure and learns it at the same moment.
			m.alert.polled = true
			m.lastFailure.polled = true
		}
		return
	}
	if polled && message == m.heldBack && m.keyboardHeldElsewhere() {
		// It timed out here once and nothing here can answer it, so
		// raising it again every tick would only cover the agent's
		// terminal on a loop. The moment the reader is back somewhere
		// they can act on it, this stops applying and it returns.
		return
	}
	if polled && message == m.hushedPoll {
		// The reader already dealt with this one. The poll will keep
		// reporting it every tick until the daemon comes back, and a
		// dismissal that lasts 700ms is not a dismissal.
		return
	}
	m.alert = alert{err: err, polled: polled, at: alertClock()}
	m.refitForms()
	// Every failure leaves its full text behind, so `e` can still reach
	// the last one after the card has gone. This is not what an open
	// detail view is reading — that took its own copy — because a poll
	// failing every tick would otherwise swap the message out from under
	// someone in the middle of reading it.
	m.lastFailure = alertDetail{text: message, at: m.alert.at, polled: polled}
}

// alertClock is when a failure says it arrived; tests replace it to hold
// an age still. It is not named `now` because `ageAlert` takes a `now`,
// and a package-scope name that ordinary parameters shadow is a trap.
var alertClock = time.Now

// dismissAlert is the reader answering the card — Esc, or opening it to
// read. Answering a self-repeating failure also hushes it: the poll would
// otherwise raise the identical message on the very next tick, and an Esc
// undone in 700ms is not a dismissal.
//
// This is the only way a card ends that counts as read. Everything else
// clears it with clearAlert, because hushing a message nobody saw is how
// a dashboard goes quiet about a daemon that is still down.
func (m *Model) dismissAlert() {
	if m.alert.polled {
		m.hushedPoll = m.alert.message()
	}
	m.clearAlert()
}

// clearAlert takes the card down without counting it as read: a card that
// timed out unread inside the portal, or a stale form complaint dropped
// when the form reopens.
func (m *Model) clearAlert() {
	m.alert = alert{}
	m.refitForms()
}

// clearComplaint drops the objection this surface raised last time it was
// open, and nothing else. Opening a form is not reading the failure that
// happened to be on screen — clearing everything here was the keystroke
// wipe this whole surface exists to remove, wearing a different hat.
func (m *Model) clearComplaint(surface mode) {
	if m.alert.active() && m.alert.raisedIn == surface {
		m.retract()
	}
}

// retract takes a complaint back entirely: the card and the recall slot
// both. A form's objection is spent when the form closes, and `e` offering
// "task cannot be empty" from an hour ago — a form long gone, about a
// field that no longer exists — presents a retracted complaint as an
// outstanding failure.
func (m *Model) retract() {
	if m.alert.message() == m.lastFailure.text {
		m.lastFailure = alertDetail{}
	}
	m.clearAlert()
}

// refitForms re-sizes the persistent widgets whose room the card changes.
// The dispatch form's task composer is sized once and kept; a card
// appearing under it shortens the modal, and a textarea that believes it
// is taller than the box it is drawn into scrolls and stays scrolled.
func (m *Model) refitForms() {
	if m.mode != modeDispatch {
		return
	}
	// A shrink can drop the optional name row out of the form, and a card
	// appearing under it shrinks it exactly like a smaller terminal does.
	// Typing must not keep landing in a field that is no longer drawn.
	if m.formFocus == dispatchName && !m.dispatchNameVisible() {
		m.formFocus = dispatchTask
		m.focusForm()
	}
	m.syncTaskComposerSize()
}

// resolvePolled is the refresh reporting that it worked. A card raised by
// a failing poll is the one kind that answers itself: the daemon came back,
// so the complaint is no longer true and should not sit there insisting.
// The recovery also lifts the hush — if the failure returns after this, it
// is news again.
func (m *Model) resolvePolled() {
	// The recall slot stops being the poll's the moment the poll works.
	// Otherwise reading it later — the whole point of recall — arms a
	// hush against a failure that is not happening, and the next time it
	// does happen the dashboard says nothing at all.
	m.lastFailure.polled = false
	if m.alert.polled {
		// Through clearAlert, not by hand: the card's rows come back to
		// the modal and whatever was sized for the shorter one has to
		// hear about it. clearAlert does not hush, so recovery still
		// counts as unread.
		m.clearAlert()
	}
	m.hushedPoll = ""
	m.heldBack = ""
}

// keyboardHeldElsewhere reports that the dashboard's own keys are not
// available: the portal has the keyboard, or a floating program does. Esc
// belongs to them there, so a card raised in that state cannot be dismissed
// by hand and gets a clock instead.
func (m Model) keyboardHeldElsewhere() bool {
	return m.overlay != nil || m.terminalFocused()
}

// ageAlert runs on the poll tick. Leaving the portal hands the card back to
// the human and stops its clock; entering it starts one.
func (m *Model) ageAlert(now time.Time) {
	if !m.keyboardHeldElsewhere() {
		// Back where a card can be answered. Anything held back because
		// it could not be is fair to raise again, and a card already
		// standing stops counting down.
		m.heldBack = ""
		if m.alert.active() {
			m.alert.expires = time.Time{}
		}
		return
	}
	if !m.alert.active() {
		return
	}
	if m.alert.expires.IsZero() {
		m.alert.expires = now.Add(alertLinger)
		return
	}
	if now.After(m.alert.expires) {
		if m.alert.polled {
			// Still failing, and no key here can answer it: raising it
			// again on the next tick would cover the agent's terminal
			// every twelve seconds forever. Hold it until the reader is
			// back on the dashboard, where it can be read and dismissed.
			m.heldBack = m.alert.message()
		}
		// Timing out is not reading, so nothing is hushed: leaving the
		// portal raises it again.
		m.clearAlert()
	}
}

// alertDetailReachable reports whether `e` opens the full text from here.
// It needs the dashboard's own keyboard and a mode that is not spending
// letters on an input.
func (m Model) alertDetailReachable() bool {
	return m.mode == modeNormal && !m.keyboardHeldElsewhere()
}

// readableFailure reports that there is a last failure for `e` to open —
// the standing card's, or the one whose card has already gone.
func (m Model) readableFailure() bool {
	return strings.TrimSpace(m.lastFailure.text) != ""
}

// openAlertDetail hands the last failure to the reader in full and clears
// the card: reading it is what an alert was waiting for. It opens whether
// or not a card is still standing, because the messages hardest to read —
// the ones raised inside the portal, where no key is ours — are exactly
// the ones whose card is gone by the time the reader can act.
func (m Model) openAlertDetail() (tea.Model, tea.Cmd) {
	if !m.readableFailure() {
		return m, nil
	}
	// A copy, taken once: what is on screen must not change under the
	// reader because a background poll failed again while they read.
	m.alertDetail = m.lastFailure
	if m.lastFailure.polled {
		// Reading it is answering it. Without this the next tick raises
		// the identical card over the view opened to read it — and, by
		// shortening the region, resizes that view mid-read.
		m.hushedPoll = m.lastFailure.text
	}
	m.dismissAlert()
	m.mode = modeAlert
	return m, nil
}

func (m Model) updateAlertDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	textWidth, window := m.alertDetailWindow()
	lines := wrapMessage(m.alertDetail.text, textWidth)
	last := max(0, len(lines)-window)
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[", "q", "e", "enter":
		m.mode = modeNormal
		return m, nil
	case "y":
		// The reason to copy an error is to paste it somewhere that can
		// answer it: an issue, a search, a shell.
		return m, copyToClipboardCmd(m.alertDetail.text)
	case "j", "down":
		m.alertDetail.offset = clamp(m.alertDetail.offset+1, 0, last)
	case "k", "up":
		m.alertDetail.offset = clamp(m.alertDetail.offset-1, 0, last)
	case "ctrl+d", "pgdown":
		m.alertDetail.offset = clamp(m.alertDetail.offset+window, 0, last)
	case "ctrl+u", "pgup":
		m.alertDetail.offset = clamp(m.alertDetail.offset-window, 0, last)
	case "g", "home":
		m.alertDetail.offset = 0
	case "G", "end":
		m.alertDetail.offset = last
	}
	return m, nil
}

// wrapMessage lays a message out at width, keeping the line breaks the
// error itself chose and folding what overruns them. Errors arrive with
// structure — an ssh failure is several lines of it — and flattening that
// to one line was half of what made them unreadable.
func wrapMessage(message string, width int) []string {
	if width <= 0 || strings.TrimSpace(message) == "" {
		return nil
	}
	message = strings.ReplaceAll(message, "\t", "    ")
	var lines []string
	for _, paragraph := range strings.Split(message, "\n") {
		paragraph = strings.TrimRight(paragraph, " ")
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines,
			strings.Split(ansi.Wrap(paragraph, width, "-"), "\n")...)
	}
	return lines
}

// renderAlertCard draws the floating card, or nothing when no error stands.
//
// It floats rather than taking rows of its own on purpose: the Spanreed is
// a real terminal sized 1:1 to its pane, and a card that changed the body's
// height would reflow every agent's screen each time something failed.
func (m Model) renderAlertCard(width, height int) string {
	if !m.alert.active() || width < 8 || height < 1 {
		return ""
	}
	// Three rows is a card with nothing in it: two borders and the keys,
	// and rendering a frame into fewer is how it loses its bottom edge.
	// A terminal that small still gets the message, just without the
	// frame — the old footer row managed that much, and going quiet
	// because the screen is small is the one thing this must never do.
	if width < 16 || height < 4 {
		return errorStyle().Render(
			truncate("! "+m.alert.message(), width))
	}
	cardWidth := clamp(width-4, 16, alertCardWidth)
	textWidth := max(4, cardWidth-6)
	// Minus the lift as well: a card sized against the whole body can grow
	// past the rows reserved for the composer, clamp to the top, and cover
	// the box it is meant to stay off.
	budget := clamp(height-3-m.inputStripRows(), 1, alertCardLines)

	lines := wrapMessage(m.alert.message(), textWidth)
	clipped := len(lines) > budget
	if clipped {
		lines = lines[:budget]
		// The ellipsis is the card admitting there is more, which it owes
		// the reader whether or not it can offer a key for the rest.
		last := len(lines) - 1
		lines[last] = ansi.Truncate(lines[last], max(1, textWidth-1), "") + "…"
	}

	body := make([]string, 0, len(lines)+1)
	for index, line := range lines {
		marker := "  "
		if index == 0 {
			marker = errorStyle().Bold(true).Render("! ")
		}
		body = append(body, " "+marker+line)
	}
	if keys := m.alertCardKeys(clipped); keys != "" &&
		lipgloss.Width(keys) <= cardWidth-2 {
		// Only if it fits on one row. Wrapped, it would cost the card the
		// row its frame was budgeted, and the bottom border with it.
		body = append(body, alignRight(keys, cardWidth-2))
	}
	return lipgloss.NewStyle().
		Width(cardWidth).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorFailed()).
		Render(strings.Join(body, "\n"))
}

// alertCardKeys names what the reader can do about the card from where they
// are. Under the portal or a floating program that is nothing — the card is
// on a clock there, because Esc belongs to the program.
func (m Model) alertCardKeys(clipped bool) string {
	if !m.alertDetailReachable() {
		// Under the portal, a floating program, or a form, every key the
		// card might name belongs to something else: Esc cancels the form
		// rather than the card, and `e` edits a path. It claims nothing,
		// and the message stays readable afterwards through `e` on the
		// dashboard.
		return ""
	}
	label := "e detail"
	if clipped {
		label = "e read it all"
	}
	if !m.escapeAnswersAlert() {
		// Esc is spoken for — a live selection or search takes it first,
		// and a card promising a key that does something else is the kind
		// of small lie that costs trust in every other hint.
		return mutedStyle().Render(label) + " "
	}
	return mutedStyle().Render(label+"  ·  Esc dismiss") + " "
}

// inputStripRows is the rows at the foot of the body that belong to an
// input rather than to content: the composer, the search line. The card
// floats above them instead of over them — covering the box someone is
// typing into is worse than covering anything else on screen.
func (m Model) inputStripRows() int {
	if m.mode == modeCompose {
		// Measured where renderInteraction lays it out, not at the pane's
		// full width — three columns of difference is a wrapped line, and
		// a lift one row short covers the rule it exists to clear.
		composerWidth, _ := m.interactionDimensions()
		// The composer is its input between two rules.
		return composerHeight(m.sendInput.Value(), max(1, composerWidth)) + 2
	}
	if m.ptyEnabled {
		// The portal is the pane; the card floats over the terminal and
		// ages out rather than reserving anything.
		return 0
	}
	// The transcript's foot row is never empty: the reply and search
	// affordances, the amber band telling the reader an agent is waiting
	// on them, or the search readout answering which match of how many.
	return 1
}

// alertCoversRow reports that the card is drawn over this screen row. The
// real terminal cursor sits at the agent's own cell and the card is
// composited over the grid without it knowing — an agent's prompt sits on
// its last output line, which is exactly where the card lands, so without
// this the cursor blinks inside the error box for the card's whole life.
func (m Model) alertCoversRow(row int) bool {
	width, height := m.bodyDimensions()
	rows := m.alertRows(width, height)
	if rows == 0 {
		return false
	}
	top := bodyTop + max(0, height-rows-m.inputStripRows())
	return row >= top && row < top+rows
}

// alertRows is how many rows the card will take, which is how much of the
// body a modal has to leave alone.
func (m Model) alertRows(width, height int) int {
	card := m.renderAlertCard(width, height)
	if card == "" {
		return 0
	}
	return strings.Count(card, "\n") + 1
}

// escapeAnswersAlert reports that Esc would reach the card rather than
// undoing something in front of it. It mirrors updateNormal's own order.
func (m Model) escapeAnswersAlert() bool {
	if m.normalPrefix != "" {
		// A half-typed chord takes Esc first, to cancel itself.
		return false
	}
	if m.selectionActive {
		return false
	}
	return !(m.activePane == paneInteraction && m.search.query != "")
}

func alignRight(content string, width int) string {
	if pad := width - lipgloss.Width(content); pad > 0 {
		return strings.Repeat(" ", pad) + content
	}
	return content
}

// alertDetailDimensions sizes the detail modal to the message: tall enough
// to hold it outright when the body has the room, scrolling when it does not.
func (m Model) alertDetailDimensions(width, height int) (int, int) {
	// Two passes, because the modal's width decides how many lines the
	// message takes and the line count decides its height. Measuring at
	// the preferred width instead would size the modal short of its own
	// text on every terminal narrow enough to be margined.
	modalWidth, _ := modalDimensions(width, height, alertCardWidth, 1)
	lines := wrapMessage(m.alertDetail.text, max(4, modalWidth-6))
	return modalDimensions(width, height, alertCardWidth, len(lines)+6)
}

// detailLayout is how the detail view divides its modal: the text area,
// and whether there is room for the title and the blank rows that separate
// it. It is computed in one place and read by the render, the scroll clamp
// and the footer hints — three readers disagreeing about the window is what
// once put the tail of a message out of reach of every scroll key.
type detailLayout struct {
	textWidth int
	window    int
	title     bool
	spaced    bool
}

// alertDetailLayout hands out the modal's rows in order of what the reader
// cannot do without: the keys that close the view, then the title that
// names it, then breathing room, and the message takes what is left.
// Sizing the message first and letting the remainder truncate is how the
// keys fell off the bottom while the footer went on advertising them.
func (m Model) alertDetailLayout(width, height int) detailLayout {
	modalWidth, modalHeight := m.alertDetailDimensions(width, height)
	layout := detailLayout{textWidth: max(4, modalWidth-6)}
	layout.window = max(1, max(1, modalHeight-2)-1)
	if layout.window > 1 {
		layout.title = true
		layout.window--
	}
	if layout.window > 2 {
		layout.spaced = true
		layout.window -= 2
	}
	return layout
}

// alertDetailLayoutHere is that layout for the region the view is actually
// drawn in — card and all. Measuring against the whole body while the view
// is rendered into less than that puts the tail of a long message out of
// reach: the state believes it is already at the bottom.
func (m Model) alertDetailLayoutHere() detailLayout {
	bodyWidth, bodyHeight := m.bodyDimensions()
	return m.alertDetailLayout(bodyWidth, m.modalRegion(bodyWidth, bodyHeight))
}

// alertDetailWindow is the text area inside the modal: how wide a line may
// be and how many of them are on screen at once.
func (m Model) alertDetailWindow() (int, int) {
	layout := m.alertDetailLayoutHere()
	return layout.textWidth, layout.window
}

// modalRegion is the room a modal has. A form makes way for the card: it
// is being typed into, and a card over the field is intolerable. An
// overlay that is only read does not — it is sized to its own content, so
// shrinking it silently cuts its last lines off, and it closes on any key.
// The card floats over that instead, which loses nothing either way.
func (m Model) modalRegion(width, height int) int {
	if m.mode.isReading() {
		return height
	}
	// F2 of the region's own arithmetic: the card is drawn inputStripRows
	// above the floor, so reserving only its own height leaves the two
	// overlapping by exactly that lift on the terminals where the modal
	// fills its region.
	// Never more than half the body: the card exists so that a failure
	// reflows nothing, and a form squeezed down to its own border is a
	// worse outcome than a card overlapping its foot.
	reserved := min(
		m.alertRows(width, height)+m.inputStripRows(),
		max(0, height/2),
	)
	if m.mode == modeDispatch && m.formFocus == dispatchName &&
		!m.dispatchNameVisibleIn(max(1, height-reserved)) {
		// Shrinking here would take away the row the reader is typing
		// into, and a failure that arrived on its own in the background
		// is the last thing that should move somebody's cursor. The card
		// overlaps the form's foot for the moment instead; that is
		// recoverable, and losing the next few keystrokes into the task
		// body is not.
		return height
	}
	return max(1, height-reserved)
}

// alertDetailScrolls reports that the open message is longer than the room
// it is drawn in, which is the only condition under which the scroll keys
// do anything.
func (m Model) alertDetailScrolls() bool {
	layout := m.alertDetailLayoutHere()
	return len(wrapMessage(m.alertDetail.text, layout.textWidth)) > layout.window
}

func (m Model) renderAlertModal(width, height int) string {
	modalWidth, modalHeight := m.alertDetailDimensions(width, height)
	layout := m.alertDetailLayout(width, height)

	lines := wrapMessage(m.alertDetail.text, layout.textWidth)
	offset := clamp(m.alertDetail.offset, 0, max(0, len(lines)-layout.window))
	visible := lines[offset:min(len(lines), offset+layout.window)]

	var out []string
	if layout.title {
		title := "  " + errorStyle().Bold(true).Render("Error")
		if age := alertAgeLabel(m.alertDetail.at); age != "" {
			title += mutedStyle().Render(age)
		}
		out = append(out, title)
		if layout.spaced {
			out = append(out, "")
		}
	}
	for _, line := range visible {
		out = append(out, "  "+line)
	}
	// Pad the text area to its full window so the keys sit on the modal's
	// last row rather than riding up under a short message.
	for index := len(visible); index < layout.window; index++ {
		out = append(out, "")
	}
	if layout.spaced {
		out = append(out, "")
	}
	keys := "y copy  ·  Esc close"
	if len(lines) > layout.window {
		keys = "j/k scroll  ·  " + keys
	}
	out = append(out, "  "+mutedStyle().Render(keys))
	return renderModal(strings.Join(out, "\n"), modalWidth, modalHeight)
}

// alertAgeLabel places a recalled message in time. A failure still on
// screen needs no stamp; one read minutes later does, because `e` reaches
// the last failure whether or not its card is still up.
func alertAgeLabel(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	if age := timeAgo(at); age != "" && age != "now" {
		return " · " + age + " ago"
	}
	return ""
}
