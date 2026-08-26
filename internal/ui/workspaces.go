package ui

// The Workspaces pane and workspace add/submit flows.
// Split from model.go; see #34.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func (m Model) updateAddWorkspace(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

	// Naming a machine the SSH configuration does not: the row's own
	// input, which takes everything but the keys that leave it.
	if m.showingMachines() && m.formFocus == dispatchCustomPath &&
		key != "esc" && key != "ctrl+c" && key != "ctrl+[" {
		if key == "enter" {
			destination := strings.TrimSpace(m.hostInput.Value())
			if destination == "" {
				return m, nil
			}
			m.enterMachine(destination)
			return m, nil
		}
		m.hostInput = m.hostInput.Update(msg)
		return m, nil
	}

	if m.formFocus == dispatchCustomPath &&
		key != "esc" && key != "ctrl+c" && key != "ctrl+[" {
		if confirmed := m.handlePathNavKey(msg); confirmed {
			return m.submitAddWorkspace(m.pathNav.chosen())
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c", "ctrl+[":
		// Inside a machine, Esc steps back out to the list rather than
		// abandoning the whole modal.
		if m.addWorkspaceTab == tabRemote && m.addWorkspaceHost != "" {
			m.leaveMachine()
			return m, nil
		}
		if m.showingMachines() && m.formFocus == dispatchCustomPath {
			m.hostInput.Blur()
			m.formFocus = dispatchDirectory
			return m, nil
		}
		m.mode = modeNormal
		m.blurForm()
		return m, nil
	case "tab", "shift+tab", "h", "left", "l", "right":
		if m.addWorkspaceTab == tabLocal {
			m.switchAddWorkspaceTab(tabRemote)
		} else {
			m.switchAddWorkspaceTab(tabLocal)
		}
		return m, nil
	case "j", "down":
		if m.showingMachines() {
			m.selectMachine(1)
			return m, nil
		}
		if m.formFocus == dispatchDirectory {
			m.selectDirectory(1)
			return m, nil
		}
	case "k", "up":
		if m.showingMachines() {
			m.selectMachine(-1)
			return m, nil
		}
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
		if m.showingMachines() {
			m.openMachine()
			if m.machineState.running {
				// The spinner rides the same tick the working glow does,
				// which only runs while something is moving.
				return m, tea.Batch(
					m.checkMachineCmd(m.machineState.host), m.startShimmer())
			}
			return m, nil
		}
		selected, ok := m.selectedDirectory()
		if !ok {
			return m, nil
		}
		switch selected.kind {
		case directorySetup:
			return m.openSetup(selected.host)
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
	// The catalog holds directories on machines this one has to ask, and
	// a machine that has not answered contributes no rows. Without a word
	// about it the pane is a short list that grows on its own a second
	// later; with one it is a list that says what is still coming.
	note := m.reachingBar(width)
	if len(m.groups) == 0 {
		if note != "" {
			return note
		}
		return mutedStyle().Render(" No workspaces")
	}
	if note != "" {
		// The note costs a row, taken from the list rather than from the
		// pane, so nothing below it moves.
		height = max(1, height-1)
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
	list := strings.Join(rows, separator)
	if note != "" {
		list += "\n" + note
	}
	return list
}

func (m Model) renderWorkspaceRow(
	group workspaceGroup,
	selected bool,
	focused bool,
	width int,
	danger bool,
) string {
	stats := agent.Count(group.agents)
	contentWidth := max(1, width-1)
	// What the name actually asks for, floored so a long one still yields
	// ground to the chips rather than shouldering them off the row.
	displayName := group.label
	if displayName == "" {
		displayName = group.context.Name
	}
	nameNeed := min(
		lipgloss.Width(displayName),
		min(10, max(1, contentWidth/2)),
	)
	// A workspace on another machine is marked in the columns themselves,
	// not only in the subtitle: compact rows have no subtitle, and where a
	// workspace is is the one thing about it not worth guessing at. Two
	// marks, because there are two questions. The cloud leads the row and
	// answers the one asked at a glance — this one is somewhere else. The
	// initial rides with the counts and answers the one asked next —
	// which machine. Neither rides with the name, so a remote workspace
	// is not also a truncated one.
	lead, mark := "", ""
	if group.context.Host != "" {
		lead = remoteGlyph + " "
		mark = remoteMark(group.context.Host) + " "
	}
	// Nothing marks this column. A selected-but-unfocused workspace is
	// already named by the row that stays at full strength while its
	// neighbours dim, and by the connector arc pointing out of it — an arrow
	// saying it a third time is texture, not information. The two columns
	// remain so quiet rows line up with the focused row's marker.
	gutter := "  "
	// Both marks are spent before the chips are fitted. A remote row has
	// four fewer columns to spend than a local one, and chips fitted
	// against the local width would take them out of the name — which is
	// the one part of the row the marks are placed to protect.
	chips := fitCountChips(
		workspaceCountChips(stats, len(group.agents)),
		max(1, contentWidth-
			lipgloss.Width(gutter)-
			lipgloss.Width(lead)-
			lipgloss.Width(mark)-1),
		nameNeed,
	)
	// The cloud is the first thing a pane too narrow for everything gives
	// up. The initial says what the cloud says and names the machine
	// besides, so the row that can afford only one mark keeps the one that
	// says more — and a row assembled from its floors (one column of name,
	// one of gap, the loudest chip) is exactly as wide as its pane rather
	// than two columns past it, which is a wrapped row and a list that
	// loses its footing below it.
	floorWidth := lipgloss.Width(gutter) + lipgloss.Width(lead) +
		lipgloss.Width(mark) + lipgloss.Width(chipsPlain(chips)) + 2
	if floorWidth > width {
		lead = ""
	}
	suffix := mark + chipsPlain(chips)
	nameWidth := max(
		1,
		contentWidth-
			lipgloss.Width(gutter)-
			lipgloss.Width(lead)-
			lipgloss.Width(suffix)-1,
	)
	name := truncate(displayName, nameWidth)
	gap := max(
		1,
		contentWidth-
			lipgloss.Width(gutter)-
			lipgloss.Width(lead)-
			lipgloss.Width(name)-
			lipgloss.Width(suffix),
	)
	// Subtitle indents to sit under the name, reading as detail of the
	// title rather than a second row of equal weight. One space here: the
	// selected row's marker column supplies the other, and the quiet path
	// adds its own, so the subtitle never shifts with selection.
	bottomContent := " " + workspaceDetail(group.context, max(1, contentWidth-2))
	tier := attentionTierOf(stats)
	if focused || danger {
		return renderSelectedWorkspaceRow(
			" "+lead,
			name,
			gap,
			suffix,
			bottomContent,
			width,
			focused,
			m.expandedRows(),
			stats.Working > 0,
			tier,
			m.shimmerPhaseOrRest(),
			rowThemeFor(danger),
		)
	}

	renderedName := titleStyle().Render(name)
	switch {
	case tier == tierUrgent:
		// Urgent attention outranks the working glow: the name goes loud
		// amber to match the chip already shouting on the right.
		renderedName = lipgloss.NewStyle().
			Foreground(colorWaiting()).
			Bold(true).
			Render(name)
	case stats.Working > 0:
		renderedName = shimmerText(name, m.shimmerPhaseOrRest(), nil)
	}
	styledSuffix := chipsStyled(chips)
	styledLead := ""
	if mark != "" {
		styledSuffix = mutedStyle().Render(mark) + styledSuffix
		styledLead = mutedStyle().Render(lead)
	}
	top := gutter +
		styledLead +
		renderedName +
		strings.Repeat(" ", gap) +
		styledSuffix
	bottom := mutedStyle().Render(" " + bottomContent)
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
func workspaceCountChips(stats agent.Stats, total int) []countChip {
	chips := make([]countChip, 0, 3)
	if stats.Urgent > 0 {
		chips = append(chips, countChip{
			"!", stats.Urgent,
			lipgloss.NewStyle().Foreground(colorWaiting()).Bold(true),
		})
	}
	if stats.Waiting > 0 {
		chips = append(chips, countChip{
			"○", stats.Waiting, lipgloss.NewStyle().Foreground(colorWaiting()),
		})
	}
	if stats.Working > 0 {
		chips = append(chips, countChip{
			"●", stats.Working, lipgloss.NewStyle().Foreground(colorWorking()),
		})
	}
	if len(chips) == 0 {
		chips = append(chips, countChip{"·", total, mutedStyle()})
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
	lead string,
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
	top := lead + name + strings.Repeat(" ", gap) + suffix
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
	renderedName := baseStyle.Copy().Bold(true).Render(name)
	switch {
	case tier == tierUrgent:
		// Urgent attention outranks the working glow, the same way it
		// does on the quiet path.
		renderedName = baseStyle.Copy().
			Foreground(colorWaiting()).
			Bold(true).
			Render(name)
	case tier == tierWaiting:
		// Deliberately nothing. A workspace someone is waiting in keeps a
		// still name even while other agents work in it, so the amber chip
		// is the only thing moving in the row. The case earns its place by
		// holding the shimmer below off, not by painting anything.
	case active:
		renderedName = shimmerText(name, shimmerPhase, theme.background)
	}

	contentWidth := width - 1
	tailWidth := max(
		0,
		contentWidth-lipgloss.Width(lead)-lipgloss.Width(name),
	)
	topLine := markerStyle.Render(marker) +
		baseStyle.Render(lead) +
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

// remoteGlyph leads the row of a workspace that lives on another machine:
// Nerd Font U+F059D, nf-md-weather_windy. It says "somewhere else" and
// nothing more, which is exactly what the first glance asks — the row is
// read left to right, and where a workspace is outranks how many agents
// are in it.
//
// It is a Private Use Area codepoint, so it needs a patched font and there
// is no way to ask a terminal whether it has one. Without a Nerd Font the
// column is a box. That is the price of the mark, and it is why remoteMark
// below stays a plain letter: the two marks fail differently, and the one
// that names the machine should not fail at all.
//
// One cell wide, here and in the emulator both — PUA is East Asian
// Ambiguous, so a terminal configured to draw ambiguous glyphs wide will
// spend two on it and take a column off the name. The same is already
// true of the arcs and diamonds this dashboard is built from.
//
// From the Material Design set for the same reason StormGlyph is: the
// gusts it keeps are drawn to fill the cell, where the Weather Icons
// cloud this started as inked barely half of what a capital M does. See
// StormGlyph for the measurements.
const remoteGlyph = "\U000f059d"

// remoteMark is how a workspace on another machine says which machine:
// that machine's initial, in the column before the counts.
//
// The cloud above answers "somewhere else" at a glance; a dashboard worth
// having several machines on is one where "which" is the next question,
// and an initial answers it in the same two columns. It reads as a mark
// rather than a word when there is only one host to tell apart.
//
// It is a plain letter, not a superscript: the superscript capitals exist
// for most of the alphabet and not all of it, and a host beginning with Q
// or Z should not render as a box. Two machines sharing an initial share
// a mark — the subtitle carries the name in full, which is where an
// ambiguity is worth resolving rather than in a single column.
func remoteMark(host string) string {
	for _, letter := range host {
		return strings.ToUpper(string(letter))
	}
	return ""
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
		// The machine comes first: it is the most significant thing about
		// a workspace that is not on this one, and two checkouts at the
		// same path on different machines are otherwise the same row
		// twice.
		if value.Host != "" {
			parts = append(parts, value.Host)
		}
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
	m.clearComplaint(modeAddWorkspace)
	return m, nil
}

func (m Model) submitAddWorkspace(path string) (tea.Model, tea.Cmd) {
	path = strings.TrimSpace(path)
	host := m.addWorkspaceHostName()
	// Only this machine's directories can be checked here; that host
	// resolves its own, and says so if the path is not there.
	if host == "" && !isDirectory(path) {
		m.complain(fmt.Errorf("workspace directory is unavailable: %s", path))
		return m, nil
	}
	if host != "" && !filepath.IsAbs(path) {
		m.complain(fmt.Errorf("a path on %s must be absolute: %s", host, path))
		return m, nil
	}
	m.blurForm()
	return m, addWorkspaceCmd(m.backend, host, path)
}
