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

	tea "charm.land/bubbletea/v2"
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
			statsA := agent.Count(a.agents)
			statsB := agent.Count(b.agents)
			if d := statsB.Urgent - statsA.Urgent; d != 0 {
				return d
			}
			return statsB.Waiting - statsA.Waiting
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
			if d := a.TriageRank() - b.TriageRank(); d != 0 {
				return d
			}
			return newestFirst(a, b)
		})
	default:
		slices.SortStableFunc(agents, newestFirst)
	}
}

// newestFirst orders by creation time; same-second ties keep the
// roster's own order, which the stable sort preserves.
func newestFirst(a, b agent.Agent) int {
	return b.CreatedAt.Compare(a.CreatedAt)
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
	return labelWorkspaceGroups(groups)
}

// labelWorkspaceGroups gives each group a name that tells it from the
// others on screen.
//
// Two workspaces can share a basename and be entirely different places: a
// catalogued parent directory and a Git repository nested inside it both
// end in MetaRepo-PFS, and the pane showed two rows spelled the same way
// with the agents apparently on the wrong one. They are genuinely
// different workspaces — the identities are right — so the fix is to say
// which is which rather than to merge them.
//
// A colliding name grows leftward along its own path until it is unique:
// workplace/MetaRepo-PFS against src/MetaRepo-PFS. Leftward because the
// pane truncates from the right, so the part that distinguishes them has
// to arrive first.
func labelWorkspaceGroups(groups []workspaceGroup) []workspaceGroup {
	for index := range groups {
		groups[index].label = groups[index].context.Name
	}
	for {
		counts := make(map[string]int, len(groups))
		for _, group := range groups {
			counts[group.label]++
		}
		grew := false
		for index := range groups {
			if counts[groups[index].label] < 2 {
				continue
			}
			if longer, ok := growLabel(
				groups[index].label, groups[index].context.Root,
			); ok {
				groups[index].label = longer
				grew = true
			}
		}
		// Names that cannot grow any further stay as they are: two rows
		// spelled alike beats a row spelled wrong.
		if !grew {
			return groups
		}
	}
}

// growLabel prepends the next path segment to the left of what the label
// already shows.
func growLabel(label, root string) (string, bool) {
	root = strings.TrimSuffix(filepath.Clean(root), "/")
	if !strings.HasSuffix(root, label) {
		// The label is not a tail of this path — a renamed workspace —
		// so there is no next segment to reach for.
		return "", false
	}
	remainder := strings.TrimSuffix(strings.TrimSuffix(root, label), "/")
	if remainder == "" || remainder == "/" {
		return "", false
	}
	return filepath.Base(remainder) + "/" + label, true
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

// checkoutBadge is the token naming the Git checkout an agent runs in, and
// whether that checkout is the workspace's own rather than a worktree hanging
// off it. An empty text means there is nothing to name.
type checkoutBadge struct {
	text    string
	primary bool
}

// agentCheckout names the execution root an agent runs in. Git gets its
// familiar checkout/worktree language; custom resolvers may provide an
// execution_root_label, with a generic root label as fallback.
func agentCheckout(managedAgent agent.Agent) checkoutBadge {
	value := effectiveWorkspace(managedAgent)
	if value.ExecutionRoot == "" {
		return checkoutBadge{}
	}
	primary := directoryKey(value.ExecutionRoot) == directoryKey(value.Root)
	if label := strings.TrimSpace(value.Metadata["execution_root_label"]); label != "" {
		return checkoutBadge{text: label, primary: primary}
	}
	if primary {
		if value.Kind == workspace.KindGit {
			return checkoutBadge{text: "main checkout", primary: true}
		}
		return checkoutBadge{}
	}
	if value.Kind == workspace.KindGit {
		return checkoutBadge{text: "worktree " + filepath.Base(value.ExecutionRoot)}
	}
	return checkoutBadge{text: "root " + filepath.Base(value.ExecutionRoot)}
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
		m.moveAgentSelection(delta)
	case paneInteraction:
		if m.ptyEnabled {
			// The PTY view scrolls the emulator's history; positive delta
			// is "down", which moves toward the live tail. Keyboard scroll
			// applies immediately — key repeat is slow; the wheel path
			// coalesces separately (see handleMouse).
			if widget, ok := m.selectedPTY(); ok {
				widget.ScrollBy(-delta)
			}
			return
		}
		if delta > 0 {
			m.interaction.ScrollDown(delta)
		} else {
			m.interaction.ScrollUp(-delta)
		}
	}
}

func (m *Model) moveAgentSelection(delta int) {
	direction := 1
	if delta < 0 {
		direction = -1
	}

	for delta != 0 {
		agents := m.agentsForSelectedWorkspace()
		if len(agents) > 0 {
			m.agentCursor = clamp(m.agentCursor, 0, len(agents)-1)
			nextAgent := m.agentCursor + direction
			if nextAgent >= 0 && nextAgent < len(agents) {
				m.agentCursor = nextAgent
				delta -= direction
				continue
			}
		}

		nextWorkspace := m.workspaceCursor + direction
		for nextWorkspace >= 0 && nextWorkspace < len(m.groups) &&
			len(m.groups[nextWorkspace].agents) == 0 {
			nextWorkspace += direction
		}
		if nextWorkspace < 0 || nextWorkspace >= len(m.groups) {
			return
		}

		m.workspaceCursor = nextWorkspace
		m.agentCursor = 0
		if direction < 0 {
			m.agentCursor = len(m.groups[nextWorkspace].agents) - 1
		}
		delta -= direction
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
		if m.ptyEnabled {
			if widget, ok := m.selectedPTY(); ok {
				widget.ScrollBy(1 << 30)
			}
			return
		}
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
		if m.ptyEnabled {
			if widget, ok := m.selectedPTY(); ok {
				widget.ScrollToBottom()
			}
			return
		}
		m.interaction.GotoBottom()
	}
}

// markAttentionSeen clears attention locally so the amber drops on the very
// next frame; the backend write follows asynchronously. A manual attention
// mark goes with it — seen is seen, whoever raised the flag.
func (m *Model) markAttentionSeen(ids ...string) {
	member := make(map[string]bool, len(ids))
	for _, id := range ids {
		member[id] = true
	}
	clear := func(managedAgent *agent.Agent) {
		managedAgent.Attention = agent.AttentionNone
		if managedAgent.Mark == agent.MarkAttention {
			managedAgent.Mark = agent.MarkNone
		}
	}
	for index := range m.agents {
		if member[m.agents[index].ID] {
			clear(&m.agents[index])
		}
	}
	for g := range m.groups {
		for index := range m.groups[g].agents {
			if member[m.groups[g].agents[index].ID] {
				clear(&m.groups[g].agents[index])
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
			return actionMsg{err: err}
		}
		// No actionMsg on success: seen-clearing is ambient, not an
		// action worth announcing or refreshing over.
		return nil
	}
}

// clearAttention handles the M hotkey: mark the selected agent — or every
// agent in the selected workspace — as seen, regardless of tier.
func (m Model) clearAttention() (tea.Model, tea.Cmd) {
	flagged := func(managedAgent agent.Agent) bool {
		return managedAgent.ProcessLive &&
			(managedAgent.Attention != agent.AttentionNone ||
				managedAgent.EffectiveMark() == agent.MarkAttention)
	}
	ids := []string{}
	if m.activePane == paneWorkspaces {
		for _, managedAgent := range m.agentsForSelectedWorkspace() {
			if flagged(managedAgent) {
				ids = append(ids, managedAgent.ID)
			}
		}
	} else if selected, ok := m.selectedAgent(); ok && flagged(selected) {
		ids = append(ids, selected.ID)
	}
	if len(ids) == 0 {
		return m, nil
	}
	m.markAttentionSeen(ids...)
	return m, clearAttentionCmd(m.backend, ids...)
}
