package ui

// The Workspaces pane and workspace add/submit flows.
// Split from model.go; see #34.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/workspace"
)

func (m Model) updateAddWorkspace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.formFocus == dispatchDirectory {
		switch {
		case m.dispatchPrefix == "g" && key == "g":
			m.dispatchPrefix = ""
			m.selectDirectoryIndex(0)
			return m, nil
		case key == "g":
			m.dispatchPrefix = "g"
			return m, nil
		}
	}
	m.dispatchPrefix = ""

	if m.formFocus == dispatchCustomPath &&
		key != "esc" && key != "ctrl+c" && key != "ctrl+[" {
		if confirmed := m.handlePathNavKey(msg); confirmed {
			return m.submitAddWorkspace(m.pathNav.chosen())
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.blurForm()
		m.status = "Ready"
		return m, nil
	case "j", "down":
		if m.formFocus == dispatchDirectory {
			m.selectDirectory(1)
			return m, nil
		}
	case "k", "up":
		if m.formFocus == dispatchDirectory {
			m.selectDirectory(-1)
			return m, nil
		}
	case "G", "end":
		if m.formFocus == dispatchDirectory {
			m.selectDirectoryIndex(len(m.directories) - 1)
			return m, nil
		}
	case "home":
		if m.formFocus == dispatchDirectory {
			m.selectDirectoryIndex(0)
			return m, nil
		}
	case "e":
		if m.formFocus == dispatchDirectory {
			m.editSelectedDirectory()
			return m, nil
		}
	case "enter":
		selected, ok := m.selectedDirectory()
		if !ok {
			return m, nil
		}
		switch selected.kind {
		case directoryYazi:
			return m.openYazi()
		case directoryCustom:
			m.formFocus = dispatchCustomPath
			m.startPathNav()
			m.focusForm()
			return m, nil
		default:
			return m.submitAddWorkspace(selected.path)
		}
	}
	return m, nil
}

func (m Model) renderWorkspaces(width, height int) string {
	if len(m.groups) == 0 {
		return mutedStyle.Render(" No workspaces")
	}

	expanded := m.expandedRows()
	capacity := listRowCapacity(height, expanded)
	start, end := visibleRange(len(m.groups), m.workspaceCursor, capacity)
	deleting := m.mode == modeDelete && m.activePane == paneWorkspaces
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, m.renderWorkspaceRow(
			m.groups[index],
			index == m.workspaceCursor,
			index == m.workspaceCursor && m.activePane == paneWorkspaces,
			width,
			deleting && index == m.workspaceCursor,
		))
	}
	separator := "\n"
	if expanded {
		separator = "\n\n"
	}
	return strings.Join(rows, separator)
}

func (m Model) renderWorkspaceRow(
	group workspaceGroup,
	selected bool,
	focused bool,
	width int,
	danger bool,
) string {
	active, urgent, waiting := workspaceStats(group.agents)
	contentWidth := max(1, width-1)
	// What the name actually asks for, floored so a long one still yields
	// ground to the chips rather than shouldering them off the row.
	nameNeed := min(
		lipgloss.Width(group.context.Name),
		min(10, max(1, contentWidth/2)),
	)
	chips := fitCountChips(
		workspaceCountChips(active, urgent, waiting, len(group.agents)),
		max(1, contentWidth-lipgloss.Width("  ")-1),
		nameNeed,
	)
	suffix := chipsPlain(chips)
	// Nothing marks this column. A selected-but-unfocused workspace is
	// already named by the row that stays at full strength while its
	// neighbours dim, and by the connector arc pointing out of it — an arrow
	// saying it a third time is texture, not information. The two columns
	// remain so quiet rows line up with the focused row's marker.
	gutter := "  "
	nameWidth := max(
		1,
		contentWidth-lipgloss.Width(gutter)-lipgloss.Width(suffix)-1,
	)
	name := truncate(group.context.Name, nameWidth)
	gap := max(
		1,
		contentWidth-
			lipgloss.Width(gutter)-
			lipgloss.Width(name)-
			lipgloss.Width(suffix),
	)
	// Subtitle indents to sit under the name, reading as detail of the
	// title rather than a second row of equal weight. One space here: the
	// selected row's marker column supplies the other, and the quiet path
	// adds its own, so the subtitle never shifts with selection.
	bottomContent := " " + workspaceDetail(group.context, max(1, contentWidth-2))
	tier := attentionTierOf(urgent, waiting)
	if focused || danger {
		return renderSelectedWorkspaceRow(
			" ",
			name,
			gap,
			suffix,
			bottomContent,
			width,
			focused,
			m.expandedRows(),
			active > 0,
			tier,
			m.shimmerPhaseOrRest(),
			rowThemeFor(danger),
		)
	}

	renderedName := titleStyle.Render(name)
	switch {
	case tier == tierUrgent:
		// Urgent attention outranks the working glow: the name goes loud
		// amber to match the chip already shouting on the right.
		renderedName = lipgloss.NewStyle().
			Foreground(colorWaiting).
			Bold(true).
			Render(name)
	case active > 0:
		renderedName = shimmerText(name, m.shimmerPhaseOrRest(), nil)
	}
	top := gutter +
		renderedName +
		strings.Repeat(" ", gap) +
		chipsStyled(chips)
	bottom := mutedStyle.Render(" " + bottomContent)
	if !m.expandedRows() {
		return top
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

// A countChip is one tier of a workspace's population, told in the glyph the
// agent rows use for it. Words lived in this column once — "1 input",
// "2 agents" — and a twenty-column pane cannot afford both a sentence and a
// name, so every workspace read as "stormlig… 1 input". A glyph and a numeral
// say the same thing in two columns and speak the vocabulary the header and
// the rows already established.
type countChip struct {
	glyph string
	count int
	style lipgloss.Style
}

// workspaceCountChips orders the tiers loudest first. A workspace with
// nothing pending reports its population instead, in the muted dot the agent
// rows use for a state worth no alarm — including the honest "· 0".
func workspaceCountChips(active, urgent, waiting, total int) []countChip {
	chips := make([]countChip, 0, 3)
	if urgent > 0 {
		chips = append(chips, countChip{
			"!", urgent,
			lipgloss.NewStyle().Foreground(colorWaiting).Bold(true),
		})
	}
	if waiting > 0 {
		chips = append(chips, countChip{
			"○", waiting, lipgloss.NewStyle().Foreground(colorWaiting),
		})
	}
	if active > 0 {
		chips = append(chips, countChip{
			"●", active, lipgloss.NewStyle().Foreground(colorWorking),
		})
	}
	if len(chips) == 0 {
		chips = append(chips, countChip{"·", total, mutedStyle})
	}
	return chips
}

// fitCountChips drops the quietest tiers until the cluster and the name both
// fit in available. The name asks for what it needs rather than a fixed
// reservation — a nine-letter workspace should not hold a column open for a
// twenty-letter one, which is what used to leave a three-tier row showing a
// single chip beside four columns of nothing. The first chip always survives:
// a narrow pane still owes you the loudest thing happening in there.
func fitCountChips(chips []countChip, available, nameNeed int) []countChip {
	for len(chips) > 1 &&
		available-lipgloss.Width(chipsPlain(chips)) < nameNeed {
		chips = chips[:len(chips)-1]
	}
	return chips
}

// A chip breathes: a space between the glyph and its numeral, and a wider
// one between chips, so a cluster reads as pairs rather than a run of
// characters. Same rhythm as the header counters.
const chipGap = "  "

func chipText(chip countChip) string {
	return chip.glyph + " " + strconv.Itoa(chip.count)
}

func chipsPlain(chips []countChip) string {
	parts := make([]string, 0, len(chips))
	for _, chip := range chips {
		parts = append(parts, chipText(chip))
	}
	return strings.Join(parts, chipGap)
}

func chipsStyled(chips []countChip) string {
	parts := make([]string, 0, len(chips))
	for _, chip := range chips {
		parts = append(parts, chip.style.Render(chipText(chip)))
	}
	return strings.Join(parts, chipGap)
}

func renderSelectedWorkspaceRow(
	activityMarker string,
	name string,
	gap int,
	suffix string,
	bottom string,
	width int,
	focused bool,
	expanded bool,
	active bool,
	tier attentionTier,
	shimmerPhase int,
	theme rowTheme,
) string {
	top := activityMarker + name + strings.Repeat(" ", gap) + suffix
	if width < 3 || lipgloss.Width(top) > max(0, width-2) {
		if focused {
			if !expanded {
				return theme.selectableRow(top, width, true)
			}
			return theme.focusedRow(top, bottom, width)
		}
		if !expanded {
			return theme.selectableRow(top, width, false)
		}
		return theme.contextRow(top, bottom, width)
	}

	marker := "▏"
	markerColor := theme.restMark
	if focused {
		marker = "▌"
		markerColor = theme.focusMark
	}
	markerStyle := lipgloss.NewStyle().
		Foreground(markerColor).
		Background(theme.background).
		Bold(focused)
	baseStyle := lipgloss.NewStyle().
		Foreground(theme.text).
		Background(theme.background)
	activityStyle := baseStyle.Copy()
	renderedName := baseStyle.Copy().Bold(true).Render(name)
	switch {
	case tier == tierUrgent:
		activityStyle = activityStyle.
			Foreground(colorWaiting).
			Bold(true)
		renderedName = activityStyle.Render(name)
	case tier == tierWaiting:
		activityStyle = activityStyle.Foreground(colorWaiting)
	case active:
		activityStyle = activityStyle.
			Foreground(colorWorking).
			Bold(true)
		renderedName = shimmerText(name, shimmerPhase, theme.background)
	}

	contentWidth := width - 1
	tailWidth := max(
		0,
		contentWidth-lipgloss.Width(activityMarker)-lipgloss.Width(name),
	)
	topLine := markerStyle.Render(marker) +
		activityStyle.Render(activityMarker) +
		renderedName +
		baseStyle.Copy().
			Width(tailWidth).
			MaxWidth(tailWidth).
			Render(strings.Repeat(" ", gap)+suffix)
	if !expanded {
		return topLine
	}
	bottomLine := markerStyle.Render(marker) +
		baseStyle.Copy().
			Width(contentWidth).
			MaxWidth(contentWidth).
			Render(ansi.Truncate(bottom, contentWidth, ""))
	return lipgloss.JoinVertical(lipgloss.Left, topLine, bottomLine)
}

// workspaceDetail is the expanded row's subtitle: quiet middot-joined
// tokens — resolver kind, home-relative root, and the component when it
// adds information — indented under the name rather than justified across
// the row.
func workspaceDetail(value workspace.Context, width int) string {
	width = max(1, width)
	kind := strings.ToLower(strings.TrimSpace(value.Kind))
	path := shortPath(strings.TrimSpace(value.Root))
	// The path's tail usually duplicates the workspace name; only the
	// parent carries information. Renamed or oddly-rooted workspaces keep
	// the full path.
	if path != "" && filepath.Base(path) == value.Name {
		path = filepath.Dir(path)
	}
	join := func(pathToken string) string {
		parts := []string{}
		if kind != "" {
			parts = append(parts, kind)
		}
		if pathToken != "" {
			parts = append(parts, pathToken)
		}
		if tail := value.Tail(); tail != "" {
			parts = append(parts, tail)
		}
		return strings.Join(parts, " · ")
	}
	detail := join(path)
	if lipgloss.Width(detail) > width && path != "" {
		// The path yields first: fish-style abbreviation, then the tail.
		path = abbreviatePath(path)
		detail = join(path)
	}
	if lipgloss.Width(detail) > width && path != "" {
		overhead := lipgloss.Width(detail) - lipgloss.Width(path)
		detail = join(truncatePathTail(path, max(1, width-overhead)))
	}
	return ansi.Truncate(detail, width, "…")
}

// abbreviatePath shortens every segment but the last to its first rune,
// fish-prompt style: /Volumes/repos/alpha-service → /V/r/alpha-service.
func abbreviatePath(path string) string {
	segments := strings.Split(path, "/")
	for index := 0; index < len(segments)-1; index++ {
		if runes := []rune(segments[index]); len(runes) > 1 && segments[index] != "~" {
			segments[index] = string(runes[:1])
		}
	}
	return strings.Join(segments, "/")
}

func (m Model) beginAddWorkspace() (tea.Model, tea.Cmd) {
	directory := m.initialCwd
	if selected, ok := m.selectedWorkspace(); ok && selected.Root != "" {
		directory = selected.Root
	}
	m.prepareAddWorkspaceChoices(directory)
	m.mode = modeAddWorkspace
	m.formFocus = dispatchDirectory
	m.dispatchPrefix = ""
	m.focusForm()
	m.err = nil
	m.status = "Add workspace"
	return m, nil
}

func (m Model) submitAddWorkspace(path string) (tea.Model, tea.Cmd) {
	path = strings.TrimSpace(path)
	if !isDirectory(path) {
		m.err = fmt.Errorf("workspace directory is unavailable: %s", path)
		return m, nil
	}
	m.blurForm()
	m.status = "Adding workspace"
	return m, addWorkspaceCmd(m.backend, path)
}
