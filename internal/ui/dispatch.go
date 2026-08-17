package ui

// The new-agent and add-workspace forms: focus, choices, submission.
// Split from model.go; see #34.

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/workspace"
)

// updateDispatch handles a key in the new-agent form and then re-sizes the
// task textarea. Nearly every key can change the box the composer is drawn
// in — a keystroke rewraps it, a directory choice pushes it down the form —
// and the textarea has to be told, so the sync wraps the key handling
// instead of trailing every branch of it.
func (m Model) updateDispatch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	next, cmd := m.dispatchKey(msg)
	updated, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	updated.syncTaskComposerSize()
	return updated, cmd
}

func (m Model) dispatchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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

	// The picker owns almost every key while it has focus, but not Tab:
	// Tab is the form's field cycle, and a picker that swallows it strands
	// the fields below with no way forward. ↑/↓ and Ctrl-n/p move the
	// highlight instead.
	if m.formFocus == dispatchCustomPath &&
		key != "esc" && key != "ctrl+c" && key != "ctrl+[" &&
		key != "ctrl+s" && key != "tab" && key != "shift+tab" {
		if confirmed := m.handlePathNavKey(msg); confirmed {
			m.cwdInput.SetValue(m.pathNav.chosen())
			m.pickerStart = m.pathNav.chosen()
			m.moveDispatchFocus(1)
		}
		return m, nil
	}

	switch key {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.blurForm()
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
	case "ctrl+j":
		// Enter launches the agent, so the task box needs its own newline
		// key — the same one the Spanreed composer uses.
		if m.formFocus == dispatchTask {
			insertTextareaNewline(&m.taskInput)
			return m, nil
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
		// Enter advances one step through the form's own field order, so it
		// can never skip a field Tab would stop on — the optional name row
		// used to fall in exactly that gap.
		switch m.formFocus {
		case dispatchProvider, dispatchName:
			m.moveDispatchFocus(1)
			return m, nil
		case dispatchDirectory:
			selected, ok := m.selectedDirectory()
			if !ok {
				m.moveDispatchFocus(1)
				return m, nil
			}
			switch selected.kind {
			case directoryYazi:
				return m.openYazi()
			case directoryCustom:
				m.formFocus = dispatchCustomPath
				m.startPathNav()
				m.focusForm()
			default:
				m.moveDispatchFocus(1)
			}
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
	preferredHeight := 24
	if m.chooseDispatchDirectory {
		preferredWidth = 78
		preferredHeight = 29
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

func (m Model) renderAddWorkspaceAt(width, height int) string {
	contentWidth := max(1, width-4)
	m.cwdInput.SetWidth(max(10, contentWidth-2))
	lines := []string{
		titleStyle().Render("  Add workspace"),
		"",
		"  " + m.renderDispatchSectionTitle(
			accentStyle(),
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
				mutedStyle().Copy().Bold(true),
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

// The task composer's floor and ceiling. Under three rows a wrapped task
// stops being editable — most of what you typed is scrolled out of sight —
// and past six the box crowds the rest of the form.
const (
	minTaskHeight = 3
	maxTaskHeight = 6
)

// dispatchDensity is how much of the form's optional furniture a given box
// can hold. The order is the order things are given up in, and it is a
// ladder rather than a switch because the two economies are not worth the
// same: blank lines are decoration, the name row is a field. Spending them
// together — as one roomy flag once did — meant the first pixel of pressure
// cost the user a field they could no longer reach, at terminal sizes as
// ordinary as 100x30. See #73.
type dispatchDensity int

const (
	// densityRoomy: blank lines between sections, and the name row.
	densityRoomy dispatchDensity = iota
	// densitySnug: the name row, with the blank lines given back.
	densitySnug
	// densityTight: neither. The name is optional and the agent falls back
	// to a name derived from its task, so this is a real if unhappy shape —
	// but it is the last rung, not the first.
	densityTight
)

// spaced reports whether sections are separated by blank lines.
func (d dispatchDensity) spaced() bool { return d == densityRoomy }

// separated reports whether the form keeps the two blanks that set the
// header and the directory section apart. They used to be unconditional, so
// the tightest form still spent two rows on whitespace it had already
// decided it could not afford — which is most of what pushed the form out of
// a twenty-row terminal.
func (d dispatchDensity) separated() bool { return d != densityTight }

// showsName reports whether the optional name row is drawn — and therefore
// whether it can hold focus at all.
func (d dispatchDensity) showsName() bool { return d <= densitySnug }

// dispatchFormLayout is the new-agent form's shape inside a given box: the
// rows above the task composer, how tall the composer gets, and how much
// furniture survived. Drawing the form and sizing the persisted textarea
// have to agree on all three, so both ask for a layout rather than each
// deriving its own.
type dispatchFormLayout struct {
	head       []string
	taskHeight int
	density    dispatchDensity
}

// dispatchLayout lays the form out at the given content box, taking the
// densest shape whose composer still clears its minimum. The composer is the
// field the form exists to fill, so it is never what gets cut; everything
// else is given up in the order dispatchDensity sets out.
func (m Model) dispatchLayout(width, height int) dispatchFormLayout {
	best := dispatchFormLayout{}
	for _, density := range []dispatchDensity{densityRoomy, densitySnug, densityTight} {
		candidate := dispatchFormLayout{density: density}
		candidate.head = m.dispatchHeadLines(width, height, density)
		candidate.taskHeight = dispatchTaskHeight(
			height, renderedRows(candidate.head), density)
		if candidate.taskHeight >= minTaskHeight {
			return candidate
		}
		// Nothing clears the minimum yet. Keep whichever shape gives the
		// composer the most, and let a tie fall to the later rung: once no
		// shape can be edited comfortably, the sparser one at least fits
		// more of itself on screen.
		if candidate.taskHeight >= best.taskHeight {
			best = candidate
		}
	}
	return best
}

// dispatchTaskHeight is what the head rows leave once the composer's border
// and the hint row have taken their share — plus, where the form is spaced,
// the blank line that separates them.
func dispatchTaskHeight(height, headRows int, density dispatchDensity) int {
	reserved := 3
	if density.spaced() {
		reserved = 4
	}
	return clamp(height-headRows-reserved, 1, maxTaskHeight)
}

// renderedRows counts the rows a slice of form lines draws. Entries are not
// all one row: the directory picker arrives as a block of its own, and
// measuring the composer's share against the slice length hands it rows the
// modal has already spent — which pushes the hint line, and then the box
// itself, off the bottom.
func renderedRows(lines []string) int {
	rows := 0
	for _, line := range lines {
		rows += strings.Count(line, "\n") + 1
	}
	return rows
}

// dispatchNameVisible answers whether the modal the current terminal
// produces draws the optional name row, so focus can skip a field that isn't
// there — a cursor in an invisible input is worse than no field at all.
func (m Model) dispatchNameVisible() bool {
	return m.dispatchLayout(m.dispatchContentDimensions()).density.showsName()
}

// dispatchContentDimensions is the modal's interior for the terminal as it
// stands, matching what renderBody hands renderDispatchModal.
func (m Model) dispatchContentDimensions() (int, int) {
	modalWidth, modalHeight := m.dispatchModalDimensions(m.bodyDimensions())
	return max(1, modalWidth-2), max(1, modalHeight-2)
}

func (m Model) renderDispatchAt(width, height int) string {
	contentWidth := max(1, width-4)
	layout := m.dispatchLayout(width, height)

	tail := []string{
		indentLines(m.renderTaskComposer(contentWidth, layout.taskHeight), "  "),
	}
	if layout.density.spaced() {
		tail = append(tail, "")
	}
	tail = append(tail,
		"  "+ansi.Truncate(renderHints(m.commandHints(), mutedStyle()), contentWidth, "…"),
	)

	return strings.Join(
		append(trimHeadToFit(layout.head, renderedRows(tail), height), tail...),
		"\n",
	)
}

// trimHeadToFit drops leading rows until the form fits the box it is drawn
// in. Below about eighteen rows the terminal cannot hold even the tightest
// form, and something has to go; what must not go is the composer and the
// hint line, because between them they are the field the form exists to fill
// and the only thing on screen that says how to leave.
//
// So the head yields, from the top. Those rows are the ones the reader can
// most afford to lose: the title says what they already know they opened,
// and the provider list restates a choice the summary line carries anyway.
// Clipping from the bottom instead — which is what happens when nothing
// trims and the modal simply cuts — takes the composer's own border and the
// hints with it, leaving a form that looks broken and strands the reader in
// it.
func trimHeadToFit(head []string, tailRows, height int) []string {
	for len(head) > 0 && renderedRows(head)+tailRows > height {
		head = head[1:]
	}
	return head
}

// dispatchHeadLines renders every row above the task composer. It stops
// there because how much room is left is the caller's question.
func (m Model) dispatchHeadLines(width, height int, density dispatchDensity) []string {
	providerStyle := titleStyle()
	if m.formFocus == dispatchProvider {
		providerStyle = accentStyle()
	}

	directoryStyle := titleStyle()
	nameStyle := titleStyle()
	taskStyle := titleStyle()
	if m.formFocus == dispatchDirectory {
		directoryStyle = accentStyle()
	}
	if m.formFocus == dispatchName {
		nameStyle = accentStyle()
	}
	if m.formFocus == dispatchTask {
		taskStyle = accentStyle()
	}
	contentWidth := max(1, width-4)
	m.cwdInput.SetWidth(max(10, contentWidth-2))

	headerLeft := titleStyle().Render("  New agent")
	summaryWidth := max(1, width-lipgloss.Width(headerLeft)-3)
	headerRight := mutedStyle().Render(truncate(m.dispatchSummary(), summaryWidth))
	gap := max(1, width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight)-1)
	lines := []string{
		headerLeft + strings.Repeat(" ", gap) + headerRight,
	}
	if density.separated() {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+providerStyle.Render("Coding agent"))
	lines = append(lines, m.renderProviderRows(contentWidth)...)
	if m.chooseDispatchDirectory {
		if density.separated() {
			lines = append(lines, "")
		}
		lines = append(lines,
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
			height-renderedRows(lines)-customPathLines-6,
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

	if density.spaced() {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+m.renderDispatchModeLine(contentWidth))

	if density.showsName() {
		if density.spaced() {
			lines = append(lines, "")
		}
		lines = append(lines,
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
	if density.spaced() {
		lines = append(lines, "")
	}
	return append(lines,
		"  "+m.renderDispatchSectionTitle(
			taskStyle,
			"Task",
			taskDetail,
			contentWidth,
		),
	)
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
	height = max(1, height)
	innerWidth := taskComposerWidth(width)
	input := m.taskInput
	input.SetWidth(innerWidth)
	input.SetHeight(height)

	// Curved, like the modal it sits inside. A square box nested in a rounded
	// one reads as a rendering slip rather than a distinction.
	//
	// The border is counted inside these dimensions in v2, so the box is
	// described by its outside edges; innerWidth stays the input's width.
	style := lipgloss.NewStyle().
		Width(width).
		Height(height + 2).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorBorder())
	if m.formFocus == dispatchTask {
		style = style.BorderForeground(colorAccent())
	}
	return style.Render(input.View())
}

// taskComposerWidth is the textarea's width inside the composer's border.
func taskComposerWidth(width int) int {
	return max(1, max(3, width)-2)
}

// syncTaskComposerSize keeps the persisted task textarea at the size the form
// draws it. The textarea wraps and scrolls against its own width and height,
// and that state lives on the model between keystrokes — sizing only the
// render-time copy leaves the persisted one wrapping at its default width,
// counting more rows than the box has, and scrolling the viewport the two
// copies share past the top of what was typed. Nothing scrolls it back.
func (m *Model) syncTaskComposerSize() {
	width, height := m.dispatchContentDimensions()
	layout := m.dispatchLayout(width, height)
	previous := m.taskInput.Height()
	m.taskInput.SetWidth(taskComposerWidth(max(1, width-4)))
	m.taskInput.SetHeight(layout.taskHeight)
	if layout.taskHeight > previous {
		resetTextareaScroll(&m.taskInput)
	}
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
	rendered := titleStyle().Render("Mode") + "  "
	if m.dispatchMode == agent.ModeAuto {
		rendered += lipgloss.NewStyle().
			Foreground(colorWaiting()).Bold(true).Render(label)
	} else {
		rendered += accentStyle().Render(label)
	}
	rendered += "  "
	available := max(0, width-lipgloss.Width(rendered))
	detail := description + "  (m)"
	return rendered + mutedStyle().Render(truncate(detail, available))
}

func (m Model) renderDispatchSectionTitle(
	style lipgloss.Style,
	label string,
	right string,
	width int,
) string {
	renderedLabel := style.Render(label)
	renderedRight := mutedStyle().Render(right)
	gap := max(1, width-lipgloss.Width(renderedLabel)-lipgloss.Width(renderedRight))
	return renderedLabel + strings.Repeat(" ", gap) + renderedRight
}

func (m Model) renderDirectoryRows(width, maxRows int) []string {
	if len(m.directories) == 0 {
		return []string{"    " + mutedStyle().Render("No directories available")}
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
		rows = append(rows, "    "+mutedStyle().Render(row))
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
		styled = titleStyle().Render(label) +
			"  " + mutedStyle().Render(
			kind,
		) +
			"  " + mutedStyle().Render(detail)
	} else {
		labelWidth := max(8, contentWidth/2)
		detailWidth := max(1, contentWidth-labelWidth-2)
		label := lipgloss.NewStyle().
			Width(labelWidth).
			Render(truncate(choice.label, labelWidth))
		detail = truncate(detail, detailWidth)
		plain = label + "  " + detail
		styled = titleStyle().Render(label) + "  " + mutedStyle().Render(detail)
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
	return selectTheme().selectableRow(content, width, focused)
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
			"    " + mutedStyle().Render("No coding agents available"),
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
		statusStyle := successStyle()
		if !info.Available {
			statusStyle = errorStyle()
		}
		styled := titleStyle().Render(label) +
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
func (m *Model) handlePathNavKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "down", "ctrl+n":
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

	for _, value := range m.workspaceRoots {
		label := value.Name
		if rootLabel := strings.TrimSpace(
			value.Metadata["execution_root_label"],
		); rootLabel != "" {
			label += " / " + rootLabel
		} else if directoryKey(value.ExecutionRoot) != directoryKey(value.Root) {
			label += " / " + filepath.Base(value.ExecutionRoot)
		}
		addPath(value.ExecutionRoot, label, value.Kind)
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
			if rootLabel := strings.TrimSpace(
				value.Metadata["execution_root_label"],
			); rootLabel != "" {
				label += " / " + rootLabel
			} else if directoryKey(executionRoot) != directoryKey(groupRoot) {
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
		// The field holding focus is always part of the order, even if the
		// directory row that opened it has since gone away — otherwise a
		// move from it has no anchor and silently restarts at the top.
		if selected, ok := m.selectedDirectory(); (ok &&
			selected.kind == directoryCustom) ||
			m.formFocus == dispatchCustomPath {
			focuses = append(focuses, dispatchCustomPath)
		}
	}
	if m.dispatchNameVisible() {
		focuses = append(focuses, dispatchName)
	}
	return append(focuses, dispatchTask)
}

// nextDispatchField names the field Enter and Tab move to from here. The
// hints quote it, so what the footer promises and where focus lands come
// from the same order.
func (m Model) nextDispatchField() string {
	focuses := m.dispatchFocusOrder()
	current := 0
	for index, focus := range focuses {
		if focus == m.formFocus {
			current = index
			break
		}
	}
	switch focuses[(current+1)%len(focuses)] {
	case dispatchProvider:
		return "agent"
	case dispatchDirectory:
		return "location"
	case dispatchCustomPath:
		return "path"
	case dispatchName:
		return "name"
	default:
		return "task"
	}
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
	m.syncTaskComposerSize()
	m.err = nil
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
