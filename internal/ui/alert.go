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
// foot of the body, wrapped rather than cut, and it waits there until it is
// dismissed, superseded, or answered — the hint row keeps its own line.
type alert struct {
	err error
	// expires bounds the card's life in the two places the dashboard
	// cannot take an Esc on its behalf: inside the portal, where the
	// keyboard belongs to the agent, and under a floating program. Zero
	// means the card waits for a human, which is the usual case.
	expires time.Time
}

// alertDetail is the whole message, opened over the card with `e`. It is a
// snapshot rather than a view onto the live alert: reading is what dismisses
// the card, so by the time this is on screen the alert itself is gone.
type alertDetail struct {
	text   string
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
	if err == nil {
		m.alert = alert{}
		return
	}
	if m.alert.active() && m.alert.message() == strings.TrimSpace(err.Error()) {
		return
	}
	m.alert = alert{err: err}
}

func (m *Model) dismissAlert() { m.alert = alert{} }

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
	if !m.alert.active() {
		return
	}
	if !m.keyboardHeldElsewhere() {
		m.alert.expires = time.Time{}
		return
	}
	if m.alert.expires.IsZero() {
		m.alert.expires = now.Add(alertLinger)
		return
	}
	if now.After(m.alert.expires) {
		m.dismissAlert()
	}
}

// alertDetailReachable reports whether `e` opens the full text from here.
// It needs the dashboard's own keyboard and a mode that is not spending
// letters on an input.
func (m Model) alertDetailReachable() bool {
	return m.alert.active() && m.mode == modeNormal && !m.keyboardHeldElsewhere()
}

// openAlertDetail hands the whole message to the reader and clears the card:
// reading it is what an alert was waiting for.
func (m Model) openAlertDetail() (tea.Model, tea.Cmd) {
	if !m.alert.active() {
		return m, nil
	}
	m.alertDetail = alertDetail{text: m.alert.message()}
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
	if !m.alert.active() || width < 16 || height < 3 {
		return ""
	}
	cardWidth := clamp(width-4, 16, alertCardWidth)
	textWidth := max(4, cardWidth-6)
	budget := clamp(height-3, 1, alertCardLines)

	lines := wrapMessage(m.alert.message(), textWidth)
	clipped := len(lines) > budget
	if clipped {
		lines = lines[:budget]
	}

	body := make([]string, 0, len(lines)+1)
	for index, line := range lines {
		marker := "  "
		if index == 0 {
			marker = errorStyle().Bold(true).Render("! ")
		}
		body = append(body, " "+marker+line)
	}
	if keys := m.alertCardKeys(clipped); keys != "" {
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
	if m.keyboardHeldElsewhere() {
		return ""
	}
	keys := []string{}
	if m.alertDetailReachable() {
		label := "e detail"
		if clipped {
			label = "e read it all"
		}
		keys = append(keys, label)
	}
	keys = append(keys, "Esc dismiss")
	return mutedStyle().Render(strings.Join(keys, "  ·  ")) + " "
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
	lines := wrapMessage(m.alertDetail.text, min(width, alertCardWidth)-6)
	return modalDimensions(width, height, alertCardWidth, len(lines)+6)
}

// alertDetailWindow is the text area inside the modal: how wide a line may
// be and how many of them are on screen at once.
func (m Model) alertDetailWindow() (int, int) {
	width, height := m.alertDetailDimensions(m.bodyDimensions())
	return max(4, width-6), max(1, height-6)
}

func (m Model) renderAlertModal(width, height int) string {
	modalWidth, modalHeight := m.alertDetailDimensions(width, height)
	textWidth := max(4, modalWidth-6)
	window := max(1, modalHeight-6)

	lines := wrapMessage(m.alertDetail.text, textWidth)
	offset := clamp(m.alertDetail.offset, 0, max(0, len(lines)-window))
	visible := lines[offset:min(len(lines), offset+window)]

	out := []string{"  " + errorStyle().Bold(true).Render("Error"), ""}
	for _, line := range visible {
		out = append(out, "  "+line)
	}
	for len(out) < window+2 {
		out = append(out, "")
	}
	keys := "y copy  ·  Esc close"
	if len(lines) > window {
		keys = "j/k scroll  ·  " + keys
	}
	out = append(out, "", "  "+mutedStyle().Render(keys))
	return renderModal(strings.Join(out, "\n"), modalWidth, modalHeight)
}
