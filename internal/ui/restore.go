package ui

// The restore picker: agents remembered from before the tmux server died,
// and the choice of which to bring back. See issue #91.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/resurrect"
)

// restoreCandidatesCmd reads what the snapshot remembers. It runs on its own
// rather than riding the dashboard poll: the answer changes only when agents
// start or stop, and the file is on disk rather than in tmux.
func restoreCandidatesCmd(backend Backend, offer bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		candidates, err := backend.RestoreCandidates(ctx)
		if err != nil {
			diagnostic.Logger().Warn("restore candidates unavailable", "error", err)
		}
		return restoreCandidatesMsg{candidates: candidates, offer: offer, err: err}
	}
}

func restoreCmd(backend Backend, ids []string) tea.Cmd {
	return func() tea.Msg {
		// Restoring spawns a window and a provider process per agent, so the
		// budget is per-agent rather than the flat ten seconds an action gets.
		timeout := time.Duration(len(ids)) * 15 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		results, err := backend.Restore(ctx, ids...)
		if err != nil {
			return actionMsg{status: "Restore failed", err: err}
		}
		restored, failed := 0, 0
		for _, result := range results {
			if result.Err != nil {
				failed++
				diagnostic.Logger().Error("restore failed",
					"agent_id", result.Entry.ID,
					"error", result.Err,
				)
				continue
			}
			restored++
		}
		if failed > 0 {
			return actionMsg{
				status: fmt.Sprintf("Restored %d, failed %d", restored, failed),
				err:    fmt.Errorf("%d agent(s) could not be restored", failed),
			}
		}
		return actionMsg{status: fmt.Sprintf("Restored %d agent(s)", restored)}
	}
}

func forgetCmd(backend Backend, ids []string) tea.Cmd {
	return actionCmd("Forgotten", func(ctx context.Context) error {
		return backend.Forget(ctx, ids...)
	})
}

// beginRestore opens the picker. Everything that can come back starts
// chosen: the human who reaches for this screen has just lost their whole
// working set, and the common answer is "all of it".
func (m Model) beginRestore() (tea.Model, tea.Cmd) {
	m.mode = modeRestore
	m.restoreCursor = 0
	m.restoreChosen = map[string]bool{}
	m.err = nil
	m.status = "Restore agents"
	return m, restoreCandidatesCmd(m.backend, false)
}

// applyRestoreCandidates lands a fresh reading of the snapshot. `offer` marks
// the unprompted case — the dashboard starting to an empty roster — which
// opens the picker only if there is in fact something to bring back.
func (m *Model) applyRestoreCandidates(msg restoreCandidatesMsg) {
	if msg.err != nil && m.mode == modeRestore {
		// An empty picker and an unreadable record look identical on screen,
		// and only one of them is the user's fault.
		m.err = msg.err
	}
	m.restoreCandidates = msg.candidates
	m.restoreCursor = clamp(m.restoreCursor, 0, max(0, len(msg.candidates)-1))
	if m.restoreChosen == nil {
		m.restoreChosen = map[string]bool{}
	}
	for _, candidate := range msg.candidates {
		if _, decided := m.restoreChosen[candidate.Entry.ID]; !decided {
			m.restoreChosen[candidate.Entry.ID] = candidate.Restorable()
		}
	}
	if !msg.offer {
		return
	}
	if len(resurrect.Restorable(msg.candidates)) == 0 {
		return
	}
	m.mode = modeRestore
	m.restoreCursor = 0
	m.status = "Agents remembered from a previous session"
}

func (m Model) updateRestore(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[", "q":
		m.mode = modeNormal
		m.status = "Ready"
		return m, nil
	case "j", "down":
		m.restoreCursor = clamp(
			m.restoreCursor+1, 0, max(0, len(m.restoreCandidates)-1))
		return m, nil
	case "k", "up":
		m.restoreCursor = clamp(
			m.restoreCursor-1, 0, max(0, len(m.restoreCandidates)-1))
		return m, nil
	case "space":
		candidate, ok := m.restoreCandidateAt(m.restoreCursor)
		if !ok || !candidate.Restorable() {
			return m, nil
		}
		m.restoreChosen[candidate.Entry.ID] = !m.restoreChosen[candidate.Entry.ID]
		return m, nil
	case "a":
		// Toggle the whole list on the state of the first thing that could
		// go either way, so the key reads as "select all" until everything
		// is selected and "clear" after.
		target := !m.allRestorableChosen()
		for _, candidate := range m.restoreCandidates {
			if candidate.Restorable() {
				m.restoreChosen[candidate.Entry.ID] = target
			}
		}
		return m, nil
	case "x", "ctrl+x":
		candidate, ok := m.restoreCandidateAt(m.restoreCursor)
		if !ok || candidate.Live {
			return m, nil
		}
		id := candidate.Entry.ID
		m.status = "Forgetting " + candidate.Entry.Name
		return m, tea.Batch(
			forgetCmd(m.backend, []string{id}),
			restoreCandidatesCmd(m.backend, false),
		)
	case "enter":
		ids := m.chosenRestoreIDs()
		if len(ids) == 0 {
			m.mode = modeNormal
			m.status = "Ready"
			return m, nil
		}
		m.mode = modeNormal
		m.status = fmt.Sprintf("Restoring %d agent(s)", len(ids))
		return m, restoreCmd(m.backend, ids)
	}
	return m, nil
}

func (m Model) restoreCandidateAt(index int) (resurrect.Candidate, bool) {
	if index < 0 || index >= len(m.restoreCandidates) {
		return resurrect.Candidate{}, false
	}
	return m.restoreCandidates[index], true
}

func (m Model) chosenRestoreIDs() []string {
	var ids []string
	for _, candidate := range m.restoreCandidates {
		if candidate.Restorable() && m.restoreChosen[candidate.Entry.ID] {
			ids = append(ids, candidate.Entry.ID)
		}
	}
	return ids
}

func (m Model) allRestorableChosen() bool {
	any := false
	for _, candidate := range m.restoreCandidates {
		if !candidate.Restorable() {
			continue
		}
		any = true
		if !m.restoreChosen[candidate.Entry.ID] {
			return false
		}
	}
	return any
}

func (m Model) renderRestoreModal(width, height int) string {
	// Title, blank, the rows, blank, hints — plus the border.
	modalWidth, modalHeight := modalDimensions(
		width,
		height,
		78,
		len(m.restoreCandidates)+7,
	)
	innerWidth := max(1, modalWidth-2)
	contentWidth := max(1, innerWidth-4)

	lines := []string{
		"  " + titleStyle().Render("Restore agents"),
		"  " + mutedStyle().Render(
			truncate("Reopens each conversation at its composer; "+
				"it does not resume the agent's work.", contentWidth)),
		"",
	}
	if len(m.restoreCandidates) == 0 {
		lines = append(lines,
			"  "+mutedStyle().Render("Nothing remembered."),
			"",
			"  "+mutedStyle().Render("Esc close"),
		)
		return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
	}

	nameWidth := max(8, min(24, contentWidth/3))
	for index, candidate := range m.restoreCandidates {
		box := "[ ]"
		switch {
		case !candidate.Restorable():
			box = "   "
		case m.restoreChosen[candidate.Entry.ID]:
			box = "[x]"
		}
		name := lipgloss.NewStyle().
			Width(nameWidth).
			Render(truncate(candidate.Entry.Name, nameWidth))
		detailWidth := max(1, contentWidth-nameWidth-len(box)-4)
		detail := truncate(m.restoreDetail(candidate), detailWidth)
		row := box + " " + name + "  " + detail
		if index == m.restoreCursor {
			lines = append(lines,
				"  "+renderSelectableRow(row, contentWidth, true))
			continue
		}
		styled := mutedStyle()
		if candidate.Restorable() {
			styled = titleStyle()
		}
		lines = append(lines, "  "+lipgloss.NewStyle().
			Width(contentWidth).
			MaxWidth(contentWidth).
			Render(" "+accentStyle().Render(box)+" "+
				styled.Render(name)+"  "+
				mutedStyle().Render(detail)))
	}
	lines = append(lines,
		"",
		"  "+mutedStyle().Render(
			"Space choose  a all  Enter restore  x forget  Esc close"),
	)
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

// restoreDetail says what will happen to a row, or why nothing will.
func (m Model) restoreDetail(candidate resurrect.Candidate) string {
	switch {
	case candidate.Live:
		return "already running"
	case candidate.Reason != "":
		return candidate.Reason
	}
	summary := strings.TrimSpace(candidate.Entry.Summary)
	if summary == "" {
		summary = strings.TrimSpace(candidate.Entry.Task)
	}
	if workspaceName := candidate.Entry.Workspace.Name; workspaceName != "" {
		return workspaceName + " · " + summary
	}
	return summary
}
