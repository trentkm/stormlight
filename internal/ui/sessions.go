package ui

// The session history browser: past provider conversations from the
// on-disk log, each one a session the provider can reopen. See #94.
//
// History is a modal picker rather than rows in the Agents pane because a
// past session answers to almost none of an agent row's verbs — there is
// nothing to interrupt, mark, or write to, only something to read about
// and resume — and rows that refuse most of the pane's keys would be
// special cases in every handler. The picker's two verbs are the two that
// exist: Enter resumes, Esc closes.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/history"
)

func (m Model) beginHistory() (tea.Model, tea.Cmd) {
	m.mode = modeHistory
	m.err = nil
	m.status = "Session history"
	m.historyCursor = 0
	m.historyRecords = nil
	m.historyLoading = true
	return m, historyCmd(m.backend)
}

func (m Model) updateHistory(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	last := max(0, len(m.historyRecords)-1)
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[", "q", "H":
		m.mode = modeNormal
		m.status = "Ready"
		return m, nil
	case "j", "down":
		m.historyCursor = clamp(m.historyCursor+1, 0, last)
		return m, nil
	case "k", "up":
		m.historyCursor = clamp(m.historyCursor-1, 0, last)
		return m, nil
	case "g", "home":
		m.historyCursor = 0
		return m, nil
	case "G", "end":
		m.historyCursor = last
		return m, nil
	case "ctrl+d", "pgdown":
		m.historyCursor = clamp(m.historyCursor+historyPageSize, 0, last)
		return m, nil
	case "ctrl+u", "pgup":
		m.historyCursor = clamp(m.historyCursor-historyPageSize, 0, last)
		return m, nil
	case "enter":
		if len(m.historyRecords) == 0 {
			return m, nil
		}
		record := m.historyRecords[m.historyCursor]
		m.mode = modeNormal
		m.status = "Resuming " + historyTitle(record)
		backend := m.backend
		return m, tea.Batch(
			func() tea.Msg {
				ctx, cancel := context.WithTimeout(
					context.Background(), resumeTimeout)
				defer cancel()
				managedAgent, err := backend.Resume(ctx, record)
				if err != nil {
					return actionMsg{err: err}
				}
				return actionMsg{
					status: "Resumed " + agentDisplayTitle(managedAgent),
				}
			},
			m.refreshCmd(),
		)
	}
	return m, nil
}

// historyPageSize is the ctrl+d/u jump; the modal shows about this many
// rows, so a page reads as one screenful.
const historyPageSize = 10

const resumeTimeout = 15 * time.Second

func historyCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		records, err := backend.SessionHistory(ctx)
		return historyMsg{records: records, err: err}
	}
}

func historyTitle(record history.Record) string {
	if strings.TrimSpace(record.Name) != "" {
		return strings.TrimSpace(record.Name)
	}
	summary := record.DisplaySummary()
	if summary != "" {
		return summary
	}
	return record.SessionID
}

func (m Model) renderHistoryModal(width, height int) string {
	// Title, blank, a page of rows plus the overflow line, blank, hints —
	// plus the border.
	modalWidth, modalHeight := modalDimensions(
		width,
		height,
		96,
		historyPageSize+7,
	)
	innerWidth := max(1, modalWidth-2)
	contentWidth := max(1, innerWidth-4)

	lines := []string{
		"  " + titleStyle().Render("Sessions · past"),
		"",
	}
	switch {
	case m.historyLoading:
		lines = append(lines, "  "+mutedStyle().Render("Loading…"))
	case len(m.historyRecords) == 0:
		lines = append(lines,
			"  "+mutedStyle().Render("No past sessions recorded yet."),
			"  "+mutedStyle().Render(
				"Finished conversations land here once their window is deleted."),
		)
	default:
		lines = append(lines, m.renderHistoryRows(contentWidth)...)
	}
	lines = append(lines, "",
		"  "+mutedStyle().Render("enter resume · j/k move · esc close"))
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) renderHistoryRows(width int) []string {
	first := clamp(
		m.historyCursor-historyPageSize/2,
		0,
		max(0, len(m.historyRecords)-historyPageSize),
	)
	last := min(len(m.historyRecords), first+historyPageSize)
	rows := make([]string, 0, historyPageSize+1)
	for index := first; index < last; index++ {
		rows = append(rows, m.renderHistoryRow(index, width))
	}
	if remaining := len(m.historyRecords) - last; remaining > 0 {
		rows = append(rows,
			"   "+mutedStyle().Render(fmt.Sprintf("… %d more", remaining)))
	}
	return rows
}

func (m Model) renderHistoryRow(index int, width int) string {
	record := m.historyRecords[index]
	title := historyTitle(record)
	detail := record.DisplaySummary()
	if detail == title {
		detail = ""
	}
	// The trailing tags answer the questions a resume decision needs: what
	// would reopen, where it would land, how stale it is, and whether the
	// provider still has a transcript to continue from.
	tags := []string{string(record.Provider), timeAgo(record.UpdatedAt)}
	if name := record.Workspace.Name; name != "" {
		tags = append(tags, name)
	}
	if !record.HasTranscript() {
		tags = append(tags, "transcript gone")
	}
	suffix := " · " + strings.Join(tags, " · ")

	body := truncate(title, max(8, width/3))
	if detail != "" {
		body += "  " + truncate(
			detail,
			max(1, width-lipgloss.Width(body)-lipgloss.Width(suffix)-4),
		)
	}
	if index == m.historyCursor {
		return "  " + renderSelectableRow(body+suffix, width, true)
	}
	// One leading space matches the selectable row's marker column, so
	// moving the cursor never shifts the text sideways.
	return "  " + lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render(" "+body+mutedStyle().Render(suffix))
}
