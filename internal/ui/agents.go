package ui

// The Agents pane rows and agent presentation helpers.
// Split from model.go; see #34.

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
)

func (m Model) renderAgents(width, height int) string {
	agents := m.agentsForSelectedWorkspace()
	if len(agents) == 0 {
		return mutedStyle.Render(" No agents")
	}
	expanded := m.expandedRows()
	capacity := listRowCapacity(height, expanded)
	start, end := visibleRange(len(agents), m.agentCursor, capacity)
	deleting := m.mode == modeDelete && m.activePane != paneWorkspaces
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, renderAgentRowWithDensity(
			agents[index],
			index == m.agentCursor,
			index == m.agentCursor && m.activePane == paneAgents,
			width,
			expanded,
			deleting && index == m.agentCursor,
			m.shimmerPhaseOrRest(),
		))
	}
	separator := "\n"
	if expanded {
		separator = "\n\n"
	}
	return strings.Join(rows, separator)
}

func renderAgentRow(
	managedAgent agent.Agent,
	selected bool,
	focused bool,
	width int,
) string {
	return renderAgentRowWithDensity(
		managedAgent,
		selected,
		focused,
		width,
		true,
		false,
		-1,
	)
}

func renderAgentRowWithDensity(
	managedAgent agent.Agent,
	selected bool,
	focused bool,
	width int,
	expanded bool,
	danger bool,
	shimmerPhase int,
) string {
	symbol, statusStyle := statusVisual(managedAgent)
	providerName := strings.ToLower(string(managedAgent.Provider))
	if len(providerName) > 6 {
		providerName = providerName[:6]
	}
	age := timeAgo(managedAgent.CreatedAt)
	contentWidth := max(1, width-1)
	ageWidth := lipgloss.Width(age)
	titleWidth := max(1, contentWidth-2-ageWidth-1)
	displayTitle := truncate(agentDisplayTitle(managedAgent), titleWidth)
	gap := max(1, contentWidth-2-lipgloss.Width(displayTitle)-ageWidth)
	// One indicator column: the focused row's bar comes from the theme, a
	// remembered selection shows the chevron, everything else shows its
	// status glyph. Displaced state speaks through the title styling.
	topContent := " " + displayTitle + strings.Repeat(" ", gap) + age

	state := string(managedAgent.Activity)
	if managedAgent.NeedsAttention() {
		state = "needs " + string(managedAgent.Attention)
	}
	details := []string{providerName, state}
	if badge := modeBadge(managedAgent.Mode); badge != "" {
		details = append(details, badge)
	}
	if location := agentLocation(managedAgent); location != "" {
		details = append(details, location)
	}
	bottomContent := " " + truncate(
		strings.Join(details, " · "),
		max(1, contentWidth-2),
	)
	// Only the active pane's cursor row gets the filled background; a
	// selection remembered in an inactive pane keeps just a faint marker,
	// so exactly one row on screen is hot.
	if focused || danger {
		theme := rowThemeFor(danger)
		if !expanded {
			return theme.selectableRow(topContent, width, focused)
		}
		if focused {
			return theme.focusedRow(topContent, bottomContent, width)
		}
		return theme.contextRow(topContent, bottomContent, width)
	}

	renderedTitle := titleStyle.Render(displayTitle)
	detailStyle := mutedStyle
	switch {
	case managedAgent.ProcessLive && managedAgent.Attention.Urgent():
		// Urgent attention outranks the working glow: the row goes loud
		// amber. The waiting tier keeps its calm row and speaks through
		// the amber status symbol alone.
		attentionStyle := lipgloss.NewStyle().Foreground(colorWaiting).Bold(true)
		renderedTitle = attentionStyle.Render(displayTitle)
		detailStyle = attentionStyle
	case managedAgent.Activity == agent.ActivityWorking,
		managedAgent.Activity == agent.ActivityStarting:
		renderedTitle = shimmerText(displayTitle, shimmerPhase, nil)
	}
	indicator := statusStyle.Render(symbol)
	if selected {
		indicator = lipgloss.NewStyle().Foreground(colorBorder).Render("›")
	}
	top := indicator + " " +
		renderedTitle +
		strings.Repeat(" ", gap) +
		detailStyle.Render(age)
	bottom := detailStyle.Render(" " + bottomContent)
	if !expanded {
		return top
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

func listRowCapacity(height int, expanded bool) int {
	if !expanded {
		return max(1, height)
	}
	return max(1, (height+1)/3)
}

func renderFocusedRow(top, bottom string, width int) string {
	return selectTheme.focusedRow(top, bottom, width)
}

func (t rowTheme) focusedRow(top, bottom string, width int) string {
	if width < 3 {
		style := lipgloss.NewStyle().
			Foreground(t.text).
			Background(t.background).
			Width(max(1, width))
		return lipgloss.JoinVertical(
			lipgloss.Left,
			style.Copy().Bold(true).Render(ansi.Truncate("▌"+top, width, "")),
			style.Render(ansi.Truncate("▌"+bottom, width, "")),
		)
	}

	contentWidth := width - 1
	markerStyle := lipgloss.NewStyle().
		Foreground(t.focusMark).
		Background(t.background).
		Bold(true)
	topStyle := lipgloss.NewStyle().
		Foreground(t.text).
		Background(t.background).
		Bold(true).
		Width(contentWidth).
		MaxWidth(contentWidth)
	bottomStyle := lipgloss.NewStyle().
		Foreground(t.text).
		Background(t.background).
		Width(contentWidth).
		MaxWidth(contentWidth)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		markerStyle.Render("▌")+topStyle.Render(top),
		markerStyle.Render("▌")+bottomStyle.Render(bottom),
	)
}

func renderContextRow(top, bottom string, width int) string {
	return selectTheme.contextRow(top, bottom, width)
}

func (t rowTheme) contextRow(top, bottom string, width int) string {
	if width < 3 {
		style := lipgloss.NewStyle().
			Foreground(t.text).
			Background(t.background).
			Width(max(1, width))
		return lipgloss.JoinVertical(
			lipgloss.Left,
			style.Copy().Bold(true).Render(ansi.Truncate(top, width, "")),
			style.Render(ansi.Truncate(bottom, width, "")),
		)
	}

	contentWidth := width - 1
	markerStyle := lipgloss.NewStyle().
		Foreground(t.restMark).
		Background(t.background)
	topStyle := lipgloss.NewStyle().
		Foreground(t.text).
		Background(t.background).
		Bold(true).
		Width(contentWidth).
		MaxWidth(contentWidth)
	bottomStyle := lipgloss.NewStyle().
		Foreground(t.text).
		Background(t.background).
		Width(contentWidth).
		MaxWidth(contentWidth)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		markerStyle.Render("▏")+topStyle.Render(top),
		markerStyle.Render("▏")+bottomStyle.Render(bottom),
	)
}

func agentDisplayTitle(managedAgent agent.Agent) string {
	name := strings.TrimSpace(managedAgent.Name)
	task := strings.TrimSpace(managedAgent.Task)
	prefix := map[agent.Provider]string{
		agent.ProviderClaude: "cl-",
		agent.ProviderCodex:  "cx-",
	}[managedAgent.Provider]

	if prefix != "" && strings.HasPrefix(strings.ToLower(name), prefix) {
		if task != "" {
			return task
		}
		friendly := strings.ReplaceAll(name[len(prefix):], "-", " ")
		if friendly = strings.TrimSpace(friendly); friendly != "" {
			return friendly
		}
	}
	if name != "" {
		return name
	}
	if task != "" {
		return task
	}
	if managedAgent.Provider != "" {
		return strings.ToUpper(string(managedAgent.Provider)) + " agent"
	}
	return "Agent"
}

// attentionTier is the visual triage level of a row: urgent is loud amber,
// waiting is a soft amber accent, none defers to activity styling.
type attentionTier int

const (
	tierNone attentionTier = iota
	tierWaiting
	tierUrgent
)

func attentionTierOf(urgent, waiting int) attentionTier {
	switch {
	case urgent > 0:
		return tierUrgent
	case waiting > 0:
		return tierWaiting
	default:
		return tierNone
	}
}

func workspaceStats(agents []agent.Agent) (active, urgent, waiting int) {
	for _, managedAgent := range agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			active++
		}
		if !managedAgent.ProcessLive {
			// A dead pane can't be waiting on anyone; its exit status is
			// the story.
			continue
		}
		switch {
		case managedAgent.Attention.Urgent():
			urgent++
		case managedAgent.Attention == agent.AttentionWaiting:
			waiting++
		}
	}
	return active, urgent, waiting
}

func agentLocation(managedAgent agent.Agent) string {
	value := effectiveWorkspace(managedAgent)
	if value.ComponentName != "" {
		return value.ComponentName
	}
	if value.ExecutionRoot != "" {
		return filepath.Base(value.ExecutionRoot)
	}
	if managedAgent.Cwd != "" {
		return filepath.Base(managedAgent.Cwd)
	}
	return ""
}

func statusVisual(managedAgent agent.Agent) (string, lipgloss.Style) {
	if managedAgent.ProcessLive && managedAgent.Attention.Urgent() {
		return "!", lipgloss.NewStyle().Foreground(colorWaiting).Bold(true)
	}
	if managedAgent.ProcessLive && managedAgent.Attention == agent.AttentionWaiting {
		return "○", lipgloss.NewStyle().Foreground(colorWaiting)
	}
	switch managedAgent.Activity {
	case agent.ActivityStarting, agent.ActivityWorking:
		return "●", lipgloss.NewStyle().Foreground(colorWorking)
	case agent.ActivityIdle:
		return "○", mutedStyle
	case agent.ActivityCompleted:
		return "✓", lipgloss.NewStyle().Foreground(colorDone)
	case agent.ActivityFailed:
		return "×", lipgloss.NewStyle().Foreground(colorFailed).Bold(true)
	case agent.ActivityStopped:
		return "■", mutedStyle
	default:
		return "·", mutedStyle
	}
}
