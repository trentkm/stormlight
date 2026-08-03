package ui

// The new-agent and add-workspace forms: focus, choices, submission.
// Split from model.go; see #34.

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/workspace"
)

func (m Model) updateDispatch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		key != "esc" && key != "ctrl+c" && key != "ctrl+[" &&
		key != "ctrl+s" && key != "shift+tab" {
		if confirmed := m.handlePathNavKey(msg); confirmed {
			m.cwdInput.SetValue(m.pathNav.chosen())
			m.pickerStart = m.pathNav.chosen()
			m.formFocus = dispatchTask
			m.focusForm()
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.blurForm()
		m.status = "Ready"
		return m, nil
	case "tab":
		m.moveDispatchFocus(1)
		return m, nil
	case "shift+tab":
		m.moveDispatchFocus(-1)
		return m, nil
	case "ctrl+s":
		return m.submitDispatch()
	case "ctrl+o":
		if m.formFocus == dispatchTask {
			return m.openTaskEditor()
		}
	case "h", "left":
		if m.formFocus == dispatchProvider && len(m.providers) > 0 {
			m.providerIndex = (m.providerIndex + len(m.providers) - 1) % len(m.providers)
			return m, nil
		}
	case "l", "right":
		if m.formFocus == dispatchProvider && len(m.providers) > 0 {
			m.providerIndex = (m.providerIndex + 1) % len(m.providers)
			return m, nil
		}
	case "j", "down":
		switch m.formFocus {
		case dispatchProvider:
			if len(m.providers) > 0 {
				m.providerIndex = (m.providerIndex + 1) % len(m.providers)
			}
			return m, nil
		case dispatchDirectory:
			m.selectDirectory(1)
			return m, nil
		}
	case "k", "up":
		switch m.formFocus {
		case dispatchProvider:
			if len(m.providers) > 0 {
				m.providerIndex = (m.providerIndex + len(m.providers) - 1) % len(m.providers)
			}
			return m, nil
		case dispatchDirectory:
			m.selectDirectory(-1)
			return m, nil
		}
	case "G", "end":
		if m.formFocus == dispatchDirectory && len(m.directories) > 0 {
			m.selectDirectoryIndex(len(m.directories) - 1)
			return m, nil
		}
	case "home":
		if m.formFocus == dispatchDirectory {
			m.selectDirectoryIndex(0)
			return m, nil
		}
	case "e":
		switch m.formFocus {
		case dispatchProvider:
			return m.openTaskEditor()
		case dispatchDirectory:
			m.editSelectedDirectory()
			return m, nil
		}
	case "m":
		if m.formFocus == dispatchProvider || m.formFocus == dispatchDirectory {
			m.dispatchMode = nextDispatchMode(m.dispatchMode)
			return m, nil
		}
	case "enter":
		switch m.formFocus {
		case dispatchProvider:
			if m.chooseDispatchDirectory {
				m.formFocus = dispatchDirectory
			} else {
				m.formFocus = dispatchTask
			}
			m.focusForm()
			return m, nil
		case dispatchDirectory:
			selected, ok := m.selectedDirectory()
			if !ok {
				m.formFocus = dispatchTask
				m.focusForm()
				return m, nil
			}
			switch selected.kind {
			case directoryYazi:
				return m.openYazi()
			case directoryCustom:
				m.formFocus = dispatchCustomPath
				m.startPathNav()
			default:
				m.formFocus = dispatchTask
			}
			m.focusForm()
			return m, nil
		case dispatchName:
			m.formFocus = dispatchTask
			m.focusForm()
			return m, nil
		case dispatchTask:
			return m.submitDispatch()
		}
	}

	switch m.formFocus {
	case dispatchName:
		m.nameInput = m.nameInput.Update(msg)
		return m, nil
	case dispatchTask:
		var cmd tea.Cmd
		m.taskInput, cmd = m.taskInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) dispatchModalDimensions(width, height int) (int, int) {
	preferredWidth := 62
	preferredHeight := 21
	if m.chooseDispatchDirectory {
		preferredWidth = 78
		preferredHeight = 27
	}
	return modalDimensions(width, height, preferredWidth, preferredHeight)
}

func (m Model) renderDispatchModal(width, height int) string {
	modalWidth, modalHeight := m.dispatchModalDimensions(width, height)
	innerWidth := max(1, modalWidth-2)
	innerHeight := max(1, modalHeight-2)
	return renderModal(
		m.renderDispatchAt(innerWidth, innerHeight),
		modalWidth,
		modalHeight,
	)
}

func (m Model) renderAddWorkspaceModal(width, height int) string {
	modalWidth, modalHeight := modalDimensions(width, height, 72, 18)
	innerWidth := max(1, modalWidth-2)
	innerHeight := max(1, modalHeight-2)
	return renderModal(
		m.renderAddWorkspaceAt(innerWidth, innerHeight),
		modalWidth,
		modalHeight,
	)
}

func (m Model) renderAddWorkspace(width int) string {
	return m.renderAddWorkspaceAt(width, max(12, m.height-5))
}

func (m Model) renderAddWorkspaceAt(width, height int) string {
	contentWidth := max(1, width-4)
	m.cwdInput.SetWidth(max(10, contentWidth-2))
	lines := []string{
		titleStyle.Render("  Add workspace"),
		"",
		"  " + m.renderDispatchSectionTitle(
			accentStyle,
			"Choose a directory",
			fmt.Sprintf("%d/%d", m.directoryIndex+1, len(m.directories)),
			contentWidth,
		),
	}
	lines = append(lines, m.renderDirectoryRows(
		contentWidth,
		max(1, min(3, height-8)),
	)...)
	if selected, ok := m.selectedDirectory(); ok &&
		selected.kind == directoryCustom {
		lines = append(lines, "")
		lines = append(lines, indentLines(
			m.pathNav.view(max(10, contentWidth-2), 4), "  "))
	}
	if len(m.groups) > 0 {
		lines = append(lines,
			"",
			"  "+m.renderDispatchSectionTitle(
				mutedStyle.Copy().Bold(true),
				"Active workspaces",
				"read only",
				contentWidth,
			),
		)
		remaining := max(1, height-len(lines))
		lines = append(lines, m.renderActiveWorkspaceRows(
			contentWidth,
			remaining,
		)...)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderDispatch(width int) string {
	return m.renderDispatchAt(width, max(12, m.height-5))
}

// roomyForm reports whether the new-agent form has height to spare. A tight
// form drops its breathing room and the optional name row; the task
// composer is never what gets cut.
func roomyForm(height int) bool {
	return height >= 15
}

// dispatchNameVisible answers the same question against the modal the
// current terminal actually produces, so focus can skip a field that isn't
// drawn — a cursor in an invisible input is worse than no field at all.
func (m Model) dispatchNameVisible() bool {
	_, modalHeight := m.dispatchModalDimensions(m.bodyDimensions())
	return roomyForm(max(1, modalHeight-2))
}

func (m Model) renderDispatchAt(width, height int) string {
	providerStyle := titleStyle
	if m.formFocus == dispatchProvider {
		providerStyle = accentStyle
	}

	directoryStyle := titleStyle
	nameStyle := titleStyle
	taskStyle := titleStyle
	if m.formFocus == dispatchDirectory {
		directoryStyle = accentStyle
	}
	if m.formFocus == dispatchName {
		nameStyle = accentStyle
	}
	if m.formFocus == dispatchTask {
		taskStyle = accentStyle
	}
	contentWidth := max(1, width-4)
	m.cwdInput.SetWidth(max(10, contentWidth-2))

	headerLeft := titleStyle.Render("  New agent")
	summaryWidth := max(1, width-lipgloss.Width(headerLeft)-3)
	headerRight := mutedStyle.Render(truncate(m.dispatchSummary(), summaryWidth))
	gap := max(1, width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight)-1)
	lines := []string{
		headerLeft + strings.Repeat(" ", gap) + headerRight,
		"",
		"  " + providerStyle.Render("Coding agent"),
	}
	lines = append(lines, m.renderProviderRows(contentWidth)...)
	if m.chooseDispatchDirectory {
		lines = append(lines,
			"",
			"  "+m.renderDispatchSectionTitle(
				directoryStyle,
				"Working directory",
				fmt.Sprintf("%d/%d", m.directoryIndex+1, len(m.directories)),
				contentWidth,
			),
		)
		customPathLines := 0
		if selected, ok := m.selectedDirectory(); ok &&
			selected.kind == directoryCustom {
			customPathLines = 8
		}
		directoryRows := clamp(
			height-len(lines)-customPathLines-6,
			1,
			4,
		)
		lines = append(lines, m.renderDirectoryRows(
			contentWidth,
			directoryRows,
		)...)

		if selected, ok := m.selectedDirectory(); ok &&
			selected.kind == directoryCustom {
			lines = append(lines, "")
			lines = append(lines, indentLines(
				m.pathNav.view(max(10, contentWidth-2), 4), "  "))
		}
	}

	roomy := roomyForm(height)
	if roomy {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+m.renderDispatchModeLine(contentWidth))

	if roomy {
		lines = append(lines,
			"",
			"  "+m.renderDispatchSectionTitle(
				nameStyle,
				"Name",
				"optional",
				contentWidth,
			),
			"    "+m.renderNameField(max(10, contentWidth-2)),
		)
	}

	taskDetail := fmt.Sprintf(
		"%d chars",
		utf8.RuneCountInString(m.taskInput.Value()),
	)
	if roomy {
		lines = append(lines, "")
	}
	lines = append(lines,
		"  "+m.renderDispatchSectionTitle(
			taskStyle,
			"Task",
			taskDetail,
			contentWidth,
		),
	)
	taskHeight := clamp(height-len(lines)-4, 1, 6)
	lines = append(lines,
		indentLines(m.renderTaskComposer(contentWidth, taskHeight), "  "),
		"",
		"  "+mutedStyle.Render(truncate(m.commandHints(), contentWidth)),
	)
	return strings.Join(lines, "\n")
}

func indentLines(block, prefix string) string {
	rows := strings.Split(block, "\n")
	for i, row := range rows {
		rows[i] = prefix + row
	}
	return strings.Join(rows, "\n")
}

// renderNameField draws the optional agent name as a single row. It stays
// unboxed on purpose: the task composer's border marks where the multi-line
// field is, and a second box around a one-liner would read as equal weight.
func (m Model) renderNameField(width int) string {
	input := m.nameInput
	input.SetWidth(width)
	return input.View()
}

func (m Model) renderTaskComposer(width, height int) string {
	width = max(3, width)
	height = max(1, height)
	innerWidth := max(1, width-2)
	input := m.taskInput
	input.SetWidth(innerWidth)
	input.SetHeight(height)

	style := lipgloss.NewStyle().
		Width(innerWidth).
		Height(height).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorBorder)
	if m.formFocus == dispatchTask {
		style = style.BorderForeground(colorAccent)
	}
	return style.Render(input.View())
}

func nextDispatchMode(mode agent.PermissionMode) agent.PermissionMode {
	switch mode {
	case agent.ModeAsk:
		return agent.ModeEdits
	case agent.ModeEdits:
		return agent.ModeAuto
	default:
		return agent.ModeAsk
	}
}

func modeSummary(mode agent.PermissionMode) (string, string) {
	switch mode {
	case agent.ModeAsk:
		return "Ask", "asks first"
	case agent.ModeAuto:
		return "Auto", "never asks"
	default:
		return "Edits", "auto file edits"
	}
}

func modeBadge(mode agent.PermissionMode) string {
	switch mode {
	case agent.ModeAsk:
		return "ask"
	case agent.ModeAuto:
		return "AUTO"
	default:
		return ""
	}
}

func (m Model) renderDispatchModeLine(width int) string {
	label, description := modeSummary(m.dispatchMode)
	rendered := titleStyle.Render("Mode") + "  "
	if m.dispatchMode == agent.ModeAuto {
		rendered += lipgloss.NewStyle().
			Foreground(colorWaiting).Bold(true).Render(label)
	} else {
		rendered += accentStyle.Render(label)
	}
	rendered += "  "
	available := max(0, width-lipgloss.Width(rendered))
	detail := description + "  (m)"
	return rendered + mutedStyle.Render(truncate(detail, available))
}

func (m Model) renderDispatchSectionTitle(
	style lipgloss.Style,
	label string,
	right string,
	width int,
) string {
	renderedLabel := style.Render(label)
	renderedRight := mutedStyle.Render(right)
	gap := max(1, width-lipgloss.Width(renderedLabel)-lipgloss.Width(renderedRight))
	return renderedLabel + strings.Repeat(" ", gap) + renderedRight
}

func (m Model) renderDirectoryRows(width, maxRows int) []string {
	if len(m.directories) == 0 {
		return []string{"    " + mutedStyle.Render("No directories available")}
	}
	maxRows = clamp(maxRows, 1, 8)
	start, end := visibleRange(len(m.directories), m.directoryIndex, maxRows)
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, "  "+m.renderDirectoryRow(
			m.directories[index],
			index == m.directoryIndex,
			width,
		))
	}
	return rows
}

func (m Model) renderActiveWorkspaceRows(width, maxRows int) []string {
	if len(m.groups) == 0 || maxRows <= 0 {
		return nil
	}
	start, end := visibleRange(
		len(m.groups),
		m.workspaceCursor,
		min(maxRows, 4),
	)
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		value := m.groups[index].context
		name := strings.TrimSpace(value.Name)
		if name == "" {
			name = filepath.Base(value.Root)
		}
		contentWidth := max(1, width-4)
		nameWidth := clamp(contentWidth/3, 8, 24)
		pathWidth := max(1, contentWidth-nameWidth-2)
		row := lipgloss.NewStyle().
			Width(nameWidth).
			Render(truncate(name, nameWidth)) +
			"  " +
			truncatePathTail(value.Root, pathWidth)
		rows = append(rows, "    "+mutedStyle.Render(row))
	}
	return rows
}

func (m Model) renderDirectoryRow(
	choice directoryChoice,
	selected bool,
	width int,
) string {
	kind := strings.ToUpper(choice.workspaceKind)
	detail := shortPath(choice.path)
	switch choice.kind {
	case directoryYazi:
		kind = "YAZI"
		detail = "Interactive picker"
	case directoryCustom:
		kind = "PATH"
		detail = "Enter a directory"
	}
	if kind == "" {
		kind = "DIRECTORY"
	}

	contentWidth := max(1, width-2)
	plain := ""
	styled := ""
	if width >= 56 {
		labelWidth := clamp(contentWidth*30/100, 18, 28)
		kindWidth := 10
		detailWidth := max(1, contentWidth-labelWidth-kindWidth-4)
		label := lipgloss.NewStyle().
			Width(labelWidth).
			Render(truncate(choice.label, labelWidth))
		kind = lipgloss.NewStyle().
			Width(kindWidth).
			Render(truncate(kind, kindWidth))
		detail = truncate(detail, detailWidth)
		plain = label +
			"  " + kind +
			"  " + detail
		styled = titleStyle.Render(label) +
			"  " + mutedStyle.Render(
			kind,
		) +
			"  " + mutedStyle.Render(detail)
	} else {
		labelWidth := max(8, contentWidth/2)
		detailWidth := max(1, contentWidth-labelWidth-2)
		label := lipgloss.NewStyle().
			Width(labelWidth).
			Render(truncate(choice.label, labelWidth))
		detail = truncate(detail, detailWidth)
		plain = label + "  " + detail
		styled = titleStyle.Render(label) + "  " + mutedStyle.Render(detail)
	}
	if selected {
		return renderSelectableRow(
			plain,
			width,
			m.formFocus == dispatchDirectory,
		)
	}
	// One space matches the selectable row's single-column marker, so
	// selection doesn't shift the text sideways.
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render(" " + styled)
}

func renderSelectableRow(content string, width int, focused bool) string {
	return selectTheme.selectableRow(content, width, focused)
}

func (t rowTheme) selectableRow(content string, width int, focused bool) string {
	if width < 3 {
		return lipgloss.NewStyle().
			Foreground(t.text).
			Background(t.background).
			Bold(focused).
			Width(max(1, width)).
			Render(ansi.Truncate(content, width, ""))
	}
	contentWidth := width - 1
	marker := "▏"
	markerColor := t.restMark
	if focused {
		marker = "▌"
		markerColor = t.focusMark
	}
	markerStyle := lipgloss.NewStyle().
		Foreground(markerColor).
		Background(t.background).
		Bold(focused)
	rowStyle := lipgloss.NewStyle().
		Foreground(t.text).
		Background(t.background).
		Bold(focused).
		Width(contentWidth).
		MaxWidth(contentWidth)
	return markerStyle.Render(marker) +
		rowStyle.Render(ansi.Truncate(content, contentWidth, ""))
}

func (m Model) dispatchSummary() string {
	if !m.chooseDispatchDirectory {
		return ""
	}
	providerName := ""
	if m.providerIndex >= 0 && m.providerIndex < len(m.providers) {
		providerName = m.providers[m.providerIndex].Label
	}
	selected, _ := m.selectedDirectory()
	parts := make([]string, 0, 2)
	if providerName != "" {
		parts = append(parts, providerName)
	}
	if selected.label != "" {
		parts = append(parts, selected.label)
	}
	return strings.Join(parts, "  ·  ")
}

func (m Model) renderProviderRows(width int) []string {
	if len(m.providers) == 0 {
		return []string{
			"    " + mutedStyle.Render("No coding agents available"),
		}
	}

	rows := make([]string, 0, len(m.providers))
	for index, info := range m.providers {
		status := "ready"
		if !info.Available {
			status = "not found"
		}
		contentWidth := max(1, width-2)
		statusWidth := min(9, contentWidth)
		labelWidth := max(1, contentWidth-statusWidth-2)
		label := lipgloss.NewStyle().
			Width(labelWidth).
			Render(truncate(info.Label, labelWidth))
		status = truncate(status, statusWidth)
		plain := label + "  " + status
		if index == m.providerIndex {
			rows = append(rows, "  "+renderSelectableRow(
				plain,
				width,
				m.formFocus == dispatchProvider,
			))
			continue
		}
		statusStyle := successStyle
		if !info.Available {
			statusStyle = errorStyle
		}
		styled := titleStyle.Render(label) +
			"  " + statusStyle.Render(status)
		rows = append(rows, "  "+lipgloss.NewStyle().
			Width(width).
			MaxWidth(width).
			Render(" "+styled))
	}
	return rows
}

func (m *Model) focusForm() {
	m.cwdInput.Blur()
	m.nameInput.Blur()
	m.taskInput.Blur()
	switch m.formFocus {
	case dispatchCustomPath:
		m.pathNav.filter.Focus()
	case dispatchName:
		m.nameInput.Focus()
	case dispatchTask:
		m.taskInput.Focus()
	}
}

// startPathNav opens the interactive cd at the most relevant directory.
// startPathNav opens the picker where Stormlight itself was launched — the
// shell's working directory is the natural anchor for typing a path, and
// workspace-relative starts are already covered by the picker's other rows.
func (m *Model) startPathNav() {
	m.pathNav = newPathNav(m.initialCwd)
}

// handlePathNavKey drives the interactive cd. The return value reports a
// confirmation: Enter with nothing highlighted chooses the directory the
// navigator is sitting in.
// handlePathNavKey drives the fzf-style picker; a true return means Enter
// chose a directory (available via pathNav.chosen()).
func (m *Model) handlePathNavKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "tab", "down", "ctrl+n":
		m.pathNav.moveHighlight(1)
		return false
	case "up", "ctrl+p":
		m.pathNav.moveHighlight(-1)
		return false
	case "backspace":
		if m.pathNav.filterEmpty() {
			m.pathNav.up()
			return false
		}
	case "enter":
		if attempted, ok := m.pathNav.jump(); attempted {
			if !ok {
				m.err = fmt.Errorf(
					"no such directory: %s",
					strings.TrimSpace(m.pathNav.filter.Value()),
				)
			} else {
				m.err = nil
			}
			return false
		}
		if m.pathNav.chosen() == "" {
			m.err = fmt.Errorf("no matching directory")
			return false
		}
		m.err = nil
		return true
	}
	m.pathNav.update(msg)
	return false
}

func (m *Model) blurForm() {
	m.cwdInput.Blur()
	m.nameInput.Blur()
	m.taskInput.Blur()
}

func (m *Model) prepareDirectoryChoices(preferred string) {
	m.pickerStart = preferred
	choices := make([]directoryChoice, 0)
	indexes := make(map[string]int)
	addPath := func(path, label, workspaceKind string) int {
		path = strings.TrimSpace(path)
		if path == "" {
			return -1
		}
		key := directoryKey(path)
		if index, ok := indexes[key]; ok {
			return index
		}
		index := len(choices)
		indexes[key] = index
		choices = append(choices, directoryChoice{
			kind:          directoryPath,
			label:         label,
			path:          filepath.Clean(path),
			workspaceKind: workspaceKind,
		})
		return index
	}

	for _, group := range m.groups {
		groupRoot := group.context.ExecutionRoot
		if groupRoot == "" {
			groupRoot = group.context.Root
		}
		addPath(groupRoot, group.context.Name, group.context.Kind)
		for _, managedAgent := range group.agents {
			value := effectiveWorkspace(managedAgent)
			executionRoot := value.ExecutionRoot
			if executionRoot == "" {
				executionRoot = value.Root
			}
			label := value.Name
			if directoryKey(executionRoot) != directoryKey(groupRoot) {
				label += " / " + filepath.Base(executionRoot)
			}
			addPath(executionRoot, label, value.Kind)
			if value.ComponentRoot != "" &&
				directoryKey(value.ComponentRoot) != directoryKey(executionRoot) {
				component := value.ComponentName
				if component == "" {
					component = filepath.Base(value.ComponentRoot)
				}
				addPath(
					value.ComponentRoot,
					value.Name+" / "+component,
					value.Kind,
				)
			}
			if managedAgent.Cwd != "" &&
				directoryKey(managedAgent.Cwd) != directoryKey(executionRoot) &&
				directoryKey(managedAgent.Cwd) != directoryKey(value.ComponentRoot) {
				addPath(
					managedAgent.Cwd,
					value.Name+" / "+filepath.Base(managedAgent.Cwd),
					value.Kind,
				)
			}
		}
	}

	currentIndex := addPath(
		m.initialCwd,
		filepath.Base(filepath.Clean(m.initialCwd))+" (current)",
		workspace.KindDirectory,
	)
	if currentIndex >= 0 && currentIndex < len(choices) &&
		directoryKey(choices[currentIndex].path) == directoryKey(m.initialCwd) &&
		!strings.Contains(choices[currentIndex].label, "(current)") {
		choices[currentIndex].label += " (current)"
	}

	preferredKey := directoryKey(preferred)
	preferredIndex, ok := indexes[preferredKey]
	if !ok {
		preferredIndex = addPath(
			preferred,
			"Selected / "+filepath.Base(filepath.Clean(preferred)),
			workspace.KindDirectory,
		)
	}
	if len(choices) == 0 {
		preferredIndex = addPath(m.initialCwd, "Current", workspace.KindDirectory)
	}
	if m.yaziPath != "" {
		choices = append(choices, directoryChoice{
			kind:  directoryYazi,
			label: "Browse with Yazi",
		})
	}
	choices = append(choices, directoryChoice{
		kind:  directoryCustom,
		label: "Enter a path",
	})

	m.directories = choices
	m.directoryIndex = clamp(preferredIndex, 0, max(0, len(choices)-1))
	if selected, ok := m.selectedDirectory(); ok && selected.kind == directoryPath {
		m.cwdInput.SetValue(selected.path)
		m.pickerStart = selected.path
	}
}

func (m *Model) prepareAddWorkspaceChoices(start string) {
	if !isDirectory(start) {
		start = m.initialCwd
	}
	m.pickerStart = start
	choices := make([]directoryChoice, 0, 2)
	if m.yaziPath != "" {
		choices = append(choices, directoryChoice{
			kind:  directoryYazi,
			label: "Browse with Yazi",
		})
	}
	choices = append(choices, directoryChoice{
		kind:  directoryCustom,
		label: "Enter a path",
	})
	m.directories = choices
	m.directoryIndex = 0
	m.cwdInput.SetValue("")
}

func (m *Model) selectDirectory(delta int) {
	if len(m.directories) == 0 {
		return
	}
	m.selectDirectoryIndex(
		(m.directoryIndex + delta + len(m.directories)) % len(m.directories),
	)
}

func (m *Model) selectDirectoryIndex(index int) {
	if len(m.directories) == 0 {
		return
	}
	m.directoryIndex = clamp(index, 0, len(m.directories)-1)
	if selected, ok := m.selectedDirectory(); ok && selected.kind == directoryPath {
		m.cwdInput.SetValue(selected.path)
		m.pickerStart = selected.path
	}
}

func (m *Model) editSelectedDirectory() {
	selected, ok := m.selectedDirectory()
	if ok && selected.kind == directoryPath {
		m.cwdInput.SetValue(selected.path)
	}
	for index := range m.directories {
		if m.directories[index].kind == directoryCustom {
			m.directoryIndex = index
			break
		}
	}
	m.formFocus = dispatchCustomPath
	m.focusForm()
}

// dispatchFocusOrder is the tab cycle through the new-agent form, in the
// order the fields are drawn.
func (m Model) dispatchFocusOrder() []dispatchFocus {
	focuses := []dispatchFocus{dispatchProvider}
	if m.chooseDispatchDirectory {
		focuses = append(focuses, dispatchDirectory)
		if selected, ok := m.selectedDirectory(); ok &&
			selected.kind == directoryCustom {
			focuses = append(focuses, dispatchCustomPath)
		}
	}
	if m.dispatchNameVisible() {
		focuses = append(focuses, dispatchName)
	}
	return append(focuses, dispatchTask)
}

func (m *Model) moveDispatchFocus(delta int) {
	focuses := m.dispatchFocusOrder()
	current := 0
	for index, focus := range focuses {
		if focus == m.formFocus {
			current = index
			break
		}
	}
	m.formFocus = focuses[(current+delta+len(focuses))%len(focuses)]
	m.focusForm()
}

func (m Model) submitDispatch() (tea.Model, tea.Cmd) {
	if len(m.providers) == 0 {
		m.err = fmt.Errorf("no providers configured")
		return m, nil
	}
	request := app.DispatchRequest{
		Provider: m.providers[m.providerIndex].ID,
		Name:     strings.TrimSpace(m.nameInput.Value()),
		Cwd:      strings.TrimSpace(m.cwdInput.Value()),
		Task:     strings.TrimSpace(m.taskInput.Value()),
		Mode:     m.dispatchMode,
	}
	if request.Task == "" {
		m.err = fmt.Errorf("task cannot be empty")
		return m, nil
	}
	if !isDirectory(request.Cwd) {
		m.err = fmt.Errorf("working directory is unavailable: %s", request.Cwd)
		return m, nil
	}
	m.mode = modeNormal
	m.blurForm()
	m.status = "Dispatching " + m.providers[m.providerIndex].Label
	m.taskInput.SetValue("")
	m.nameInput.SetValue("")
	return m, dispatchCmd(m.backend, request)
}

func (m Model) selectedDirectory() (directoryChoice, bool) {
	if m.directoryIndex < 0 || m.directoryIndex >= len(m.directories) {
		return directoryChoice{}, false
	}
	return m.directories[m.directoryIndex], true
}

func (m Model) beginDispatch(chooseDirectory bool) (tea.Model, tea.Cmd) {
	directory := m.initialCwd
	selectedWorkspace, hasWorkspace := m.selectedWorkspace()
	if hasWorkspace {
		selected := selectedWorkspace
		directory = selected.ExecutionRoot
		if directory == "" {
			directory = selected.Root
		}
	}
	if !chooseDirectory && !hasWorkspace {
		chooseDirectory = true
	}
	if selected, ok := m.selectedAgent(); ok &&
		(chooseDirectory || m.activePane == paneInteraction) {
		directory = selected.Cwd
	}
	m.chooseDispatchDirectory = chooseDirectory
	if chooseDirectory {
		m.prepareDirectoryChoices(directory)
		m.formFocus = dispatchDirectory
	} else {
		m.cwdInput.SetValue(directory)
		m.formFocus = dispatchProvider
	}
	m.applyDispatchOverrides(directory)
	m.mode = modeDispatch
	m.dispatchPrefix = ""
	m.focusForm()
	m.err = nil
	m.status = "New agent"
	return m, nil
}

// applyDispatchOverrides applies per-workspace config defaults for the
// directory the form opens in. The user's in-form choices still win — this
// only presets the fields.
func (m *Model) applyDispatchOverrides(directory string) {
	if m.modeForDir != nil {
		if mode, ok := m.modeForDir(directory); ok {
			m.dispatchMode = mode
		}
	}
	if m.providerForDir == nil {
		return
	}
	if providerID, ok := m.providerForDir(directory); ok {
		for index, info := range m.providers {
			if info.ID == providerID {
				m.providerIndex = index
				break
			}
		}
	}
}
