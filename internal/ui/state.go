package ui

// Selection state, group building, and attention bookkeeping.
// Split from model.go; see #34.

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

func (m Model) selectedWorkspace() (workspace.Context, bool) {
	if m.workspaceCursor < 0 || m.workspaceCursor >= len(m.groups) {
		return workspace.Context{}, false
	}
	return m.groups[m.workspaceCursor].context, true
}

func (m Model) selectedWorkspaceLabel() string {
	value, ok := m.selectedWorkspace()
	if !ok {
		return ""
	}
	if strings.TrimSpace(value.Name) != "" {
		return value.Name
	}
	return filepath.Base(value.Root)
}

func (m Model) selectedWorkspaceID() string {
	selected, ok := m.selectedWorkspace()
	if !ok {
		return ""
	}
	return selected.ID
}

func (m Model) agentsForSelectedWorkspace() []agent.Agent {
	if m.workspaceCursor < 0 || m.workspaceCursor >= len(m.groups) {
		return nil
	}
	return m.groups[m.workspaceCursor].agents
}

func (m Model) selectedAgent() (agent.Agent, bool) {
	agents := m.agentsForSelectedWorkspace()
	if m.agentCursor < 0 || m.agentCursor >= len(agents) {
		return agent.Agent{}, false
	}
	return agents[m.agentCursor], true
}

func (m Model) selectedAgentID() string {
	selected, ok := m.selectedAgent()
	if !ok {
		return ""
	}
	return selected.ID
}

func (m *Model) rebuildGroups(preferredWorkspaceID, preferredAgentID string) {
	previousWorkspaceCursor := m.workspaceCursor
	previousAgentCursor := m.agentCursor
	m.groups = buildWorkspaceGroups(m.catalogWorkspaces, m.agents)
	m.applySort()
	if len(m.groups) == 0 {
		m.workspaceCursor = 0
		m.agentCursor = 0
		return
	}
	m.workspaceCursor = clamp(previousWorkspaceCursor, 0, len(m.groups)-1)
	if preferredWorkspaceID != "" {
		for index := range m.groups {
			if m.groups[index].context.ID == preferredWorkspaceID {
				m.workspaceCursor = index
				break
			}
		}
	}
	agents := m.agentsForSelectedWorkspace()
	if len(agents) == 0 {
		m.agentCursor = 0
		return
	}
	m.agentCursor = clamp(previousAgentCursor, 0, len(agents)-1)
	if preferredAgentID != "" {
		for index := range agents {
			if agents[index].ID == preferredAgentID {
				m.agentCursor = index
				break
			}
		}
	}
}

// applySort orders groups and their agents by the user-chosen mode. The
// backend delivers agents attention-first; the UI re-sorts so nothing
// rearranges unless the user asked for it.
func (m *Model) applySort() {
	for index := range m.groups {
		sortAgentList(m.groups[index].agents, m.sortMode)
	}
	switch m.sortMode {
	case sortByName:
		slices.SortStableFunc(m.groups, func(a, b workspaceGroup) int {
			return strings.Compare(
				strings.ToLower(a.context.Name),
				strings.ToLower(b.context.Name),
			)
		})
	case sortByAttention:
		slices.SortStableFunc(m.groups, func(a, b workspaceGroup) int {
			_, urgentA, waitingA := workspaceStats(a.agents)
			_, urgentB, waitingB := workspaceStats(b.agents)
			if d := urgentB - urgentA; d != 0 {
				return d
			}
			return waitingB - waitingA
		})
	}
}

func sortAgentList(agents []agent.Agent, mode sortMode) {
	switch mode {
	case sortByName:
		slices.SortStableFunc(agents, func(a, b agent.Agent) int {
			return strings.Compare(
				strings.ToLower(agentDisplayTitle(a)),
				strings.ToLower(agentDisplayTitle(b)),
			)
		})
	case sortByAttention:
		slices.SortStableFunc(agents, func(a, b agent.Agent) int {
			if d := a.Attention.Rank() - b.Attention.Rank(); d != 0 {
				return d
			}
			return newestFirst(a, b)
		})
	default:
		slices.SortStableFunc(agents, newestFirst)
	}
}

// newestFirst orders by creation time; window index breaks same-second
// ties deterministically (later windows are newer within a session).
func newestFirst(a, b agent.Agent) int {
	if d := b.CreatedAt.Compare(a.CreatedAt); d != 0 {
		return d
	}
	return b.WindowIndex - a.WindowIndex
}

func buildWorkspaceGroups(
	catalog []workspace.Context,
	agents []agent.Agent,
) []workspaceGroup {
	groups := make([]workspaceGroup, 0, len(catalog))
	indexes := make(map[string]int, len(catalog))
	for _, value := range catalog {
		if value.ID == "" || indexes[value.ID] > 0 {
			continue
		}
		indexes[value.ID] = len(groups) + 1
		groups = append(groups, workspaceGroup{context: value})
	}
	for _, managedAgent := range agents {
		value := effectiveWorkspace(managedAgent)
		position := indexes[value.ID]
		if position == 0 {
			groups = append(groups, workspaceGroup{context: value})
			position = len(groups)
			indexes[value.ID] = position
		}
		index := position - 1
		groups[index].agents = append(groups[index].agents, managedAgent)
	}
	return groups
}

func appendWorkspace(values []workspace.Context, value workspace.Context) []workspace.Context {
	for index := range values {
		if values[index].ID == value.ID {
			values[index] = value
			return values
		}
	}
	return append(values, value)
}

func effectiveWorkspace(managedAgent agent.Agent) workspace.Context {
	value := managedAgent.Workspace
	path := filepath.Clean(managedAgent.Cwd)
	if managedAgent.Cwd == "" {
		path = ""
	}
	if value.ID == "" {
		if path == "" {
			return workspace.Context{
				ID:            "directory:unresolved",
				Kind:          workspace.KindDirectory,
				Name:          "Unresolved",
				Root:          "",
				ExecutionRoot: "",
			}
		}
		return workspace.DirectoryContext(path)
	}
	if value.Kind == "" {
		value.Kind = workspace.KindDirectory
	}
	if value.Root == "" {
		value.Root = path
	}
	if value.ExecutionRoot == "" {
		value.ExecutionRoot = value.Root
	}
	if value.Name == "" {
		value.Name = filepath.Base(value.Root)
	}
	return value
}

// worktreeLabel names the linked Git worktree an agent runs in, and is empty
// when the agent works the main checkout or isn't in Git at all. Linked
// worktrees share a --git-common-dir with their checkout, so the resolver
// files them under one workspace: Root is the checkout every worktree hangs
// off, ExecutionRoot the tree this agent actually edits.
func worktreeLabel(managedAgent agent.Agent) string {
	value := effectiveWorkspace(managedAgent)
	if value.Kind != workspace.KindGit || value.ExecutionRoot == "" {
		return ""
	}
	if directoryKey(value.ExecutionRoot) == directoryKey(value.Root) {
		return ""
	}
	return filepath.Base(value.ExecutionRoot)
}

func (m *Model) moveSelection(delta int) {
	m.moveSelectionIn(m.activePane, delta)
}

func (m *Model) moveSelectionIn(target pane, delta int) {
	switch target {
	case paneWorkspaces:
		if len(m.groups) == 0 {
			return
		}
		m.workspaceCursor = clamp(m.workspaceCursor+delta, 0, len(m.groups)-1)
		m.agentCursor = 0
	case paneAgents:
		agents := m.agentsForSelectedWorkspace()
		if len(agents) == 0 {
			return
		}
		m.agentCursor = clamp(m.agentCursor+delta, 0, len(agents)-1)
	case paneInteraction:
		if delta > 0 {
			m.interaction.LineDown(delta)
		} else {
			m.interaction.LineUp(-delta)
		}
	}
}

func (m *Model) moveSelectionToStart() {
	switch m.activePane {
	case paneWorkspaces:
		m.workspaceCursor = 0
		m.agentCursor = 0
	case paneAgents:
		m.agentCursor = 0
	case paneInteraction:
		m.interaction.GotoTop()
	}
}

func (m *Model) moveSelectionToEnd() {
	switch m.activePane {
	case paneWorkspaces:
		if len(m.groups) > 0 {
			m.workspaceCursor = len(m.groups) - 1
			m.agentCursor = 0
		}
	case paneAgents:
		agents := m.agentsForSelectedWorkspace()
		if len(agents) > 0 {
			m.agentCursor = len(agents) - 1
		}
	case paneInteraction:
		m.interaction.GotoBottom()
	}
}

// markAttentionSeen clears attention locally so the amber drops on the very
// next frame; the backend write follows asynchronously.
func (m *Model) markAttentionSeen(ids ...string) {
	member := make(map[string]bool, len(ids))
	for _, id := range ids {
		member[id] = true
	}
	for index := range m.agents {
		if member[m.agents[index].ID] {
			m.agents[index].Attention = agent.AttentionNone
		}
	}
	for g := range m.groups {
		for index := range m.groups[g].agents {
			if member[m.groups[g].agents[index].ID] {
				m.groups[g].agents[index].Attention = agent.AttentionNone
			}
		}
	}
}

func clearAttentionCmd(backend Backend, ids ...string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var failures []error
		for _, id := range ids {
			if err := backend.ClearAttention(ctx, id); err != nil {
				failures = append(failures, err)
			}
		}
		if err := errors.Join(failures...); err != nil {
			return actionMsg{status: "Attention cleared", err: err}
		}
		// No actionMsg on success: seen-clearing is ambient, not an
		// action worth announcing or refreshing over.
		return nil
	}
}

// clearAttention handles the M hotkey: mark the selected agent — or every
// agent in the selected workspace — as seen, regardless of tier.
func (m Model) clearAttention() (tea.Model, tea.Cmd) {
	ids := []string{}
	if m.activePane == paneWorkspaces {
		for _, managedAgent := range m.agentsForSelectedWorkspace() {
			if managedAgent.ProcessLive &&
				managedAgent.Attention != agent.AttentionNone {
				ids = append(ids, managedAgent.ID)
			}
		}
	} else if selected, ok := m.selectedAgent(); ok &&
		selected.ProcessLive && selected.Attention != agent.AttentionNone {
		ids = append(ids, selected.ID)
	}
	if len(ids) == 0 {
		return m, nil
	}
	m.markAttentionSeen(ids...)
	m.status = "Marked seen"
	return m, clearAttentionCmd(m.backend, ids...)
}
