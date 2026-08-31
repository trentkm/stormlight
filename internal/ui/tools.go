package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ToolOverlay is a configured interactive program. Its binary and arguments
// are passed straight to the runtime rather than through a shell.
type ToolOverlay struct {
	ID     string
	Title  string
	Binary string
	Args   []string
	Hotkey []string
	Host   string
}

type toolOverlayForm struct {
	editing         int
	focus           int
	id              lineInput
	title           lineInput
	binary          lineInput
	args            lineInput
	hotkey          lineInput
	host            lineInput
	validation      string
	validationField int
	touched         [6]bool
	submitted       bool
}

func sortToolOverlays(overlays []ToolOverlay) []ToolOverlay {
	out := append([]ToolOverlay(nil), overlays...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func overlayTitle(tool ToolOverlay) string {
	if tool.Title != "" {
		return tool.Title
	}
	return tool.ID
}

func toolHotkeyLabel(hotkey []string) string { return strings.Join(hotkey, " ") }

func sameToolHotkey(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func startsToolHotkey(full, prefix []string) bool {
	return len(prefix) <= len(full) && sameToolHotkey(full[:len(prefix)], prefix)
}

// toolOverlayForKey consumes a configured multi-key chord before dashboard
// commands. Config validation only permits unused roots, so this cannot hide
// an existing dashboard action.
func (m *Model) toolOverlayForKey(key string) (*ToolOverlay, bool) {
	sequence := append(append([]string(nil), m.toolPrefix...), key)
	var match *ToolOverlay
	waiting := false
	for index := range m.toolOverlays {
		tool := &m.toolOverlays[index]
		if sameToolHotkey(tool.Hotkey, sequence) {
			match = tool
		}
		if len(sequence) < len(tool.Hotkey) && startsToolHotkey(tool.Hotkey, sequence) {
			waiting = true
		}
	}
	if match != nil {
		m.toolPrefix = nil
		return match, false
	}
	if waiting {
		m.toolPrefix = sequence
		return nil, true
	}
	if len(m.toolPrefix) > 0 {
		m.toolPrefix = nil
		return nil, true
	}
	return nil, false
}

func (m Model) toolOverlaySpec(tool ToolOverlay) overlaySpec {
	return overlaySpec{
		title:       overlayTitle(tool),
		host:        tool.Host,
		path:        tool.Binary,
		args:        append([]string(nil), tool.Args...),
		minimizable: true,
		toolID:      tool.ID,
		result: func(_ string, runErr error) tea.Msg {
			if runErr != nil {
				return actionMsg{err: fmt.Errorf("%s: %w", overlayTitle(tool), runErr)}
			}
			return actionMsg{}
		},
		cleanup: func() {},
	}
}

func (m Model) toggleToolOverlay(tool ToolOverlay) (tea.Model, tea.Cmd) {
	if hidden := m.minimizedOverlay; hidden != nil && hidden.spec.toolID == tool.ID {
		return m.restoreMinimizedOverlay()
	}
	if hidden := m.minimizedOverlay; hidden != nil {
		m.minimizedOverlay = nil
		return m, tea.Sequence(closeMinimizedOverlayCmd(hidden), m.openOverlay(m.toolOverlaySpec(tool)))
	}
	return m, m.openOverlay(m.toolOverlaySpec(tool))
}

func (m Model) restoreMinimizedOverlay() (tea.Model, tea.Cmd) {
	view := m.minimizedOverlay
	m.minimizedOverlay = nil
	outerWidth, outerHeight := m.overlayDimensions()
	_, resize := view.widget.SetSize(max(2, outerWidth-2), max(2, outerHeight-2))
	view.widget.SetVisible(true)
	m.overlay = view
	return m, tea.Batch(resize, m.armPTYWait())
}

func (m *Model) beginToolManager() {
	m.toolCursor = min(m.toolCursor, max(0, len(m.toolOverlays)-1))
	m.mode = modeTools
	m.clearComplaint(modeTools)
}

func (m *Model) beginToolEdit(index int) {
	m.toolForm = toolOverlayForm{editing: index}
	if index >= 0 {
		tool := m.toolOverlays[index]
		m.toolForm.id.SetValue(tool.ID)
		m.toolForm.title.SetValue(tool.Title)
		m.toolForm.binary.SetValue(tool.Binary)
		if len(tool.Args) > 0 {
			encoded, _ := json.Marshal(tool.Args)
			m.toolForm.args.SetValue(string(encoded))
		}
		m.toolForm.hotkey.SetValue(strings.Join(tool.Hotkey, " "))
		m.toolForm.host.SetValue(tool.Host)
	}
	m.toolForm.id = ensureToolInput(m.toolForm.id, "id")
	m.toolForm.title = ensureToolInput(m.toolForm.title, "Title")
	m.toolForm.binary = ensureToolInput(m.toolForm.binary, "Executable")
	m.toolForm.args = ensureToolInput(m.toolForm.args, "Arguments as JSON array")
	m.toolForm.hotkey = ensureToolInput(m.toolForm.hotkey, "c r")
	m.toolForm.host = ensureToolInput(m.toolForm.host, "Local machine")
	m.focusToolField(0)
	m.mode = modeToolEdit
	m.refreshToolValidation()
	m.clearComplaint(modeToolEdit)
}

func ensureToolInput(input lineInput, placeholder string) lineInput {
	if input.placeholder == "" {
		value := input.Value()
		input = newLineInput(placeholder)
		input.SetValue(value)
	}
	return input
}

func (m *Model) focusToolField(index int) {
	m.toolForm.focus = (index + 6) % 6
	for field, input := range m.toolFormInputs() {
		if field == m.toolForm.focus {
			input.Focus()
		} else {
			input.Blur()
		}
		m.setToolFormInput(field, input)
	}
}

func (m Model) toolFormInputs() []lineInput {
	return []lineInput{m.toolForm.id, m.toolForm.title, m.toolForm.binary, m.toolForm.args, m.toolForm.hotkey, m.toolForm.host}
}

func (m *Model) setToolFormInput(index int, input lineInput) {
	switch index {
	case 0:
		m.toolForm.id = input
	case 1:
		m.toolForm.title = input
	case 2:
		m.toolForm.binary = input
	case 3:
		m.toolForm.args = input
	case 4:
		m.toolForm.hotkey = input
	case 5:
		m.toolForm.host = input
	}
}

func (m Model) updateTools(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
	case "j", "down":
		if len(m.toolOverlays) > 0 {
			m.toolCursor = min(len(m.toolOverlays)-1, m.toolCursor+1)
		}
	case "k", "up":
		m.toolCursor = max(0, m.toolCursor-1)
	case "n":
		m.beginToolEdit(-1)
	case "e":
		if len(m.toolOverlays) > 0 {
			m.beginToolEdit(m.toolCursor)
		}
	case "d":
		if len(m.toolOverlays) > 0 {
			tools := append([]ToolOverlay(nil), m.toolOverlays...)
			tools = append(tools[:m.toolCursor], tools[m.toolCursor+1:]...)
			if err := m.persistToolOverlays(tools); err != nil {
				m.complain(err)
			}
		}
	case "enter":
		if len(m.toolOverlays) > 0 {
			return m.toggleToolOverlay(m.toolOverlays[m.toolCursor])
		}
	}
	return m, nil
}

func (m Model) updateToolEdit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeTools
		return m, nil
	case "tab", "down":
		m.focusToolField(m.toolForm.focus + 1)
		return m, nil
	case "shift+tab", "up":
		m.focusToolField(m.toolForm.focus - 1)
		return m, nil
	case "enter":
		m.toolForm.submitted = true
		m.refreshToolValidation()
		if m.toolForm.validation == "" {
			if err := m.submitToolForm(); err != nil {
				m.toolForm.validation = err.Error()
			}
		}
		return m, nil
	}
	input := m.toolFormInputs()[m.toolForm.focus].Update(msg)
	m.setToolFormInput(m.toolForm.focus, input)
	m.toolForm.touched[m.toolForm.focus] = true
	m.refreshToolValidation()
	return m, nil
}

func (m *Model) submitToolForm() error {
	if err := m.validateToolForm(); err != nil {
		return err
	}
	tool := ToolOverlay{
		ID:     strings.TrimSpace(m.toolForm.id.Value()),
		Title:  strings.TrimSpace(m.toolForm.title.Value()),
		Binary: strings.TrimSpace(m.toolForm.binary.Value()),
		Hotkey: strings.Fields(strings.ToLower(m.toolForm.hotkey.Value())),
		Host:   strings.TrimSpace(m.toolForm.host.Value()),
	}
	if !validToolID(tool.ID) || tool.Binary == "" {
		return fmt.Errorf("id and executable are required")
	}
	if len(strings.TrimSpace(m.toolForm.args.Value())) > 0 {
		if err := json.Unmarshal([]byte(m.toolForm.args.Value()), &tool.Args); err != nil {
			return fmt.Errorf("arguments must be a JSON string array: %w", err)
		}
	}
	if err := validToolHotkey(tool.Hotkey); err != nil {
		return err
	}
	tools := append([]ToolOverlay(nil), m.toolOverlays...)
	if m.toolForm.editing >= 0 {
		tools = append(tools[:m.toolForm.editing], tools[m.toolForm.editing+1:]...)
	}
	for _, other := range tools {
		if other.ID == tool.ID {
			return fmt.Errorf("another tool already uses id %q", tool.ID)
		}
		if sameToolHotkey(other.Hotkey, tool.Hotkey) || startsToolHotkey(other.Hotkey, tool.Hotkey) || startsToolHotkey(tool.Hotkey, other.Hotkey) {
			return fmt.Errorf("Hotkey conflicts with %q", other.ID)
		}
	}
	tools = append(tools, tool)
	if err := m.persistToolOverlays(tools); err != nil {
		return err
	}
	m.mode = modeTools
	return nil
}

func validToolID(id string) bool {
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, letter := range id[1:] {
		if !(letter >= 'a' && letter <= 'z' || letter >= '0' && letter <= '9' || letter == '_' || letter == '-') {
			return false
		}
	}
	return true
}

func validToolHotkey(hotkey []string) error {
	if len(hotkey) < 2 || len(hotkey) > 3 {
		return fmt.Errorf("hotkey must contain two or three lowercase letters")
	}
	for _, key := range hotkey {
		if len(key) != 1 || key[0] < 'a' || key[0] > 'z' {
			return fmt.Errorf("hotkey must contain two or three lowercase letters")
		}
	}
	if !strings.ContainsRune("abcdpuvwy", rune(hotkey[0][0])) {
		return fmt.Errorf("hotkey starts with a dashboard command")
	}
	return nil
}

func (m *Model) refreshToolValidation() {
	m.toolForm.validation = ""
	m.toolForm.validationField = m.toolValidationField()
	if m.toolForm.validationField >= 0 {
		m.toolForm.validation = m.validateToolForm().Error()
	}
}

func (m Model) toolValidationField() int {
	id := strings.TrimSpace(m.toolForm.id.Value())
	if !validToolID(id) {
		return 0
	}
	if strings.TrimSpace(m.toolForm.binary.Value()) == "" {
		return 2
	}
	args := strings.TrimSpace(m.toolForm.args.Value())
	if args != "" {
		var values []string
		if err := json.Unmarshal([]byte(args), &values); err != nil || values == nil {
			return 3
		}
	}
	hotkey := strings.Fields(strings.ToLower(m.toolForm.hotkey.Value()))
	if validToolHotkey(hotkey) != nil {
		return 4
	}
	host := strings.TrimSpace(m.toolForm.host.Value())
	if host != "" && !m.hasToolHost(host) {
		return 5
	}
	for index, other := range m.toolOverlays {
		if index == m.toolForm.editing {
			continue
		}
		if other.ID == id {
			return 0
		}
		if sameToolHotkey(other.Hotkey, hotkey) || startsToolHotkey(other.Hotkey, hotkey) || startsToolHotkey(hotkey, other.Hotkey) {
			return 4
		}
	}
	return -1
}

func (m Model) validateToolForm() error {
	id := strings.TrimSpace(m.toolForm.id.Value())
	if !validToolID(id) {
		return fmt.Errorf("ID: start lowercase; use a-z, 0-9, _ or -")
	}
	if strings.TrimSpace(m.toolForm.binary.Value()) == "" {
		return fmt.Errorf("Executable is required")
	}
	args := strings.TrimSpace(m.toolForm.args.Value())
	if args != "" {
		var values []string
		if err := json.Unmarshal([]byte(args), &values); err != nil || values == nil {
			return fmt.Errorf("Arguments: use a JSON string array")
		}
	}
	hotkey := strings.Fields(strings.ToLower(m.toolForm.hotkey.Value()))
	if err := validToolHotkey(hotkey); err != nil {
		return err
	}
	host := strings.TrimSpace(m.toolForm.host.Value())
	if host != "" && !m.hasToolHost(host) {
		return fmt.Errorf("Host %q is not configured", host)
	}
	for index, other := range m.toolOverlays {
		if index == m.toolForm.editing {
			continue
		}
		if other.ID == id {
			return fmt.Errorf("ID %q is already in use", id)
		}
		if sameToolHotkey(other.Hotkey, hotkey) || startsToolHotkey(other.Hotkey, hotkey) || startsToolHotkey(hotkey, other.Hotkey) {
			return fmt.Errorf("hotkey conflicts with %q", other.ID)
		}
	}
	return nil
}

func (m Model) hasToolHost(host string) bool {
	for _, machine := range m.machines {
		if machine.kind == machineHost && machine.name == host {
			return true
		}
	}
	return false
}

func (m *Model) persistToolOverlays(tools []ToolOverlay) error {
	tools = sortToolOverlays(tools)
	if m.saveToolOverlays != nil {
		if err := m.saveToolOverlays(tools); err != nil {
			return err
		}
	}
	m.toolOverlays = tools
	m.toolCursor = min(m.toolCursor, max(0, len(tools)-1))
	return nil
}

func (m Model) renderToolsModal(width, height int) string {
	lines := []string{"  " + titleStyle().Render("Tool overlays"), ""}
	if len(m.toolOverlays) == 0 {
		lines = append(lines, "  "+mutedStyle().Render("No configured tools"))
	} else {
		for index, tool := range m.toolOverlays {
			prefix := "  "
			if index == m.toolCursor {
				prefix = accentStyle().Render("› ")
			}
			lines = append(lines, prefix+truncate(overlayTitle(tool), 28)+"  "+mutedStyle().Render(toolHotkeyLabel(tool.Hotkey)))
		}
	}
	lines = append(lines, "", "  "+mutedStyle().Render("Enter open  n add  e edit  d delete  Esc close"))
	modalWidth, modalHeight := modalDimensions(width, height, 60, len(lines)+2)
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) renderToolEditModal(width, height int) string {
	modalWidth, modalHeight := modalDimensions(width, height, 72, 20)
	inputWidth := max(12, modalWidth-24)
	labels := []string{"ID", "Title", "Executable", "Arguments", "Hotkey", "Host"}
	inputs := m.toolFormInputs()
	lines := []string{"  " + titleStyle().Render("Configure tool overlay"), ""}
	for index, label := range labels {
		inputs[index].SetWidth(inputWidth)
		invalid := m.toolForm.validation != "" &&
			m.toolForm.validationField == index &&
			(m.toolForm.submitted || m.toolForm.touched[index])
		ink := mutedStyle()
		if invalid {
			ink = errorStyle().Bold(true)
		} else if m.toolForm.focus == index {
			ink = accentStyle()
		}
		lines = append(lines, "  "+ink.Render(label)+"  "+inputs[index].View())
		if invalid {
			lines = append(lines, "    "+errorStyle().Render("! "+truncate(m.toolForm.validation, max(1, modalWidth-8))))
		} else if m.toolForm.focus == index {
			help := truncate(m.toolFormHelp(), max(1, modalWidth-6))
			lines = append(lines, "    "+mutedStyle().Render(help))
		}
	}
	lines = append(lines, "", "  "+mutedStyle().Render("Enter save  Tab fields  Esc cancel"))
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) toolFormHelp() string {
	switch m.toolForm.focus {
	case 0:
		return "Lowercase config ID; letters, digits, _ and - only."
	case 1:
		return "Optional label shown in the overlay list and help."
	case 2:
		return "Command to run on this host; an absolute path is most reliable."
	case 3:
		return "Optional JSON string array passed after the executable."
	case 4:
		return "Two or three unused lowercase dashboard keys, for example c r."
	case 5:
		return "Blank runs here. A configured host runs the command on that machine."
	default:
		return ""
	}
}
