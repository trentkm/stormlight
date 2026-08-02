package ui

// Pending provider actions: approval/question navigation and rendering.
// Split from model.go; see #34.

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/pending"
)

func (m Model) updatePendingAction(
	key string,
	action pending.Action,
) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "j", "down":
		m.pendingOption = clamp(
			m.pendingOption+1,
			0,
			max(0, len(action.Options)-1),
		)
		return m, nil, true
	case "k", "up":
		m.pendingOption = clamp(
			m.pendingOption-1,
			0,
			max(0, len(action.Options)-1),
		)
		return m, nil, true
	case "home":
		m.pendingOption = 0
		return m, nil, true
	case "G", "end":
		m.pendingOption = max(0, len(action.Options)-1)
		return m, nil, true
	case "y":
		return m.resolvePendingOption(action, pending.OptionAllowOnce)
	case "a":
		for _, option := range action.Options {
			if strings.HasPrefix(option.ID, pending.OptionAlwaysPrefix) {
				return m.resolvePendingOption(action, option.ID)
			}
		}
		return m, nil, true
	case "n":
		return m.resolvePendingOption(action, pending.OptionDeny)
	case "t":
		return m.resolvePendingOption(action, pending.OptionTerminal)
	case "enter":
		if m.pendingOption >= 0 && m.pendingOption < len(action.Options) {
			return m.resolvePendingOption(
				action,
				action.Options[m.pendingOption].ID,
			)
		}
		return m, nil, true
	default:
		for _, option := range action.Options {
			if option.Shortcut != "" && option.Shortcut == key {
				return m.resolvePendingOption(action, option.ID)
			}
		}
		return m, nil, false
	}
}

func (m Model) resolvePendingOption(
	action pending.Action,
	optionID string,
) (tea.Model, tea.Cmd, bool) {
	option, ok := pendingOptionByID(action, optionID)
	if !ok {
		return m, nil, true
	}
	managedAgent, ok := m.selectedAgent()
	if !ok {
		return m, nil, true
	}
	m.status = option.Label
	return m, resolvePendingActionCmd(
		m.backend,
		action,
		option,
		managedAgent,
	), true
}

func renderPendingAction(
	action pending.Action,
	selectedOption int,
	width int,
	height int,
) string {
	width = max(1, width)
	height = max(1, height)
	if len(action.Options) == 0 {
		return titleStyle.Render(truncate(action.Title, width))
	}
	selectedOption = clamp(selectedOption, 0, len(action.Options)-1)

	kind := strings.ToUpper(string(action.Kind))
	if kind == "" {
		kind = "ACTION"
	}
	contextLabel := strings.TrimSpace(action.ToolName)
	if contextLabel == "" {
		contextLabel = strings.ToUpper(string(action.Provider))
	}
	left := lipgloss.NewStyle().
		Foreground(colorWaiting).
		Bold(true).
		Render(kind)
	rightWidth := max(0, width-lipgloss.Width(left)-2)
	right := mutedStyle.Render(truncate(contextLabel, rightWidth))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	headerLines := []string{
		ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, ""),
		titleStyle.Render(truncate(action.Title, width)),
	}

	optionBudget := min(len(action.Options), max(1, height-2))
	headerBudget := min(len(headerLines), max(0, height-optionBudget))
	lines := make([]string, 0, height)
	if headerBudget == 1 {
		lines = append(lines, headerLines[1])
	} else if headerBudget == 2 {
		lines = append(lines, headerLines...)
	}

	remaining := max(0, height-headerBudget-optionBudget)
	bodyText := strings.TrimSpace(action.Description)
	if detail := strings.TrimSpace(action.Detail); detail != "" {
		if bodyText != "" {
			bodyText += "\n\n"
		}
		bodyText += detail
	}
	bodyLines := wrapActionText(bodyText, width)
	bodyBudget := max(0, remaining-1)
	if len(bodyLines) > bodyBudget {
		bodyLines = bodyLines[:bodyBudget]
		if bodyBudget > 0 {
			bodyLines[bodyBudget-1] = truncate(
				strings.TrimSuffix(bodyLines[bodyBudget-1], "…")+"…",
				width,
			)
		}
	}
	for _, line := range bodyLines {
		lines = append(lines, mutedStyle.Render(line))
	}
	if remaining > 0 {
		lines = append(lines, "")
	}

	optionStart, optionEnd := visibleRange(
		len(action.Options),
		selectedOption,
		optionBudget,
	)
	for index := optionStart; index < optionEnd; index++ {
		option := action.Options[index]
		lines = append(lines, renderPendingOption(
			option,
			index == selectedOption,
			width,
		))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func renderPendingOption(
	option pending.Option,
	selected bool,
	width int,
) string {
	shortcut := ""
	if option.Shortcut != "" {
		shortcut = "[" + option.Shortcut + "] "
	}
	content := truncate(shortcut+option.Label, max(1, width-2))
	if selected {
		marker := lipgloss.NewStyle().
			Foreground(colorWaiting).
			Background(colorSelect).
			Bold(true).
			Render("▌ ")
		row := lipgloss.NewStyle().
			Foreground(colorSelectedText).
			Background(colorSelect).
			Bold(true).
			Width(max(1, width-2)).
			MaxWidth(max(1, width-2)).
			Render(content)
		return marker + row
	}
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render("  " + mutedStyle.Render(content))
}

func wrapActionText(value string, width int) []string {
	if strings.TrimSpace(value) == "" || width <= 0 {
		return nil
	}
	wrapped := ansi.Wrap(value, width, "")
	lines := strings.Split(wrapped, "\n")
	for index := range lines {
		lines[index] = truncate(lines[index], width)
	}
	return lines
}

func (m Model) selectedPendingAction() (pending.Action, bool) {
	agentID := m.selectedAgentID()
	if agentID == "" {
		return pending.Action{}, false
	}
	for _, action := range m.pendingActions {
		if action.AgentID == agentID {
			return action, true
		}
	}
	return pending.Action{}, false
}

func (m Model) selectedPendingActionID() string {
	action, ok := m.selectedPendingAction()
	if !ok {
		return ""
	}
	return action.ID
}

func (m *Model) clampPendingOption() {
	action, ok := m.selectedPendingAction()
	if !ok {
		m.pendingOption = 0
		return
	}
	m.pendingOption = clamp(
		m.pendingOption,
		0,
		max(0, len(action.Options)-1),
	)
}

func (m *Model) removePendingAction(actionID string) {
	m.pendingActions = slices.DeleteFunc(
		m.pendingActions,
		func(action pending.Action) bool {
			return action.ID == actionID
		},
	)
}

func pendingOptionByID(
	action pending.Action,
	optionID string,
) (pending.Option, bool) {
	for _, option := range action.Options {
		if option.ID == optionID {
			return option, true
		}
	}
	return pending.Option{}, false
}
