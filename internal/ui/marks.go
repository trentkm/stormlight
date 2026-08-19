package ui

// The manual mark picker: the human's own reading of an agent, overriding
// what Stormlight inferred. See issue #67.

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/agent"
)

// markChoice is one row of the picker: the mark it sets, the key that picks
// it directly, and how it reads.
type markChoice struct {
	mark   agent.Mark
	key    string
	label  string
	detail string
}

// markChoices are ordered as the picker draws them. Clearing sits last
// because it is the exit, not a state.
var markChoices = []markChoice{
	{
		mark:   agent.MarkWorking,
		key:    "w",
		label:  "In progress",
		detail: "still going; nothing pending",
	},
	{
		mark:   agent.MarkAttention,
		key:    "a",
		label:  "Needs attention",
		detail: "come back to this one",
	},
	{
		mark:   agent.MarkNone,
		key:    "c",
		label:  "Clear mark",
		detail: "back to Stormlight's own reading",
	},
}

func markChoiceIndex(mark agent.Mark) int {
	for index, choice := range markChoices {
		if choice.mark == mark {
			return index
		}
	}
	return 0
}

// beginMark opens the picker on the selected agent. Marking is per-agent
// wherever the cursor is, like interrupting: a workspace has no state of its
// own to correct.
func (m Model) beginMark() (tea.Model, tea.Cmd) {
	selected, ok := m.selectedAgent()
	if !ok {
		return m, nil
	}
	m.markAgentID = selected.ID
	m.markIndex = markChoiceIndex(selected.EffectiveMark())
	m.mode = modeMark
	return m, nil
}

func (m Model) updateMark(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		return m, nil
	case "j", "down":
		m.markIndex = clamp(m.markIndex+1, 0, len(markChoices)-1)
		return m, nil
	case "k", "up":
		m.markIndex = clamp(m.markIndex-1, 0, len(markChoices)-1)
		return m, nil
	case "enter":
		return m.submitMark(markChoices[m.markIndex].mark)
	}
	for _, choice := range markChoices {
		if key == choice.key {
			return m.submitMark(choice.mark)
		}
	}
	return m, nil
}

func (m Model) submitMark(mark agent.Mark) (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	id := m.markAgentID
	if id == "" {
		return m, nil
	}
	// Apply locally first so the row reports the human's reading on the very
	// next frame; the backend write follows.
	m.applyMark(id, mark)
	label := "Marked in progress"
	switch mark {
	case agent.MarkAttention:
		label = "Marked needs attention"
	case agent.MarkNone:
		label = "Mark cleared"
	}
	backend := m.backend
	return m, actionCmd(label, func(ctx context.Context) error {
		return backend.SetMark(ctx, id, mark)
	})
}

// applyMark writes the mark into the loaded agents so the dashboard reflects
// it before the next refresh lands.
func (m *Model) applyMark(id string, mark agent.Mark) {
	for index := range m.agents {
		if m.agents[index].ID == id {
			m.agents[index].Mark = mark
		}
	}
	for group := range m.groups {
		for index := range m.groups[group].agents {
			if m.groups[group].agents[index].ID == id {
				m.groups[group].agents[index].Mark = mark
			}
		}
	}
}

func (m Model) renderMarkModal(width, height int) string {
	// Title, blank, the choices, blank, hints — plus the border.
	modalWidth, modalHeight := modalDimensions(
		width,
		height,
		64,
		len(markChoices)+6,
	)
	innerWidth := max(1, modalWidth-2)
	contentWidth := max(1, innerWidth-4)

	title := "Mark agent"
	if selected, ok := m.selectedAgent(); ok && selected.ID == m.markAgentID {
		title = "Mark · " + truncate(agentDisplayTitle(selected), contentWidth-8)
	}
	lines := []string{
		"  " + titleStyle().Render(title),
		"",
	}
	for index, choice := range markChoices {
		labelWidth := max(1, min(18, contentWidth-4))
		label := lipgloss.NewStyle().
			Width(labelWidth).
			Render(truncate(choice.label, labelWidth))
		detail := truncate(choice.detail, max(1, contentWidth-labelWidth-6))
		if index == m.markIndex {
			lines = append(lines, "  "+renderSelectableRow(
				choice.key+"  "+label+"  "+detail,
				contentWidth,
				true,
			))
			continue
		}
		// One leading space matches the selectable row's marker column, so
		// moving the cursor never shifts the text sideways.
		lines = append(lines, "  "+lipgloss.NewStyle().
			Width(contentWidth).
			MaxWidth(contentWidth).
			Render(" "+accentStyle().Render(choice.key)+"  "+
				titleStyle().Render(label)+"  "+
				mutedStyle().Render(detail)))
	}
	lines = append(lines,
		"",
		"  "+mutedStyle().Render("j/k choose  Enter apply  Esc cancel"),
	)
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}
