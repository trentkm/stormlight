package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
