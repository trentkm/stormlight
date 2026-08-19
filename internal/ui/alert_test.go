package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

const sshFailure = "set up thaidex: ssh: connect to host thaidex port 22: " +
	"Connection refused\nkex_exchange_identification: read: " +
	"Connection reset by peer"

func alertModel(t *testing.T) Model {
	t.Helper()
	model := NewModel(stubBackend{})
	model.ready = true
	model.width = 100
	model.height = 30
	return model
}

func TestAlertCardShowsWholeMessageWhenItFits(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("workspace directory is unavailable: /Volumes/repos/gone"))

	card := ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if !strings.Contains(card, "/Volumes/repos/gone") {
		t.Fatalf("the path the message is about was cut:\n%s", card)
	}
	if strings.Contains(card, "…") {
		t.Fatalf("a message this short should not be elided:\n%s", card)
	}
}

func TestAlertCardWrapsRatherThanTruncates(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New(sshFailure))

	card := ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if !strings.Contains(unwrapped(card), unwrapped(sshFailure)) {
		t.Fatalf("the message did not survive the card whole:\n%s", card)
	}
	if !strings.Contains(card, "Esc dismiss") {
		t.Fatalf("the card does not say how to dismiss it:\n%s", card)
	}
}

// unwrapped reads a rendered block back as running text: the card's frame
// and the line breaks it introduced are layout, not message.
func unwrapped(value string) string {
	value = strings.Map(func(r rune) rune {
		if strings.ContainsRune("│╭╮╰╯─!", r) {
			return -1
		}
		return r
	}, ansi.Strip(value))
	return strings.Join(strings.Fields(value), " ")
}

func TestAlertCardOffersTheRestWhenItRunsOut(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New(strings.Repeat("stderr says something long. ", 30)))

	card := ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if lines := strings.Count(card, "\n") + 1; lines > alertCardLines+3 {
		t.Fatalf("the card grew past its budget (%d lines):\n%s", lines, card)
	}
	if !strings.Contains(card, "e read it all") {
		t.Fatalf("a clipped card must offer the rest:\n%s", card)
	}
}

// The Spanreed is a real terminal sized 1:1 to its pane. A card that took
// rows of its own would resize every agent's screen each time something
// failed, so it floats instead.
func TestAlertCardDoesNotResizeTheBody(t *testing.T) {
	model := alertModel(t)
	width, height := model.bodyDimensions()

	model.raise(errors.New(sshFailure))
	alertWidth, alertHeight := model.bodyDimensions()
	if alertWidth != width || alertHeight != height {
		t.Fatalf("body geometry moved under the card: %dx%d, want %dx%d",
			alertWidth, alertHeight, width, height)
	}

	body := strings.Split(model.renderBody(), "\n")
	if len(body) != height {
		t.Fatalf("body rendered %d rows, want %d", len(body), height)
	}
	if !strings.Contains(
		ansi.Strip(strings.Join(body, "\n")), "kex_exchange_identification") {
		t.Fatalf("the card never made it onto the body:\n%s", strings.Join(body, "\n"))
	}
}

func TestAlertDetailOpensTheWholeMessageAndClosesTheCard(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New(sshFailure))

	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)
	if model.mode != modeAlert {
		t.Fatalf("mode = %v, want modeAlert", model.mode)
	}
	if model.alert.active() {
		t.Fatalf("reading the message left the card standing")
	}

	view := ansi.Strip(model.renderAlertModal(model.bodyDimensions()))
	if !strings.Contains(view, "kex_exchange_identification") ||
		!strings.Contains(view, "y copy") {
		t.Fatalf("detail view is missing the message or its keys:\n%s", view)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next := updated.(Model); next.mode != modeNormal {
		t.Fatalf("Esc left the detail view open: mode = %v", next.mode)
	}
}

func TestAlertDetailScrollsAndCopies(t *testing.T) {
	model := alertModel(t)
	model.height = 14
	model.raise(errors.New(strings.Repeat("a line of failure output. ", 60)))

	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)

	updated, _ = model.Update(runeKey("j"))
	if next := updated.(Model); next.alertDetail.offset != 1 {
		t.Fatalf("offset = %d, want 1", next.alertDetail.offset)
	}
	model = updated.(Model)

	updated, cmd := model.Update(runeKey("y"))
	if cmd == nil {
		t.Fatalf("y did not copy the message")
	}
	if next := updated.(Model); next.mode != modeAlert {
		t.Fatalf("copying closed the detail view: mode = %v", next.mode)
	}
}

// A refresh against a daemon that is down fails every tick. That is one
// failure repeating, not a new one each time, so its clock keeps running.
func TestRepeatedFailureIsTheSameCard(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("daemon is not listening"))
	model.alert.expires = time.Now().Add(time.Second)

	model.raise(errors.New("daemon is not listening"))
	if model.alert.expires.IsZero() {
		t.Fatalf("an identical failure restarted the card's clock")
	}

	model.raise(errors.New("something else went wrong"))
	if !model.alert.expires.IsZero() {
		t.Fatalf("a different failure kept the old card's clock")
	}
}

// Inside the portal Esc belongs to the agent, so the card cannot be
// dismissed by hand there and ages out instead.
func TestAlertAgesOutOnlyWhereItCannotBeDismissed(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	model.raise(errors.New("interrupt failed"))

	now := time.Now()
	model.ageAlert(now)
	if model.alert.expires.IsZero() {
		t.Fatalf("the portal did not start the card's clock")
	}
	model.ageAlert(now.Add(alertLinger + time.Second))
	if model.alert.active() {
		t.Fatalf("the card outlived its linger inside the portal")
	}

	model.raise(errors.New("interrupt failed"))
	model.activePane = paneAgents
	model.ageAlert(now.Add(alertLinger + time.Second))
	if !model.alert.active() {
		t.Fatalf("a dismissible card aged out anyway")
	}
	if !model.alert.expires.IsZero() {
		t.Fatalf("leaving the portal left the clock running")
	}
}

// A form's complaint has nothing left to say once the form is gone.
func TestLeavingAModeClearsItsComplaint(t *testing.T) {
	model := alertModel(t)
	model.mode = modeRename
	model.renameInput = newLineInput("New name")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.alert.message() != "name cannot be empty" {
		t.Fatalf("rename accepted an empty name: %v", model.alert.err)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.mode != modeNormal {
		t.Fatalf("Esc did not leave the rename form: mode = %v", model.mode)
	}
	if model.alert.active() {
		t.Fatalf("the form's complaint outlived the form: %v", model.alert.err)
	}
}

func TestWrapMessageKeepsTheLinesTheErrorChose(t *testing.T) {
	lines := wrapMessage("first line\nsecond line that is long enough to fold", 20)
	if len(lines) < 3 || lines[0] != "first line" {
		t.Fatalf("wrap lost the error's own structure: %#v", lines)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 20 {
			t.Fatalf("line %q overruns the width", line)
		}
	}
}

// A small terminal still gets the message, and the card still fits inside
// the frame rather than pushing it around.
func TestAlertCardFitsASmallTerminal(t *testing.T) {
	model := alertModel(t)
	model.width = 54
	model.height = 14
	model.raise(errors.New(sshFailure))

	assertViewFitsPane(t, model, model.width, model.height)
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "connect to host") {
		t.Fatalf("the message never reached a narrow screen:\n%s", view)
	}
	if !strings.Contains(view, "e read it all") {
		t.Fatalf("a card clipped by a small screen must offer the rest:\n%s", view)
	}
}

// Opening something is not leaving anything: a card raised on the dashboard
// is still there when the reader comes back from the help.
func TestAlertSurvivesATripThroughTheHelp(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("interrupt failed"))

	updated, _ := model.Update(runeKey("?"))
	model = updated.(Model)
	if model.mode != modeHelp {
		t.Fatalf("? did not open the help: mode = %v", model.mode)
	}
	if !model.alert.active() {
		t.Fatalf("opening the help dismissed the card")
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.mode != modeNormal {
		t.Fatalf("the help did not close: mode = %v", model.mode)
	}
	if !model.alert.active() {
		t.Fatalf("closing the help dismissed a card the help never raised")
	}
}

// The refresh is the one failure that reports its own recovery: it runs on
// a timer, so a card insisting the daemon is down after it came back is a
// lie the reader never asked for.
func TestRecoveredPollClearsItsOwnCard(t *testing.T) {
	model := alertModel(t)

	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)
	if !model.alert.active() {
		t.Fatalf("a failed refresh raised no card")
	}

	updated, _ = model.Update(dashboardMsg{})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the daemon came back and the card stayed: %v", next.alert.err)
	}
}

// Succeeding at something else is not an answer to an unread failure.
func TestSuccessElsewhereLeavesTheCardStanding(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("interrupt failed"))

	updated, _ := model.Update(actionMsg{})
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("an unrelated success wiped the card")
	}

	// Nor does a refresh that was never the thing complaining.
	updated, _ = updated.(Model).Update(dashboardMsg{})
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("a successful refresh answered a failure it did not raise")
	}
}

// Where no key is the dashboard's — inside the portal, under a form — the
// card names none, and says so by ending in an ellipsis rather than
// mid-word.
func TestClippedCardAdmitsThereIsMoreAndClaimsNoKeys(t *testing.T) {
	model := alertModel(t)
	model.mode = modeDispatch
	model.raise(errors.New(strings.Repeat("stderr says something long. ", 30)))

	card := ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if !strings.Contains(card, "…") {
		t.Fatalf("a clipped card gave no sign it was clipped:\n%s", card)
	}
	for _, claim := range []string{"Esc dismiss", "e detail", "e read it all"} {
		if strings.Contains(card, claim) {
			t.Fatalf("the card claimed %q, which belongs to the form:\n%s", claim, card)
		}
	}
}

// `e` reaches the last failure even after its card is gone. Inside the
// portal no key is ours, so the card ages out unread — recall is the only
// thing that makes those messages readable at all.
func TestDetailRecallsTheLastFailureAfterItsCardIsGone(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	model.raise(errors.New(sshFailure))

	moment := time.Now()
	model.ageAlert(moment)
	model.ageAlert(moment.Add(alertLinger + time.Second))
	if model.alert.active() {
		t.Fatalf("the card should have aged out inside the portal")
	}

	model.activePane = paneAgents
	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)
	if model.mode != modeAlert {
		t.Fatalf("e did not recall the last failure: mode = %v", model.mode)
	}
	view := ansi.Strip(model.renderAlertModal(model.bodyDimensions()))
	if !strings.Contains(unwrapped(view), unwrapped(sshFailure)) {
		t.Fatalf("the recalled message is not the failure:\n%s", view)
	}
}

// The card is anchored to the foot of the body and a modal centers in what
// is left, so the card never lands on the form it is complaining about.
func TestModalKeepsItsRowsWhenACardStands(t *testing.T) {
	// Sizes matter here: a roomy terminal separates the two by luck, and
	// it is the ordinary ones — where the form fills most of the body —
	// that the card used to land on.
	for _, size := range [][2]int{{80, 24}, {90, 30}, {100, 34}, {120, 40}} {
		model := alertModel(t)
		model.width, model.height = size[0], size[1]
		model.mode = modeDispatch
		model.raise(errors.New(sshFailure))

		width, height := model.bodyDimensions()
		modalTop, modalBottom := frameSpan(model.renderModeBody(width, height), 1)
		cardTop, cardBottom := frameSpan(model.renderBody(), 0)
		if modalTop < 0 || cardTop < 0 {
			t.Fatalf("%dx%d: expected a form and a card (form %d..%d, card %d..%d)",
				size[0], size[1], modalTop, modalBottom, cardTop, cardBottom)
		}
		if cardTop <= modalBottom {
			t.Fatalf("%dx%d: the card lands on the form: form rows %d..%d, card rows %d..%d",
				size[0], size[1], modalTop, modalBottom, cardTop, cardBottom)
		}
	}
}

// frameSpan finds the rows of a rounded frame drawn at more than `indent`
// columns in (a centered modal) or at exactly one (the card, against the
// left edge).
func frameSpan(block string, minIndent int) (int, int) {
	top, bottom := -1, -1
	for index, line := range strings.Split(ansi.Strip(block), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if minIndent == 0 && indent != 1 || minIndent > 0 && indent <= minIndent {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "╭") && top < 0:
			top = index
		case strings.HasPrefix(trimmed, "╰"):
			bottom = index
		}
	}
	return top, bottom
}

// A modal sized short of its own text makes the reader scroll for nothing.
func TestDetailFitsItsMessageWhenTheBodyHasRoom(t *testing.T) {
	model := alertModel(t)
	model.width = 61
	model.height = 40
	model.raise(errors.New(strings.Repeat("a line of failure output. ", 12)))

	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)

	textWidth, window := model.alertDetailWindow()
	if lines := wrapMessage(model.alertDetail.text, textWidth); len(lines) > window {
		t.Fatalf("modal holds %d of %d lines with %d rows of body free",
			window, len(lines), model.height)
	}
}

// A card is two borders, a line, and its keys. Rendering one into fewer
// rows than that is how it loses its bottom edge.
func TestCardKeepsItsFrameOnAShortTerminal(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("boom"))

	card := ansi.Strip(model.renderAlertCard(60, 4))
	if !strings.Contains(card, "╰") {
		t.Fatalf("the card lost its bottom edge:\n%s", card)
	}
	if lines := strings.Count(card, "\n") + 1; lines > 4 {
		t.Fatalf("the card rendered %d rows into 4", lines)
	}
	// Too short for a frame is not too short for the message: going quiet
	// because the terminal is small is the one thing this must never do.
	shorter := ansi.Strip(model.renderAlertCard(60, 3))
	if strings.Contains(shorter, "╭") || strings.Contains(shorter, "╰") {
		t.Fatalf("a frame drew into three rows:\n%s", shorter)
	}
	if !strings.Contains(shorter, "boom") {
		t.Fatalf("a short terminal was told nothing:\n%q", shorter)
	}
}

func TestFooterNamesTheDetailViewsOwnKeys(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New("boom"))
	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)

	footer := ansi.Strip(model.renderFooter())
	if !strings.Contains(footer, "y copy") || !strings.Contains(footer, "Esc close") {
		t.Fatalf("footer does not name the detail view's keys: %q", footer)
	}
	if strings.Contains(footer, "n new") || strings.Contains(footer, "q quit") {
		t.Fatalf("footer advertises keys the detail view does not answer: %q", footer)
	}
}

// A message read minutes after its card is gone has to place itself in
// time; one still on screen does not.
func TestRecalledFailureCarriesItsAge(t *testing.T) {
	moment := time.Now()
	alertClock = func() time.Time { return moment.Add(-7 * time.Minute) }
	t.Cleanup(func() { alertClock = time.Now })

	model := alertModel(t)
	model.raise(errors.New("interrupt failed"))
	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)

	view := ansi.Strip(model.renderAlertModal(model.bodyDimensions()))
	if !strings.Contains(view, "7m ago") {
		t.Fatalf("a recalled failure did not say when it happened:\n%s", view)
	}

	alertClock = time.Now
	fresh := alertModel(t)
	fresh.raise(errors.New("interrupt failed"))
	updated, _ = fresh.Update(runeKey("e"))
	view = ansi.Strip(updated.(Model).renderAlertModal(fresh.bodyDimensions()))
	if strings.Contains(view, "ago") {
		t.Fatalf("a failure that just happened stamped itself anyway:\n%s", view)
	}
}

// The poll fires every 700ms. A failure arriving while someone is reading
// another one must not swap the text out from under them.
func TestOpenDetailIsNotOverwrittenByANewFailure(t *testing.T) {
	model := alertModel(t)
	model.raise(errors.New(sshFailure))

	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)

	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon poll failed")})
	model = updated.(Model)

	view := ansi.Strip(model.renderAlertModal(model.bodyDimensions()))
	if strings.Contains(view, "daemon poll failed") {
		t.Fatalf("a background failure rewrote the message being read:\n%s", view)
	}
	if !strings.Contains(unwrapped(view), unwrapped(sshFailure)) {
		t.Fatalf("the message being read went missing:\n%s", view)
	}
	if model.alertDetail.text == model.lastFailure.text {
		t.Fatalf("the open view and the recall slot are the same storage")
	}
}

// A dismissal that lasts 700ms is not a dismissal: the poll would raise the
// identical failure on the next tick, forever.
func TestDismissingARecurringFailureSticks(t *testing.T) {
	model := alertModel(t)
	down := func() dashboardMsg {
		return dashboardMsg{err: errors.New("daemon is not listening")}
	}

	updated, _ := model.Update(down())
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.alert.active() {
		t.Fatalf("Esc did not take the card down")
	}

	updated, _ = model.Update(down())
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the next tick raised the failure the reader just dismissed")
	}
	model = updated.(Model)

	// Recovery lifts the hush: if it breaks again after working, that is
	// news.
	updated, _ = model.Update(dashboardMsg{})
	model = updated.(Model)
	updated, _ = model.Update(down())
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("a failure that returned after a recovery stayed silent")
	}
}

// Reading a recurring failure dismisses it too, so it must not reappear as
// a card underneath the view that is reading it.
func TestReadingARecurringFailureDoesNotRaiseItAgain(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)

	updated, _ = model.Update(runeKey("e"))
	model = updated.(Model)

	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the card came back under the view opened to read it")
	}
}

// Any key closes an informational overlay, so treating that key as an
// answer to a failure is the keystroke-wipe this surface exists to remove.
func TestClosingAnInformationalOverlayKeepsTheFailure(t *testing.T) {
	model := alertModel(t)
	model.mode = modeHistory
	model.raise(errors.New("session history is unreadable"))

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.mode != modeNormal {
		t.Fatalf("Esc did not close the history: mode = %v", model.mode)
	}
	if !model.alert.active() {
		t.Fatalf("closing the history discarded the failure it was showing")
	}
}

// The window the scroll keys clamp to must be the one the message is drawn
// into, card and all, or the tail is unreachable by every scroll key: the
// state believes it is already at the bottom.
func TestScrollReachesTheEndWhileACardStands(t *testing.T) {
	model := alertModel(t)
	model.width = 100
	model.height = 30
	// Distinct lines, because a message that repeats itself cannot tell a
	// test whether it is looking at the last line or the tenth.
	numbered := make([]string, 40)
	for index := range numbered {
		numbered[index] = fmt.Sprintf("failure detail line %02d", index+1)
	}
	model.raise(errors.New(strings.Join(numbered, "\n")))

	updated, _ := model.Update(runeKey("e"))
	model = updated.(Model)
	// A poll failure raises a card under the open view, which is what
	// shrinks the room the message is drawn into.
	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon poll failed")})
	model = updated.(Model)
	if !model.alert.active() {
		t.Fatalf("expected a card standing under the detail view")
	}

	updated, _ = model.Update(runeKey("G"))
	model = updated.(Model)

	// Read the screen, not the state that decided what to put on it.
	view := ansi.Strip(model.renderBody())
	if !strings.Contains(view, numbered[len(numbered)-1]) {
		t.Fatalf("G left the last line of the message unreachable:\n%s", view)
	}
}

// A keys row too wide for the card wraps, costing the card the row its
// frame was budgeted and the bottom border with it.
func TestNarrowCardDropsItsKeysRatherThanItsFrame(t *testing.T) {
	model := alertModel(t)
	model.width = 25
	model.height = 7
	model.raise(errors.New("boom"))

	width, height := model.bodyDimensions()
	card := ansi.Strip(model.renderAlertCard(width, height))
	if !strings.Contains(card, "╰") {
		t.Fatalf("the card lost its bottom edge:\n%s", card)
	}
	if rows := strings.Count(card, "\n") + 1; rows > height {
		t.Fatalf("the card rendered %d rows into a body of %d:\n%s", rows, height, card)
	}
}

// Timing out is the one ending where the reader provably did not read it.
// Hushing there is how a dashboard goes quiet about a daemon still down.
func TestTimingOutDoesNotHushAFailure(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)

	moment := time.Now()
	model.ageAlert(moment)
	model.ageAlert(moment.Add(alertLinger + time.Second))
	if model.alert.active() {
		t.Fatalf("the card should have timed out inside the portal")
	}

	model.activePane = paneAgents
	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("a failure nobody read was silenced by its own timeout")
	}
}

// Opening a form drops its stale complaint, which must not silence a
// self-repeating failure the reader never answered.
func TestOpeningAFormDoesNotHushAPolledFailure(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)

	updated, _ = model.Update(runeKey("n"))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)

	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("opening a form silenced a failure the reader never answered")
	}
}

// Asking again is asking to be told: a hushed failure that is still
// failing has to answer an explicit refresh, or the retry looks like
// success.
func TestExplicitRefreshBreaksTheHush(t *testing.T) {
	model := alertModel(t)
	down := func() dashboardMsg {
		return dashboardMsg{err: errors.New("daemon is not listening")}
	}
	updated, _ := model.Update(down())
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)

	updated, _ = model.Update(runeKey("r"))
	model = updated.(Model)
	updated, _ = model.Update(down())
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("an explicit refresh that failed again said nothing")
	}
}

// A failure that merely arrived while a form was open belongs to nothing
// on screen; cancelling that form is not an answer to it.
func TestCancellingAFormKeepsAnUnrelatedFailure(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.Update(runeKey("n"))
	model = updated.(Model)
	if !model.mode.isForm() {
		t.Fatalf("n did not open a form: mode = %v", model.mode)
	}

	// An ssh setup started earlier finishes badly while the form is open.
	updated, _ = model.Update(machinePreparedMsg{
		host: "thaidex", err: errors.New("ssh: could not resolve hostname"),
	})
	model = updated.(Model)
	if !model.alert.active() {
		t.Fatalf("the setup failure raised nothing")
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("cancelling the form discarded a failure it did not raise")
	}
}

// Esc keeps meaning what it meant: drop the selection, clear the search,
// and only then answer the card.
func TestEscapeAnswersTheCardLast(t *testing.T) {
	model := alertModel(t)
	// The transcript's own selection, so the keyboard is the dashboard's
	// rather than the agent's.
	model.ptyEnabled = false
	model.activePane = paneInteraction
	model.selectionActive = true
	model.raise(errors.New("interrupt failed"))

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.selectionActive {
		t.Fatalf("Esc did not drop the selection")
	}
	if !model.alert.active() {
		t.Fatalf("Esc answered the card while a selection was still up")
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the next Esc did not answer the card")
	}
}

// The card floats above the box someone is typing into, never over it.
func TestCardStaysOffTheComposer(t *testing.T) {
	model := alertModel(t)
	model.mode = modeCompose
	model.sendInput.SetValue("a reply in progress")
	model.raise(errors.New(sshFailure))

	_, height := model.bodyDimensions()
	lift := model.inputStripRows()
	if lift < 3 {
		t.Fatalf("the composer strip measured %d rows", lift)
	}
	_, cardBottom := frameSpan(model.renderBody(), 0)
	if cardBottom < 0 {
		t.Fatalf("no card rendered")
	}
	if cardBottom > height-1-lift {
		t.Fatalf("the card reaches row %d, into the composer's last %d rows of %d",
			cardBottom, lift, height)
	}
}

// With no card there is nothing to composite, and the body must go out
// exactly as the mode drew it.
func TestBodyIsUntouchedWithoutACard(t *testing.T) {
	model := alertModel(t)
	width, height := model.bodyDimensions()
	if got, want := model.renderBody(), model.renderModeBody(width, height); got != want {
		t.Fatalf("an empty card still rewrote the body:\n%q\n\n%q", got, want)
	}
}

// A form may only claim the card it actually raised. One standing for
// another reason is not the form's to take with it when it closes.
func TestAFormDoesNotClaimACardItDidNotRaise(t *testing.T) {
	model := alertModel(t)
	repeated := "workspace directory is unavailable: /gone"
	updated, _ := model.Update(dashboardMsg{err: errors.New(repeated)})
	model = updated.(Model)
	if model.alert.raisedIn != modeNormal {
		t.Fatalf("a polled failure was tagged %v", model.alert.raisedIn)
	}

	// The same text arrives again, this time as a form's own objection.
	model.mode = modeAddWorkspace
	model.complain(errors.New(repeated))
	if model.alert.raisedIn != modeNormal {
		t.Fatalf("the form claimed a card it did not raise: %v", model.alert.raisedIn)
	}
}

// The task composer is persistent state sized from the room the modal has.
// A card shortens that room, and a textarea that believes it is taller than
// the box it is drawn into scrolls itself and stays scrolled.
func TestDispatchComposerIsSizedForTheModalActuallyDrawn(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.beginDispatch(false)
	model = updated.(Model)
	if model.mode != modeDispatch {
		t.Fatalf("the dispatch form did not open: mode = %v", model.mode)
	}

	updated, _ = model.Update(dashboardMsg{err: errors.New(sshFailure)})
	model = updated.(Model)
	if !model.alert.active() {
		t.Fatalf("expected a card under the form")
	}

	// Derived independently of dispatchContentDimensions: this is the
	// shape renderModeBody draws.
	bodyWidth, bodyHeight := model.bodyDimensions()
	region := model.modalRegion(bodyWidth, bodyHeight)
	modalWidth, modalHeight := model.dispatchModalDimensions(bodyWidth, region)
	drawn := model.dispatchLayout(max(1, modalWidth-2), max(1, modalHeight-2))
	if got := model.taskInput.Height(); got != drawn.taskHeight {
		t.Fatalf("composer kept at %d rows while the form draws it at %d",
			got, drawn.taskHeight)
	}
}

// The composer is laid out narrower than its pane, and three columns is a
// wrapped line: a lift measured at the wrong width covers the rule it
// exists to clear.
func TestComposerStripIsMeasuredWhereTheComposerIsDrawn(t *testing.T) {
	model := alertModel(t)
	model.mode = modeCompose
	width, _ := model.bodyDimensions()
	composerWidth, _ := model.interactionDimensions()
	_, _, paneWidth := model.paneWidths(width)
	if composerWidth == paneWidth {
		t.Skip("this layout does not distinguish the two widths")
	}

	// A draft that fits on one line at the pane's width and wraps at the
	// composer's.
	model.sendInput.SetValue(strings.Repeat("x", composerWidth))
	if got, want := model.inputStripRows(),
		composerHeight(model.sendInput.Value(), composerWidth)+2; got != want {
		t.Fatalf("strip measured %d rows, composer draws %d", got, want)
	}
	if model.inputStripRows() ==
		composerHeight(model.sendInput.Value(), paneWidth)+2 {
		t.Fatalf("the strip was measured at the pane width, not the composer's")
	}
}

// A card that timed out unread takes its polled flag with it, but reading
// the message afterwards is still answering it.
func TestReadingATimedOutPollHushesIt(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)

	moment := time.Now()
	model.ageAlert(moment)
	model.ageAlert(moment.Add(alertLinger + time.Second))
	if model.alert.active() {
		t.Fatalf("the card should have timed out")
	}

	model.activePane = paneAgents
	updated, _ = model.Update(runeKey("e"))
	model = updated.(Model)
	if model.mode != modeAlert {
		t.Fatalf("e did not open the message: mode = %v", model.mode)
	}

	updated, _ = model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the card came back over the view opened to read it")
	}
}

// A failure that arrives first from an action and then repeats from the
// poll is the poll's card too, or it never clears when the daemon returns.
func TestAFailureThePollRepeatsLearnsToClearItself(t *testing.T) {
	model := alertModel(t)
	shared := "read unix ->/tmp/stormlight/daemon.sock: i/o timeout"

	updated, _ := model.Update(actionMsg{err: errors.New(shared)})
	model = updated.(Model)
	updated, _ = model.Update(dashboardMsg{err: errors.New(shared)})
	model = updated.(Model)
	if !model.alert.active() {
		t.Fatalf("the card went missing")
	}

	updated, _ = model.Update(dashboardMsg{})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the daemon came back and the card kept insisting it was down")
	}
}

// The card must not name a key that does something else first.
func TestCardNamesEscapeOnlyWhenEscapeAnswersIt(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = false
	model.activePane = paneInteraction
	model.raise(errors.New("interrupt failed"))

	card := ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if !strings.Contains(card, "Esc dismiss") {
		t.Fatalf("with nothing in front of it the card should name Esc:\n%s", card)
	}

	model.search.query = "needle"
	card = ansi.Strip(model.renderAlertCard(model.bodyDimensions()))
	if strings.Contains(card, "Esc dismiss") {
		t.Fatalf("Esc clears the search first; the card must not claim it:\n%s", card)
	}
	if !strings.Contains(card, "e detail") {
		t.Fatalf("the card dropped the key it does own:\n%s", card)
	}
}

// errorWithSlice is not comparable: two interfaces holding it panic on ==.
type errorWithSlice struct{ parts []string }

func (e errorWithSlice) Error() string { return strings.Join(e.parts, " ") }

// A dependency's value-typed error must not take the dashboard down on the
// next keypress.
func TestANonComparableErrorDoesNotPanicOnTheNextKey(t *testing.T) {
	model := alertModel(t)
	model.mode = modeRename
	model.renameInput = newLineInput("New name")
	model.complain(errorWithSlice{parts: []string{"could not", "rename"}})

	// Any key at all: the mode-close check compares the standing failure
	// against itself.
	updated, _ := model.Update(runeKey("j"))
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("the complaint vanished")
	}
}

// Recovery hands the card's rows back to the modal, and whatever was
// sized for the shorter one has to hear about it.
func TestRecoveryRefitsTheDispatchComposer(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.Update(dashboardMsg{err: errors.New("daemon is not listening")})
	model = updated.(Model)
	updated, _ = model.beginDispatch(false)
	model = updated.(Model)

	updated, _ = model.Update(dashboardMsg{})
	model = updated.(Model)
	if model.alert.active() {
		t.Fatalf("the card outlived the recovery")
	}

	bodyWidth, bodyHeight := model.bodyDimensions()
	region := model.modalRegion(bodyWidth, bodyHeight)
	modalWidth, modalHeight := model.dispatchModalDimensions(bodyWidth, region)
	drawn := model.dispatchLayout(max(1, modalWidth-2), max(1, modalHeight-2))
	if got := model.taskInput.Height(); got != drawn.taskHeight {
		t.Fatalf("composer left at %d rows while the form draws it at %d",
			got, drawn.taskHeight)
	}
}

// Opening a form is not reading the failure that happened to be on screen.
func TestOpeningAFormKeepsAFailureItDidNotRaise(t *testing.T) {
	for _, open := range []struct {
		name string
		key  string
	}{{"dispatch", "n"}, {"history", "H"}, {"mark", "m"}, {"rename", "R"}} {
		model := alertModel(t)
		model.activePane = paneAgents
		model.raise(errors.New("set up thaidex: ssh: could not resolve hostname"))

		updated, _ := model.Update(runeKey(open.key))
		if next := updated.(Model); !next.alert.active() {
			t.Fatalf("%s wiped an unread failure it did not raise", open.name)
		}
	}
}

// A form still drops its own stale objection when it reopens.
func TestReopeningAFormDropsItsOwnStaleComplaint(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.beginDispatch(false)
	model = updated.(Model)
	model.formFocus = dispatchTask
	model.taskInput.SetValue("")

	updated, _ = model.submitDispatch()
	model = updated.(Model)
	if model.alert.raisedIn != modeDispatch {
		t.Fatalf("the form's objection was tagged %v: %v",
			model.alert.raisedIn, model.alert.err)
	}
	model.mode = modeNormal

	updated, _ = model.beginDispatch(false)
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the form reopened still wearing its last objection: %v",
			next.alert.err)
	}
}

// Inside the portal a card cannot be answered, so one that timed out must
// not return every twelve seconds for as long as the daemon is down — and
// must return the moment the reader is back where they can act on it.
func TestATimedOutCardWaitsForTheReaderToComeBack(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	down := func() dashboardMsg {
		return dashboardMsg{err: errors.New("daemon is not listening")}
	}
	updated, _ := model.Update(down())
	model = updated.(Model)

	moment := time.Now()
	model.ageAlert(moment)
	model.ageAlert(moment.Add(alertLinger + time.Second))
	if model.alert.active() {
		t.Fatalf("the card should have timed out")
	}

	updated, _ = model.Update(down())
	model = updated.(Model)
	if model.alert.active() {
		t.Fatalf("the card came back over the terminal on the next tick")
	}

	model.activePane = paneAgents
	updated, _ = model.Update(down())
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("the failure never returned once the reader could answer it")
	}
}

// A card must not cost the body a column it drew.
func TestCardDoesNotCropTheBody(t *testing.T) {
	model := alertModel(t)
	width, height := model.bodyDimensions()
	bare := blockWidth(model.renderModeBody(width, height))

	model.raise(errors.New(sshFailure))
	if got := blockWidth(model.renderBody()); got != bare {
		t.Fatalf("body is %d columns wide with a card, %d without", got, bare)
	}
}

// The footer names the scroll keys only when there is something to scroll.
func TestFooterNamesScrollOnlyWhenTheMessageScrolls(t *testing.T) {
	short := alertModel(t)
	short.raise(errors.New("boom"))
	updated, _ := short.Update(runeKey("e"))
	if footer := ansi.Strip(updated.(Model).renderFooter()); strings.Contains(
		footer, "j/k scroll",
	) {
		t.Fatalf("a one-line message advertised scrolling: %q", footer)
	}

	long := alertModel(t)
	long.raise(errors.New(strings.Repeat("a line of failure output. ", 60)))
	updated, _ = long.Update(runeKey("e"))
	if footer := ansi.Strip(updated.(Model).renderFooter()); !strings.Contains(
		footer, "j/k scroll",
	) {
		t.Fatalf("a message that scrolls did not say so: %q", footer)
	}
}

// A path that worked disproves the form's objection to the last one — and
// that form closes on a message, not a keystroke, so the sweep never sees
// it.
func TestASuccessfulAddRetractsItsComplaint(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.beginAddWorkspace()
	model = updated.(Model)
	updated, _ = model.submitAddWorkspace("/definitely/not/here")
	model = updated.(Model)
	if model.alert.raisedIn != modeAddWorkspace {
		t.Fatalf("the form's objection was tagged %v: %v",
			model.alert.raisedIn, model.alert.err)
	}

	updated, _ = model.Update(workspaceAddedMsg{
		value: workspace.Context{ID: "w1", Root: "/tmp", Name: "tmp"},
	})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the add succeeded and its complaint stayed: %v", next.alert.err)
	}
}

// A message that sends disproves "message cannot be empty", and the
// composer outlives the send.
func TestASuccessfulSendRetractsItsComplaint(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = false
	model.agents = []agent.Agent{{ID: "a1", Name: "one", ProcessLive: true}}
	model.rebuildGroups("", "a1")
	model.activePane = paneInteraction
	model.mode = modeCompose
	model.sendInput.Focus()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.alert.message() != "message cannot be empty" {
		t.Skipf("this model cannot reach the empty-message guard: %v", model.alert.err)
	}

	model.sendInput.SetValue("a real message")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next := updated.(Model); next.alert.active() {
		t.Fatalf("the send left its own objection standing: %v", next.alert.err)
	}
}

// The path navigator serves the add-workspace picker too, so it must clear
// whichever form is using it.
func TestThePathNavigatorClearsTheFormUsingIt(t *testing.T) {
	model := alertModel(t)
	updated, _ := model.beginAddWorkspace()
	model = updated.(Model)
	model.complain(errors.New("no such directory: /definitely/not/here"))
	if model.alert.raisedIn != modeAddWorkspace {
		t.Fatalf("complaint tagged %v", model.alert.raisedIn)
	}

	model.clearComplaint(model.mode)
	if model.alert.active() {
		t.Fatalf("the picker's own complaint survived its success: %v", model.alert.err)
	}
}

// A card shrinks the form exactly like a smaller terminal does, so the
// same guard has to run: typing must not land in a field that is no longer
// drawn.
func TestACardNeverLeavesFocusOnAnUndrawnField(t *testing.T) {
	for height := 20; height <= 26; height++ {
		model := alertModel(t)
		model.width = 120
		model.height = height
		updated, _ := model.beginDispatch(false)
		model = updated.(Model)
		if !model.dispatchNameVisible() {
			continue
		}
		model.formFocus = dispatchName
		model.focusForm()

		updated, _ = model.Update(dashboardMsg{err: errors.New(sshFailure)})
		model = updated.(Model)
		if !model.dispatchNameVisible() && model.formFocus == dispatchName {
			t.Fatalf("at height %d focus stayed on a name row the form no longer draws",
				height)
		}
	}
}

// Read-only overlays are sized to their own content, so taking rows out of
// them cuts their last lines off. They keep their full height and the card
// floats over them instead — a failure must never be invisible, which is
// the whole point of the surface.
func TestReadingOverlaysKeepTheirLastLinesAndStillShowFailures(t *testing.T) {
	// 120x44: tall enough that the help draws its final entry, so
	// shrinking by the card's rows is visible as content loss rather than
	// as a border that moves.
	model := alertModel(t)
	model.width = 120
	model.height = 44
	width, height := model.bodyDimensions()
	if !strings.Contains(
		ansi.Strip(model.renderHelpModal(width, height)), "quit the dashboard",
	) {
		t.Skip("this terminal is too short for the help's last entry")
	}

	model.raise(errors.New(sshFailure))
	model.mode = modeHelp
	if got := ansi.Strip(model.renderModeBody(width, height)); !strings.Contains(
		got, "quit the dashboard",
	) {
		t.Fatalf("the help lost its last entry to the card:\n%s", got)
	}
	if model.renderAlertCard(width, height) == "" {
		t.Fatalf("the failure was invisible while the help was open")
	}
}

// A failure raised while a reading overlay is open is exactly the case the
// card must not swallow: nothing else on screen will mention it.
func TestAFailureRaisedUnderAReadingOverlayIsVisible(t *testing.T) {
	for _, surface := range []mode{modeHelp, modeInfo, modeHistory, modeAlert} {
		model := alertModel(t)
		model.mode = surface
		model.raise(errors.New("session history is unreadable"))

		width, height := model.bodyDimensions()
		if !strings.Contains(
			ansi.Strip(model.renderBody()), "session history is unreadable",
		) {
			t.Fatalf("mode %v swallowed the failure raised under it", surface)
		}
		if model.renderAlertCard(width, height) == "" {
			t.Fatalf("mode %v drew no card", surface)
		}
	}
}

func lastLine(block string) string {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(ansi.Strip(lines[index])) != "" {
			return lines[index]
		}
	}
	return ""
}

// Recovery is news everywhere, including for a card held back inside the
// portal.
func TestRecoveryLiftsTheHoldBackToo(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = true
	model.activePane = paneInteraction
	down := func() dashboardMsg {
		return dashboardMsg{err: errors.New("daemon is not listening")}
	}
	updated, _ := model.Update(down())
	model = updated.(Model)
	moment := time.Now()
	model.ageAlert(moment)
	model.ageAlert(moment.Add(alertLinger + time.Second))
	if model.heldBack == "" {
		t.Fatalf("the timeout did not hold the failure back")
	}

	updated, _ = model.Update(dashboardMsg{})
	model = updated.(Model)
	updated, _ = model.Update(down())
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("a failure that returned after a recovery stayed held back")
	}
}

// An unreadable shelf is not an empty one. Saying "nothing here yet" when
// the read failed is the screen telling the reader something untrue.
func TestHistoryDoesNotClaimAnEmptyShelfWhenItCouldNotRead(t *testing.T) {
	model := alertModel(t)
	model.mode = modeHistory
	model.historyLoading = true

	updated, _ := model.Update(historyMsg{err: errors.New("state directory is unreadable")})
	model = updated.(Model)

	view := ansi.Strip(model.renderHistoryModal(model.bodyDimensions()))
	if strings.Contains(view, "No past sessions recorded yet") {
		t.Fatalf("the modal claimed an empty shelf after a failed read:\n%s", view)
	}
	if !strings.Contains(view, "state directory is unreadable") {
		t.Fatalf("the modal does not say why it is empty:\n%s", view)
	}
}

// The transcript's foot row always says something — the reply affordances,
// the amber band, the search readout — and the card must stay off it.
func TestCardStaysOffTheTranscriptsFootRow(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = false
	if got := model.inputStripRows(); got < 1 {
		t.Fatalf("the transcript's foot row was not reserved: %d", got)
	}

	model.search.query = "needle"
	if got := model.inputStripRows(); got < 1 {
		t.Fatalf("the search readout was not reserved: %d", got)
	}

	model.ptyEnabled = true
	model.mode = modeNormal
	if got := model.inputStripRows(); got != 0 {
		t.Fatalf("the portal reserved %d rows; it has no foot row", got)
	}
}

// Reading a failure that has already recovered must not arm a hush against
// its next occurrence — that would be the dashboard going silent about a
// daemon that is down.
func TestRecallingARecoveredFailureDoesNotSilenceItsReturn(t *testing.T) {
	model := alertModel(t)
	down := func() dashboardMsg {
		return dashboardMsg{err: errors.New("daemon is not listening")}
	}

	updated, _ := model.Update(down())
	model = updated.(Model)
	updated, _ = model.Update(dashboardMsg{})
	model = updated.(Model)
	if model.alert.active() {
		t.Fatalf("the card outlived the recovery")
	}

	// Minutes later the reader re-reads the message that recall exists for.
	updated, _ = model.Update(runeKey("e"))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)

	updated, _ = model.Update(down())
	if next := updated.(Model); !next.alert.active() {
		t.Fatalf("the daemon went down again and the dashboard said nothing")
	}
}

// Sessions is a list the reader moves through, filters and resumes from.
// A card over it covers rows they need, and Esc there closes the modal
// rather than the card.
func TestTheCardMakesRoomForSurfacesTheReaderWorksIn(t *testing.T) {
	for _, surface := range []mode{modeHistory, modeAlert} {
		model := alertModel(t)
		model.width = 120
		model.height = 24
		model.mode = surface
		model.raise(errors.New(sshFailure))

		width, height := model.bodyDimensions()
		if got := model.modalRegion(width, height); got >= height {
			t.Fatalf("mode %v gave the card no room: region %d of %d",
				surface, got, height)
		}
		modalTop, modalBottom := frameSpan(model.renderModeBody(width, height), 1)
		cardTop, _ := frameSpan(model.renderBody(), 0)
		if modalTop < 0 || cardTop < 0 {
			t.Fatalf("mode %v: expected a modal and a card (%d..%d, %d)",
				surface, modalTop, modalBottom, cardTop)
		}
		if cardTop <= modalBottom {
			t.Fatalf("mode %v: the card lands on it: modal %d..%d, card from %d",
				surface, modalTop, modalBottom, cardTop)
		}
	}
}

// The composer closing itself is still the composer closing, and its
// objection goes with it.
func TestAnAutoClosedComposerTakesItsComplaintWithIt(t *testing.T) {
	model := alertModel(t)
	model.ptyEnabled = false
	model.agents = []agent.Agent{{ID: "a1", Name: "one", ProcessLive: true}}
	model.rebuildGroups("", "a1")
	model.activePane = paneInteraction
	model.mode = modeCompose
	model.complain(errors.New("message cannot be empty"))

	// The agent puts up a prompt, which closes the composer from under it.
	updated, _ := model.Update(dashboardMsg{agents: []agent.Agent{{
		ID: "a1", Name: "one", ProcessLive: true,
		Attention: agent.AttentionApproval,
	}}})
	model = updated.(Model)
	if model.mode == modeCompose {
		t.Skip("this model did not auto-close the composer")
	}
	if model.alert.active() {
		t.Fatalf("the composer's objection outlived the composer: %v", model.alert.err)
	}
}
