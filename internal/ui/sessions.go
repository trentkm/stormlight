package ui

// The session history browser: past provider conversations from the
// on-disk log, each one a session the provider can reopen. See #94.
//
// History is a modal picker rather than rows in the Agents pane because a
// past session answers to almost none of an agent row's verbs — there is
// nothing to interrupt, mark, or write to, only something to read about
// and resume — and rows that refuse most of the pane's keys would be
// special cases in every handler. The picker's verbs are the ones that
// exist: Enter resumes, / filters, Esc closes.

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
	m.clearComplaint(modeHistory)
	m.historyCursor = 0
	m.historyRecords = nil
	m.historyLoading = true
	m.historyFilter = newLineInput("type to filter")
	m.historyFilter.Focus()
	m.historyFiltering = false
	return m, historyCmd(m.backend)
}

func (m Model) updateHistory(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.historyFiltering {
		return m.updateHistoryFilter(msg)
	}
	visible := m.visibleHistory()
	last := max(0, len(visible)-1)
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[", "q", "H":
		if m.historyFilterActive() {
			// A set filter narrows what the list shows; leaving it behind
			// on close would make the next H open onto a mystery subset.
			m.historyFilter.SetValue("")
			m.historyCursor = 0
			return m, nil
		}
		m.mode = modeNormal
		return m, nil
	case "/":
		m.historyFiltering = true
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
		return m.resumeSelectedHistory()
	}
	return m, nil
}

// updateHistoryFilter owns the keys while the filter line is being typed:
// characters narrow the list live, arrows move within the narrowed list,
// Enter resumes the highlighted match the way pathnav's Enter picks one,
// and Esc abandons the filter.
func (m Model) updateHistoryFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	visible := m.visibleHistory()
	last := max(0, len(visible)-1)
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.historyFiltering = false
		m.historyFilter.SetValue("")
		m.historyCursor = 0
		return m, nil
	case "enter":
		return m.resumeSelectedHistory()
	case "down", "ctrl+n":
		m.historyCursor = clamp(m.historyCursor+1, 0, last)
		return m, nil
	case "up", "ctrl+p":
		m.historyCursor = clamp(m.historyCursor-1, 0, last)
		return m, nil
	}
	before := m.historyFilter.Value()
	m.historyFilter = m.historyFilter.Update(msg)
	if m.historyFilter.Value() != before {
		m.historyCursor = 0
	}
	return m, nil
}

func (m Model) resumeSelectedHistory() (tea.Model, tea.Cmd) {
	visible := m.visibleHistory()
	if len(visible) == 0 {
		return m, nil
	}
	record := visible[min(m.historyCursor, len(visible)-1)]
	m.mode = modeNormal
	backend := m.backend
	return m, tea.Batch(
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(
				context.Background(), resumeTimeout)
			defer cancel()
			_, err := backend.Resume(ctx, record)
			return actionMsg{err: err}
		},
		m.refreshCmd(),
	)
}

func (m Model) historyFilterActive() bool {
	return strings.TrimSpace(m.historyFilter.Value()) != ""
}

// visibleHistory is the record list the cursor and the rows agree on: all
// of it, or the subset matching every filter term. Terms match anywhere a
// human might remember a session by — name, task, summary, provider,
// workspace, or the id itself.
func (m Model) visibleHistory() []history.Record {
	terms := strings.Fields(strings.ToLower(m.historyFilter.Value()))
	if len(terms) == 0 {
		return m.historyRecords
	}
	var matches []history.Record
	for _, record := range m.historyRecords {
		haystack := strings.ToLower(strings.Join([]string{
			record.Name,
			record.Task,
			record.Summary,
			string(record.Provider),
			record.Workspace.Name,
			record.Workspace.ComponentName,
			record.SessionID,
		}, "\x1f"))
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, record)
		}
	}
	return matches
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
	// Title, blank, the filter line, a page of rows plus the overflow
	// line, blank, hints — plus the border.
	modalWidth, modalHeight := modalDimensions(
		width,
		height,
		96,
		historyPageSize+8,
	)
	innerWidth := max(1, modalWidth-2)
	contentWidth := max(1, innerWidth-4)

	lines := []string{
		"  " + titleStyle().Render("Sessions · past"),
		"",
	}
	if m.historyFiltering || m.historyFilterActive() {
		m.historyFilter.SetWidth(max(1, contentWidth-2))
		lines = append(lines, "  > "+m.historyFilter.View())
	}
	visible := m.visibleHistory()
	switch {
	case m.historyLoading:
		lines = append(lines, "  "+mutedStyle().Render("Loading…"))
	case m.historyFailed != nil:
		// Not an empty shelf — an unreadable one. Saying "nothing here
		// yet" would be the screen telling the reader something untrue.
		lines = append(lines,
			"  "+errorStyle().Render("Could not read past sessions."))
		for _, line := range wrapMessage(
			strings.TrimSpace(m.historyFailed.Error()), max(1, contentWidth-2),
		) {
			lines = append(lines, "  "+mutedStyle().Render(line))
		}
	case len(m.historyRecords) == 0:
		lines = append(lines,
			"  "+mutedStyle().Render("No past sessions recorded yet."),
			"  "+mutedStyle().Render(
				"Finished conversations land here once their window is deleted."),
		)
	case len(visible) == 0:
		lines = append(lines, "  "+mutedStyle().Render("No matching sessions."))
	default:
		lines = append(lines, m.renderHistoryRows(visible, contentWidth)...)
	}
	hints := "enter resume · / filter · j/k move · esc close"
	if m.historyFiltering {
		hints = "enter resume · ↑/↓ move · esc clear filter"
	}
	lines = append(lines, "", "  "+mutedStyle().Render(hints))
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) renderHistoryRows(visible []history.Record, width int) []string {
	cursor := min(m.historyCursor, len(visible)-1)
	first := clamp(
		cursor-historyPageSize/2,
		0,
		max(0, len(visible)-historyPageSize),
	)
	last := min(len(visible), first+historyPageSize)
	rows := make([]string, 0, historyPageSize+1)
	for index := first; index < last; index++ {
		rows = append(rows, m.renderHistoryRow(visible[index], index == cursor, width))
	}
	if remaining := len(visible) - last; remaining > 0 {
		rows = append(rows,
			"   "+mutedStyle().Render(fmt.Sprintf("… %d more", remaining)))
	}
	return rows
}

func (m Model) renderHistoryRow(
	record history.Record,
	selected bool,
	width int,
) string {
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
		// The machine is part of where it would land, and the browser
		// now lists several machines' conversations in one column.
		if record.Workspace.Host != "" {
			name = record.Workspace.Host + ":" + name
		}
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
	if selected {
		return "  " + renderSelectableRow(body+suffix, width, true)
	}
	// One leading space matches the selectable row's marker column, so
	// moving the cursor never shifts the text sideways.
	return "  " + lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render(" "+body+mutedStyle().Render(suffix))
}
