package ui

// Delete confirmation, rename, info, and help overlays.
// Split from model.go; see #34.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/trentkm/stormlight/internal/workspace"
)

func (m Model) updateDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "x", "ctrl+x", "y", "enter":
		if m.activePane == paneWorkspaces {
			selected, ok := m.selectedWorkspace()
			if !ok {
				m.mode = modeNormal
				return m, nil
			}
			if count := len(m.groups[m.workspaceCursor].agents); count > 0 {
				// Deleting agents needs the deliberate keystroke; stay in
				// the confirmation and say so.
				m.status = fmt.Sprintf(
					"%s has %d agent(s) — press X to delete everything",
					m.selectedWorkspaceLabel(),
					count,
				)
				return m, nil
			}
			m.mode = modeNormal
			m.status = "Removing " + selected.Name
			return m, actionCmd("Workspace removed", func(ctx context.Context) error {
				return m.backend.RemoveWorkspace(ctx, selected)
			})
		}
		m.mode = modeNormal
		selected, ok := m.selectedAgent()
		if !ok {
			return m, nil
		}
		m.status = "Deleting " + agentDisplayTitle(selected)
		return m, actionCmd("Agent deleted", func(ctx context.Context) error {
			return m.backend.Delete(ctx, selected.ID)
		})
	case "X":
		if m.activePane != paneWorkspaces {
			return m, nil
		}
		selected, ok := m.selectedWorkspace()
		if !ok {
			m.mode = modeNormal
			return m, nil
		}
		doomed := slices.Clone(m.groups[m.workspaceCursor].agents)
		label := m.selectedWorkspaceLabel()
		m.mode = modeNormal
		m.status = fmt.Sprintf("Deleting %s and %d agent(s)", label, len(doomed))
		backend := m.backend
		return m, actionCmd("Workspace and agents deleted",
			func(ctx context.Context) error {
				var failures []error
				for _, condemned := range doomed {
					if err := backend.Delete(ctx, condemned.ID); err != nil {
						failures = append(failures, err)
					}
				}
				if err := backend.RemoveWorkspace(ctx, selected); err != nil {
					failures = append(failures, err)
				}
				return errors.Join(failures...)
			})
	case "n", "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.status = "Ready"
	}
	return m, nil
}

func (m Model) renderInfoModal(width, height int) string {
	selected, ok := m.selectedWorkspace()
	if !ok {
		return ""
	}
	rows := [][2]string{
		{"Name", m.selectedWorkspaceLabel()},
		{"Kind", strings.ToUpper(selected.Kind)},
		{"ID", selected.ID},
		{"Root", selected.Root},
	}
	if selected.ExecutionRoot != "" && selected.ExecutionRoot != selected.Root {
		rows = append(rows, [2]string{"Execution root", selected.ExecutionRoot})
	}
	if selected.ComponentName != "" {
		rows = append(rows, [2]string{"Component", selected.ComponentName})
		if selected.ComponentRoot != "" &&
			selected.ComponentRoot != selected.ExecutionRoot {
			rows = append(rows, [2]string{"Component root", selected.ComponentRoot})
		}
	}
	keys := make([]string, 0, len(selected.Metadata))
	for key := range selected.Metadata {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		rows = append(rows, [2]string{key, selected.Metadata[key]})
	}
	rows = append(rows, [2]string{
		"Agents", fmt.Sprintf("%d", len(m.agentsForSelectedWorkspace())),
	})

	modalWidth, modalHeight := modalDimensions(width, height, 72, len(rows)+6)
	innerWidth := max(1, modalWidth-2)
	lines := []string{
		"  " + titleStyle.Render("Workspace · "+m.selectedWorkspaceLabel()),
		"",
	}
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, lipgloss.Width(row[0]))
	}
	for _, row := range rows {
		pad := strings.Repeat(" ", labelWidth-lipgloss.Width(row[0]))
		value := truncate(row[1], max(1, innerWidth-labelWidth-6))
		lines = append(lines,
			"  "+mutedStyle.Render(row[0])+pad+"  "+value,
		)
	}
	lines = append(lines, "", "  "+mutedStyle.Render("any key closes"))
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) renderHelpModal(width, height int) string {
	sections := []struct {
		title string
		keys  [][2]string
	}{
		{"Navigate", [][2]string{
			{"h/l", "move between panes"},
			{"j/k", "move in the pane; scroll Spanreed"},
			{"gg / G", "first or last item"},
			{"Ctrl-d/u  Ctrl-f/b", "half or full page"},
			{"Enter", "into agents; open agent terminal"},
			{"Ctrl-6", "return from an agent to the dashboard"},
			{"prefix Q", "return (tmux-native fallback)"},
		}},
		{"Act", [][2]string{
			{"n", "new agent (or add workspace)"},
			{"o", "new agent with directory picker"},
			{"i / s", "write a reply in Spanreed"},
			{"/ then n/N", "search the Spanreed transcript"},
			{"drag", "select transcript lines; release copies"},
			{"Ctrl-v", "paste clipboard image into the reply"},
			{"x", "interrupt the selected agent"},
			{"Ctrl-x then x/X", "delete agent / workspace (+agents)"},
			{"R", "rename workspace or agent"},
			{"M", "mark agent or workspace seen"},
			{"K", "workspace info"},
		}},
		{"View", [][2]string{
			{", then a/n/c", "sort: attention, name, newest"},
			{"< / >", "narrow or widen the focused pane"},
			{"z", "toggle compact and expanded rows"},
			{"r / Ctrl-l", "refresh"},
			{"?", "this help"},
			{"q", "quit the dashboard"},
		}},
	}

	lines := []string{"  " + titleStyle.Render("Keys"), ""}
	for _, section := range sections {
		lines = append(lines, "  "+accentStyle.Render(section.title))
		for _, key := range section.keys {
			pad := max(1, 20-lipgloss.Width(key[0]))
			lines = append(lines,
				"    "+titleStyle.Render(key[0])+
					strings.Repeat(" ", pad)+
					mutedStyle.Render(key[1]),
			)
		}
		lines = append(lines, "")
	}
	lines = append(lines, "  "+mutedStyle.Render("any key closes"))

	modalWidth, modalHeight := modalDimensions(width, height, 64, len(lines)+2)
	return renderModal(strings.Join(lines, "\n"), modalWidth, modalHeight)
}

func (m Model) beginRename() (tea.Model, tea.Cmd) {
	m.renameAgentID = ""
	m.renameWorkspace = workspace.Context{}
	current := ""
	if m.activePane == paneWorkspaces {
		selected, ok := m.selectedWorkspace()
		if !ok {
			return m, nil
		}
		m.renameWorkspace = selected
		current = m.selectedWorkspaceLabel()
	} else {
		selected, ok := m.selectedAgent()
		if !ok {
			return m, nil
		}
		m.renameAgentID = selected.ID
		current = agentDisplayTitle(selected)
	}
	m.renameInput = newLineInput("New name")
	m.renameInput.SetValue(current)
	m.renameInput.Focus()
	m.mode = modeRename
	m.err = nil
	m.status = "Rename"
	return m, nil
}

func (m Model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.status = "Ready"
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.renameInput.Value())
		if name == "" {
			m.err = fmt.Errorf("name cannot be empty")
			return m, nil
		}
		m.mode = modeNormal
		backend := m.backend
		if m.renameAgentID != "" {
			id := m.renameAgentID
			m.status = "Renaming agent"
			return m, actionCmd("Agent renamed", func(ctx context.Context) error {
				return backend.Rename(ctx, id, name)
			})
		}
		target := m.renameWorkspace
		m.status = "Renaming workspace"
		return m, actionCmd("Workspace renamed", func(ctx context.Context) error {
			return backend.RenameWorkspace(ctx, target, name)
		})
	}
	m.renameInput = m.renameInput.Update(msg)
	return m, nil
}

func (m Model) renderRenameModal(width, height int) string {
	modalWidth, modalHeight := modalDimensions(width, height, 56, 7)
	innerWidth := max(1, modalWidth-2)
	title := "Rename workspace"
	if m.renameAgentID != "" {
		title = "Rename agent"
	}
	m.renameInput.SetWidth(max(10, innerWidth-6))
	content := strings.Join([]string{
		"  " + titleStyle.Render(title),
		"",
		"    " + m.renameInput.View(),
		"",
		"  " + mutedStyle.Render("Enter apply  Esc cancel"),
	}, "\n")
	return renderModal(content, modalWidth, modalHeight)
}
