package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/runstead/internal/agent"
	"github.com/trentkm/runstead/internal/app"
	"github.com/trentkm/runstead/internal/diagnostic"
	"github.com/trentkm/runstead/internal/pending"
	"github.com/trentkm/runstead/internal/provider"
	"github.com/trentkm/runstead/internal/workspace"
)

type Backend interface {
	ListAgents(context.Context) ([]agent.Agent, error)
	ListWorkspaces(context.Context) ([]workspace.Context, error)
	AddWorkspace(context.Context, string) (workspace.Context, error)
	RemoveWorkspace(context.Context, workspace.Context) error
	ListPendingActions(context.Context) ([]pending.Action, error)
	ResolvePendingAction(context.Context, string, string) error
	Dispatch(context.Context, app.DispatchRequest) (agent.Agent, error)
	Capture(context.Context, string, int) (string, error)
	Attach(context.Context, string) (app.AttachResult, error)
	Send(context.Context, string, string) error
	Interrupt(context.Context, string) error
	Delete(context.Context, string) error
	Providers() []provider.Info
}

type mode int

const (
	modeNormal mode = iota
	modeDispatch
	modeCompose
	modeDelete
	modeAddWorkspace
)

type pane int

const (
	paneWorkspaces pane = iota
	paneAgents
	paneInteraction
)

type workspaceGroup struct {
	context workspace.Context
	agents  []agent.Agent
}

type directoryChoiceKind int

const (
	directoryPath directoryChoiceKind = iota
	directoryYazi
	directoryCustom
)

type directoryChoice struct {
	kind          directoryChoiceKind
	label         string
	path          string
	workspaceKind string
}

type dispatchFocus int

const (
	dispatchProvider dispatchFocus = iota
	dispatchDirectory
	dispatchCustomPath
	dispatchTask
)

type Model struct {
	backend Backend

	agents            []agent.Agent
	catalogWorkspaces []workspace.Context
	groups            []workspaceGroup
	workspaceCursor   int
	agentCursor       int
	activePane        pane
	rowsExpanded      bool
	interaction       viewport.Model
	width             int
	height            int
	ready             bool

	mode                    mode
	formFocus               dispatchFocus
	providers               []provider.Info
	providerIndex           int
	cwdInput                lineInput
	taskInput               lineInput
	sendInput               lineInput
	initialCwd              string
	directories             []directoryChoice
	directoryIndex          int
	yaziPath                string
	pickerStart             string
	chooseDispatchDirectory bool

	interactionID  string
	status         string
	err            error
	pendingActions []pending.Action
	pendingOption  int
	pulseOn        bool

	normalPrefix   string
	dispatchPrefix string
}

type dashboardMsg struct {
	agents     []agent.Agent
	workspaces []workspace.Context
	actions    []pending.Action
	err        error
}

type interactionMsg struct {
	id      string
	content string
	err     error
}

type actionMsg struct {
	status string
	err    error
}

type pendingResolvedMsg struct {
	actionID string
	optionID string
	agentID  string
	name     string
	terminal bool
	err      error
}

type attachMsg struct {
	result app.AttachResult
	name   string
	err    error
}

type directoryPickedMsg struct {
	path string
	err  error
}

type workspaceAddedMsg struct {
	value workspace.Context
	err   error
}

type tickMsg time.Time
type pulseTickMsg time.Time

var (
	colorAccent       = lipgloss.Color("#62AEEF")
	colorText         = lipgloss.AdaptiveColor{Light: "#24323A", Dark: "#D7DEE5"}
	colorMuted        = lipgloss.AdaptiveColor{Light: "#70808A", Dark: "#74818B"}
	colorWorking      = lipgloss.AdaptiveColor{Light: "#26799D", Dark: "#61AFEF"}
	colorWaiting      = lipgloss.AdaptiveColor{Light: "#A86600", Dark: "#E5C07B"}
	colorDone         = lipgloss.AdaptiveColor{Light: "#257A4A", Dark: "#72C087"}
	colorFailed       = lipgloss.AdaptiveColor{Light: "#B33838", Dark: "#E06C75"}
	colorBorder       = lipgloss.AdaptiveColor{Light: "#AAB3B9", Dark: "#59636B"}
	colorSelect       = lipgloss.AdaptiveColor{Light: "#E1E4E6", Dark: "#3D4245"}
	colorSelectedText = lipgloss.AdaptiveColor{Light: "#172027", Dark: "#F3F5F6"}

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	mutedStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle  = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	errorStyle   = lipgloss.NewStyle().Foreground(colorFailed)
	successStyle = lipgloss.NewStyle().Foreground(colorDone)
)

func NewModel(backend Backend) Model {
	cwd, _ := os.Getwd()
	yaziPath, _ := exec.LookPath("yazi")

	cwdInput := newLineInput("")
	cwdInput.SetValue(cwd)

	taskInput := newLineInput("What should the agent do?")

	sendInput := newLineInput("Reply to the selected agent")

	model := Model{
		backend:      backend,
		providers:    backend.Providers(),
		cwdInput:     cwdInput,
		taskInput:    taskInput,
		sendInput:    sendInput,
		initialCwd:   cwd,
		yaziPath:     yaziPath,
		activePane:   paneWorkspaces,
		rowsExpanded: true,
		status:       "Ready",
	}
	model.prepareDirectoryChoices(cwd)
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tickCmd(), pulseTickCmd())
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		interactionWidth, contentHeight := m.interactionDimensions()
		if !m.ready {
			m.interaction = viewport.New(interactionWidth, contentHeight)
			m.ready = true
		} else {
			m.interaction.Width = interactionWidth
			m.interaction.Height = contentHeight
		}
		return m, m.loadInteractionCmd()

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tickCmd())

	case pulseTickMsg:
		m.pulseOn = !m.pulseOn
		return m, pulseTickCmd()

	case dashboardMsg:
		workspaceID := m.selectedWorkspaceID()
		agentID := m.selectedAgentID()
		pendingID := m.selectedPendingActionID()
		if msg.err != nil {
			m.err = msg.err
			diagnostic.Logger().Error("dashboard refresh failed", "error", msg.err)
		} else {
			m.agents = msg.agents
			m.catalogWorkspaces = msg.workspaces
			m.pendingActions = msg.actions
			m.rebuildGroups(workspaceID, agentID)
			if currentID := m.selectedPendingActionID(); currentID != pendingID {
				m.pendingOption = 0
			}
			m.clampPendingOption()
		}
		return m, m.loadInteractionCmd()

	case interactionMsg:
		if msg.id == m.selectedAgentID() {
			followOutput := m.interactionID != msg.id || m.interaction.AtBottom()
			previousOffset := m.interaction.YOffset
			m.interactionID = msg.id
			if msg.err != nil {
				m.interaction.SetContent(errorStyle.Render(msg.err.Error()))
			} else {
				selected, _ := m.selectedAgent()
				m.interaction.SetContent(cleanInteraction(
					msg.content,
					m.interaction.Width,
					selected.Provider,
				))
				if !followOutput {
					m.interaction.SetYOffset(previousOffset)
				}
			}
			if followOutput {
				m.interaction.GotoBottom()
			}
		}
		return m, nil

	case attachMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Action failed"
			diagnostic.Logger().Error("dashboard open failed",
				"agent", msg.name,
				"error", msg.err,
			)
			return m, nil
		}
		if msg.result.Command != nil {
			m.status = "Opening " + msg.name
			return m, tea.ExecProcess(msg.result.Command, func(err error) tea.Msg {
				if err != nil {
					diagnostic.Logger().Error("external tmux attach failed",
						"agent", msg.name,
						"error", err,
					)
				}
				return actionMsg{status: "Returned from " + msg.name, err: err}
			})
		}
		m.err = nil
		m.status = "Attached"
		return m, nil

	case actionMsg:
		m.err = msg.err
		if msg.err != nil {
			m.status = "Action failed"
			diagnostic.Logger().Error("dashboard action failed", "error", msg.err)
		} else {
			m.status = msg.status
		}
		return m, m.refreshCmd()

	case pendingResolvedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Action failed"
			diagnostic.Logger().Error(
				"pending action resolution failed",
				"action_id", msg.actionID,
				"option_id", msg.optionID,
				"error", msg.err,
			)
			return m, nil
		}
		m.removePendingAction(msg.actionID)
		m.pendingOption = 0
		if msg.terminal {
			m.status = "Opening " + msg.name
			return m, attachCmd(m.backend, msg.agentID, msg.name)
		}
		m.err = nil
		m.status = "Response sent"
		return m, tea.Batch(m.refreshCmd(), m.loadInteractionCmd())

	case directoryPickedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Action failed"
			diagnostic.Logger().Error("directory picker failed", "error", msg.err)
			return m, nil
		}
		if msg.path == "" {
			if m.mode == modeAddWorkspace {
				m.status = "Add workspace"
			} else {
				m.status = "New agent"
			}
			return m, nil
		}
		if m.mode == modeAddWorkspace {
			m.cwdInput.SetValue(msg.path)
			m.status = "Adding workspace"
			return m, addWorkspaceCmd(m.backend, msg.path)
		}
		m.prepareDirectoryChoices(msg.path)
		m.cwdInput.SetValue(msg.path)
		m.formFocus = dispatchTask
		m.focusForm()
		m.status = "Directory selected"
		return m, nil

	case workspaceAddedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Action failed"
			diagnostic.Logger().Error("add workspace failed", "error", msg.err)
			return m, nil
		}
		m.mode = modeNormal
		m.blurForm()
		m.catalogWorkspaces = appendWorkspace(m.catalogWorkspaces, msg.value)
		m.rebuildGroups(msg.value.ID, "")
		m.activePane = paneWorkspaces
		m.status = "Workspace added"
		return m, nil

	case tea.KeyMsg:
		if m.err != nil {
			m.err = nil
			if m.status == "Action failed" {
				m.status = "Ready"
			}
		}
		switch m.mode {
		case modeDispatch:
			return m.updateDispatch(msg)
		case modeCompose:
			return m.updateCompose(msg)
		case modeDelete:
			return m.updateDelete(msg)
		case modeAddWorkspace:
			return m.updateAddWorkspace(msg)
		default:
			return m.updateNormal(msg)
		}
	}

	if m.mode == modeNormal && m.ready && m.activePane == paneInteraction {
		var cmd tea.Cmd
		m.interaction, cmd = m.interaction.Update(message)
		return m, cmd
	}
	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "\n  Loading..."
	}

	header := m.renderHeader()
	body := m.renderBody()
	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.normalPrefix == "g" {
		m.normalPrefix = ""
		if key == "g" {
			if m.activePane == paneInteraction {
				if _, ok := m.selectedPendingAction(); ok {
					m.pendingOption = 0
					return m, nil
				}
			}
			m.moveSelectionToStart()
			return m, m.loadInteractionCmd()
		}
		return m, nil
	}

	if m.activePane == paneInteraction {
		if action, ok := m.selectedPendingAction(); ok {
			if updated, cmd, handled := m.updatePendingAction(key, action); handled {
				return updated, cmd
			}
		}
	}

	switch key {
	case "g":
		m.normalPrefix = "g"
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.moveSelection(1)
		return m, m.loadInteractionCmd()
	case "k", "up":
		m.moveSelection(-1)
		return m, m.loadInteractionCmd()
	case "home":
		m.moveSelectionToStart()
		return m, m.loadInteractionCmd()
	case "G", "end":
		m.moveSelectionToEnd()
		return m, m.loadInteractionCmd()
	case "ctrl+d":
		m.moveSelection(max(1, m.visibleRows()/2))
		return m, m.loadInteractionCmd()
	case "ctrl+u":
		m.moveSelection(-max(1, m.visibleRows()/2))
		return m, m.loadInteractionCmd()
	case "pgdown", "ctrl+f":
		m.moveSelection(m.visibleRows())
		return m, m.loadInteractionCmd()
	case "pgup", "ctrl+b":
		m.moveSelection(-m.visibleRows())
		return m, m.loadInteractionCmd()
	case "h", "left":
		if m.activePane > paneWorkspaces {
			m.activePane--
		}
		return m, nil
	case "l", "right":
		if m.activePane < paneInteraction {
			m.activePane++
		}
		return m, m.loadInteractionCmd()
	case "tab":
		m.activePane = (m.activePane + 1) % 3
		return m, m.loadInteractionCmd()
	case "shift+tab":
		m.activePane = (m.activePane + 2) % 3
		return m, m.loadInteractionCmd()
	case "z":
		if m.width >= 72 {
			m.rowsExpanded = !m.rowsExpanded
		}
		return m, nil
	case "enter":
		if m.activePane == paneWorkspaces {
			m.activePane = paneAgents
			return m, m.loadInteractionCmd()
		}
		if selected, ok := m.selectedAgent(); ok {
			displayTitle := agentDisplayTitle(selected)
			m.status = "Opening " + displayTitle
			return m, attachCmd(m.backend, selected.ID, displayTitle)
		}
	case "n":
		if m.activePane == paneWorkspaces {
			return m.beginAddWorkspace()
		}
		return m.beginDispatch(false)
	case "o":
		return m.beginDispatch(true)
	case "i", "s":
		if _, ok := m.selectedAgent(); ok {
			m.activePane = paneInteraction
			m.mode = modeCompose
			m.sendInput.SetValue("")
			m.sendInput.Focus()
			m.err = nil
			return m, nil
		}
	case "x":
		if selected, ok := m.selectedAgent(); ok {
			m.status = "Interrupting " + agentDisplayTitle(selected)
			return m, actionCmd("Interrupted", func(ctx context.Context) error {
				return m.backend.Interrupt(ctx, selected.ID)
			})
		}
	case "d":
		if m.activePane == paneWorkspaces {
			if _, ok := m.selectedWorkspace(); ok {
				m.mode = modeDelete
				m.err = nil
			}
			return m, nil
		}
		if _, ok := m.selectedAgent(); ok {
			m.mode = modeDelete
			m.err = nil
			return m, nil
		}
	case "r", "ctrl+l":
		m.status = "Refreshing"
		return m, tea.Batch(m.refreshCmd(), m.loadInteractionCmd())
	}

	if m.activePane != paneInteraction {
		return m, nil
	}
	var cmd tea.Cmd
	m.interaction, cmd = m.interaction.Update(msg)
	return m, cmd
}

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
		if m.formFocus == dispatchDirectory {
			m.editSelectedDirectory()
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
			default:
				m.formFocus = dispatchTask
			}
			m.focusForm()
			return m, nil
		case dispatchCustomPath:
			path := strings.TrimSpace(m.cwdInput.Value())
			if !isDirectory(path) {
				m.err = fmt.Errorf("working directory is unavailable: %s", path)
				return m, nil
			}
			m.formFocus = dispatchTask
			m.focusForm()
			return m, nil
		case dispatchTask:
			return m.submitDispatch()
		}
	}

	switch m.formFocus {
	case dispatchCustomPath:
		m.cwdInput = m.cwdInput.Update(msg)
	case dispatchTask:
		m.taskInput = m.taskInput.Update(msg)
	}
	return m, nil
}

func (m Model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.sendInput.Blur()
		m.status = "Ready"
		return m, nil
	case "enter":
		selected, ok := m.selectedAgent()
		if !ok {
			m.mode = modeNormal
			return m, nil
		}
		text := strings.TrimSpace(m.sendInput.Value())
		if text == "" {
			m.err = fmt.Errorf("message cannot be empty")
			return m, nil
		}
		m.mode = modeNormal
		m.sendInput.Blur()
		m.status = "Sending to " + selected.Name
		return m, actionCmd("Message sent", func(ctx context.Context) error {
			return m.backend.Send(ctx, selected.ID, text)
		})
	}
	m.sendInput = m.sendInput.Update(msg)
	return m, nil
}

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
		if m.formFocus == dispatchCustomPath {
			return m.submitAddWorkspace(m.cwdInput.Value())
		}
		selected, ok := m.selectedDirectory()
		if !ok {
			return m, nil
		}
		switch selected.kind {
		case directoryYazi:
			return m.openYazi()
		case directoryCustom:
			m.formFocus = dispatchCustomPath
			m.focusForm()
			return m, nil
		default:
			return m.submitAddWorkspace(selected.path)
		}
	}
	if m.formFocus == dispatchCustomPath {
		m.cwdInput = m.cwdInput.Update(msg)
	}
	return m, nil
}

func (m Model) updateDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "d", "y", "enter":
		m.mode = modeNormal
		if m.activePane == paneWorkspaces {
			selected, ok := m.selectedWorkspace()
			if !ok {
				return m, nil
			}
			m.status = "Removing " + selected.Name
			return m, actionCmd("Workspace removed", func(ctx context.Context) error {
				return m.backend.RemoveWorkspace(ctx, selected)
			})
		}
		selected, ok := m.selectedAgent()
		if !ok {
			return m, nil
		}
		m.status = "Deleting " + agentDisplayTitle(selected)
		return m, actionCmd("Agent deleted", func(ctx context.Context) error {
			return m.backend.Delete(ctx, selected.ID)
		})
	case "n", "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.status = "Ready"
	}
	return m, nil
}

func (m Model) renderHeader() string {
	width := max(1, m.width-1)
	working := 0
	attention := 0
	for _, managedAgent := range m.agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			working++
		}
		if managedAgent.NeedsAttention() {
			attention++
		}
	}
	left := titleStyle.Render(" Runstead")
	right := mutedStyle.Render(fmt.Sprintf("%d active", working))
	if attention > 0 {
		attentionLabel := fmt.Sprintf("%d need input", attention)
		if attention == 1 {
			attentionLabel = "1 needs input"
		}
		right += "  " + lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).
			Render(attentionLabel)
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) renderBody() string {
	contentHeight := max(1, m.height-3)
	width := max(1, m.width-1)
	dashboard := m.renderDashboardBody(width, contentHeight)
	switch m.mode {
	case modeDispatch:
		return overlayCentered(
			dashboard,
			m.renderDispatchModal(width, contentHeight),
			width,
			contentHeight,
		)
	case modeAddWorkspace:
		return overlayCentered(
			dashboard,
			m.renderAddWorkspaceModal(width, contentHeight),
			width,
			contentHeight,
		)
	}
	return dashboard
}

func (m Model) renderDashboardBody(width, contentHeight int) string {
	if width < 72 {
		return m.renderFocusedPane(width, contentHeight)
	}

	workspaceWidth := clamp(width*24/100, 18, 30)
	agentWidth := clamp(width*33/100, 26, 44)
	interactionWidth := width - workspaceWidth - agentWidth
	if interactionWidth < 24 {
		deficit := 24 - interactionWidth
		agentWidth = max(22, agentWidth-deficit)
		interactionWidth = width - workspaceWidth - agentWidth
	}

	workspaces := m.renderPane(
		"Workspaces",
		"",
		m.renderWorkspaces(max(1, workspaceWidth-2), contentHeight-1),
		workspaceWidth,
		contentHeight,
		m.activePane == paneWorkspaces,
		true,
	)
	agents := m.renderPane(
		"Agents",
		m.selectedWorkspaceLabel(),
		m.renderAgents(max(1, agentWidth-2), contentHeight-1),
		agentWidth,
		contentHeight,
		m.activePane == paneAgents,
		true,
	)
	interaction := m.renderPane(
		"Interaction",
		"",
		m.renderInteraction(
			max(1, interactionWidth-2),
			contentHeight-1,
		),
		interactionWidth,
		contentHeight,
		m.activePane == paneInteraction,
		false,
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, workspaces, agents, interaction)
}

func (m Model) renderDispatchModal(width, height int) string {
	preferredWidth := 62
	preferredHeight := 10
	if m.chooseDispatchDirectory {
		preferredWidth = 78
		preferredHeight = 20
	}
	modalWidth, modalHeight := modalDimensions(
		width,
		height,
		preferredWidth,
		preferredHeight,
	)
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

func modalDimensions(
	availableWidth int,
	availableHeight int,
	preferredWidth int,
	preferredHeight int,
) (int, int) {
	widthMargin := 4
	heightMargin := 2
	if availableWidth < 28 {
		widthMargin = 0
	}
	if availableHeight < 12 {
		heightMargin = 0
	}
	width := min(preferredWidth, max(1, availableWidth-widthMargin))
	height := min(preferredHeight, max(1, availableHeight-heightMargin))
	return width, height
}

func renderModal(content string, width, height int) string {
	if width < 3 || height < 3 {
		return fitBlock(content, width, height)
	}
	innerWidth := width - 2
	innerHeight := height - 2
	content = fitBlock(content, innerWidth, innerHeight)
	return lipgloss.NewStyle().
		Width(innerWidth).
		Height(innerHeight).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorWaiting).
		Render(content)
}

func overlayCentered(background, foreground string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	backgroundLines := blockLines(background, width, height)
	foregroundLines := strings.Split(foreground, "\n")
	if len(foregroundLines) > height {
		foregroundLines = foregroundLines[:height]
	}
	foregroundWidth := 0
	for _, line := range foregroundLines {
		foregroundWidth = max(foregroundWidth, ansi.StringWidth(line))
	}
	foregroundWidth = min(foregroundWidth, width)
	if foregroundWidth == 0 || len(foregroundLines) == 0 {
		return strings.Join(backgroundLines, "\n")
	}

	left := max(0, (width-foregroundWidth)/2)
	top := max(0, (height-len(foregroundLines))/2)
	for index, foregroundLine := range foregroundLines {
		row := top + index
		if row >= len(backgroundLines) {
			break
		}
		foregroundLine = fitLine(foregroundLine, foregroundWidth)
		backgroundLine := backgroundLines[row]
		before := ansi.Cut(backgroundLine, 0, left)
		rightStart := left + foregroundWidth
		after := ansi.Cut(backgroundLine, rightStart, width)
		backgroundLines[row] = fitLine(before, left) +
			ansi.ResetStyle +
			foregroundLine +
			ansi.ResetStyle +
			sgrStateAt(backgroundLine, rightStart) +
			fitLine(after, width-rightStart)
	}
	return strings.Join(backgroundLines, "\n")
}

func fitBlock(content string, width, height int) string {
	return strings.Join(blockLines(content, width, height), "\n")
}

func blockLines(content string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return lines
}

func fitLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "")
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func sgrStateAt(value string, column int) string {
	if column <= 0 {
		return ""
	}
	var result strings.Builder
	var state byte
	width := 0
	for len(value) > 0 && width < column {
		sequence, cellWidth, consumed, nextState := ansi.DecodeSequence(
			value,
			state,
			nil,
		)
		if consumed <= 0 {
			break
		}
		if isSGRSequence(sequence) {
			result.WriteString(sequence)
		}
		width += cellWidth
		value = value[consumed:]
		state = nextState
	}
	return result.String()
}

func (m Model) renderFocusedPane(width, height int) string {
	switch m.activePane {
	case paneAgents:
		contextLabel := m.selectedWorkspaceLabel()
		if width < 72 {
			contextLabel = strings.TrimSpace(contextLabel + "  ›")
		}
		return m.renderPane(
			"Agents",
			contextLabel,
			m.renderAgents(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
		)
	case paneInteraction:
		return m.renderPane(
			"Interaction",
			"‹",
			m.renderInteraction(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
		)
	default:
		return m.renderPane(
			"Workspaces",
			"Agents ›",
			m.renderWorkspaces(max(1, width-2), height-1),
			width,
			height,
			true,
			false,
		)
	}
}

func (m Model) renderPane(
	label string,
	contextLabel string,
	content string,
	width int,
	height int,
	active bool,
	borderRight bool,
) string {
	innerWidth := max(1, width)
	style := lipgloss.NewStyle().Width(innerWidth).Height(height).MaxHeight(height)
	if borderRight {
		innerWidth = max(1, width-1)
		style = style.Width(innerWidth).
			BorderStyle(lipgloss.NormalBorder()).
			BorderRight(true).
			BorderForeground(colorBorder)
		if active {
			style = style.BorderForeground(colorAccent)
		}
	}
	header := renderPaneHeader(label, contextLabel, innerWidth, active)
	body := lipgloss.NewStyle().
		Width(innerWidth).
		MaxWidth(innerWidth).
		Render(content)
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

func renderPaneHeader(label, contextLabel string, width int, active bool) string {
	left := mutedStyle.Copy().
		Bold(true).
		Render(truncate(" "+label, width))
	if active {
		rail := lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true).
			Render(ansi.Truncate(" ▌ ", width, ""))
		labelWidth := max(0, width-lipgloss.Width(rail))
		left = rail + titleStyle.Render(truncate(label, labelWidth))
	}

	remaining := width - lipgloss.Width(left) - 2
	if strings.TrimSpace(contextLabel) == "" || remaining < 4 {
		return lipgloss.NewStyle().Width(width).Render(left)
	}

	rightStyle := mutedStyle
	if strings.ContainsAny(contextLabel, "‹›") {
		rightStyle = accentStyle
	}
	right := rightStyle.Render(truncate(contextLabel, remaining))
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	return lipgloss.NewStyle().Width(width).Render(
		left + strings.Repeat(" ", gap) + right,
	)
}

func (m Model) renderWorkspaces(width, height int) string {
	if m.mode == modeDelete && m.activePane == paneWorkspaces {
		value, ok := m.selectedWorkspace()
		if !ok {
			return mutedStyle.Render("No workspace selected")
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			errorStyle.Render(truncate("Remove "+value.Name+"?", width)),
			"",
			mutedStyle.Render(ansi.Wrap(
				"This removes the workspace from Runstead. Running agents are unchanged.",
				width,
				"",
			)),
		)
	}
	if len(m.groups) == 0 {
		return "\n" + mutedStyle.Render(" No workspaces")
	}

	expanded := m.expandedRows()
	capacity := listRowCapacity(height, expanded)
	start, end := visibleRange(len(m.groups), m.workspaceCursor, capacity)
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, m.renderWorkspaceRow(
			m.groups[index],
			index == m.workspaceCursor,
			index == m.workspaceCursor && m.activePane == paneWorkspaces,
			width,
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
) string {
	active, attention := workspaceStats(group.agents)
	countLabel := fmt.Sprintf("%d agents", len(group.agents))
	if len(group.agents) == 1 {
		countLabel = "1 agent"
	}
	suffixes := []string{}
	if attention > 0 {
		suffixes = append(suffixes, fmt.Sprintf("%d input", attention))
	}
	if active > 0 {
		suffixes = append(suffixes, fmt.Sprintf("%d active", active))
	}
	suffixes = append(suffixes, countLabel)
	contentWidth := max(1, width-2)
	minimumNameWidth := min(10, max(1, contentWidth/2))
	maxSuffixWidth := max(
		1,
		contentWidth-lipgloss.Width("  ")-minimumNameWidth-1,
	)
	suffix := truncate(suffixes[len(suffixes)-1], maxSuffixWidth)
	for _, candidate := range suffixes {
		if lipgloss.Width(candidate) <= maxSuffixWidth {
			suffix = candidate
			break
		}
	}
	activityMarker := "  "
	if active > 0 {
		activityMarker = "· "
		if m.pulseOn {
			activityMarker = "● "
		}
	}
	nameWidth := max(
		1,
		contentWidth-lipgloss.Width(activityMarker)-lipgloss.Width(suffix)-1,
	)
	name := truncate(group.context.Name, nameWidth)
	gap := max(
		1,
		contentWidth-
			lipgloss.Width(activityMarker)-
			lipgloss.Width(name)-
			lipgloss.Width(suffix),
	)
	topContent := activityMarker + name + strings.Repeat(" ", gap) + suffix
	path, kind, detailGap := workspaceDetail(group.context, contentWidth)
	bottomContent := path + strings.Repeat(" ", detailGap) + kind
	if focused {
		if !m.expandedRows() {
			return renderSelectableRow(topContent, width, true)
		}
		return renderFocusedRow(topContent, bottomContent, width)
	}
	if selected {
		if !m.expandedRows() {
			return renderSelectableRow(topContent, width, false)
		}
		return renderContextRow(topContent, bottomContent, width)
	}

	marker := "  "
	activityStyle := mutedStyle
	nameStyle := titleStyle
	if active > 0 && m.pulseOn {
		activityStyle = lipgloss.NewStyle().
			Foreground(colorWorking).
			Bold(true)
		nameStyle = titleStyle.Copy().Foreground(colorWorking)
	}
	top := marker + activityStyle.Render(activityMarker) +
		nameStyle.Render(name) +
		strings.Repeat(" ", gap) +
		mutedStyle.Render(suffix)
	bottom := marker + mutedStyle.Render(path) +
		strings.Repeat(" ", detailGap) +
		mutedStyle.Render(kind)
	if !m.expandedRows() {
		return top
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

func workspaceDetail(value workspace.Context, width int) (string, string, int) {
	width = max(1, width)
	path := strings.TrimSpace(value.Root)
	if path != "" {
		path = truncatePathTail(path, width)
	}
	kind := strings.ToUpper(strings.TrimSpace(value.Kind))
	if kind == "" || width < 32 {
		return path, "", 0
	}

	kindWidth := lipgloss.Width(kind)
	pathWidth := max(1, width-kindWidth-2)
	path = truncatePathTail(value.Root, pathWidth)
	gap := max(2, width-lipgloss.Width(path)-kindWidth)
	return path, kind, gap
}

func (m Model) renderAgents(width, height int) string {
	agents := m.agentsForSelectedWorkspace()
	if len(agents) == 0 {
		return "\n" + mutedStyle.Render(" No agents")
	}
	expanded := m.expandedRows()
	capacity := listRowCapacity(height, expanded)
	start, end := visibleRange(len(agents), m.agentCursor, capacity)
	rows := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		rows = append(rows, renderAgentRowWithDensity(
			agents[index],
			index == m.agentCursor,
			index == m.agentCursor && m.activePane == paneAgents,
			width,
			expanded,
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
	)
}

func renderAgentRowWithDensity(
	managedAgent agent.Agent,
	selected bool,
	focused bool,
	width int,
	expanded bool,
) string {
	symbol, statusStyle := statusVisual(managedAgent)
	providerName := strings.ToUpper(string(managedAgent.Provider))
	if len(providerName) > 6 {
		providerName = providerName[:6]
	}
	age := timeAgo(managedAgent.CreatedAt)
	contentWidth := max(1, width-2)
	ageWidth := lipgloss.Width(age)
	titleWidth := max(1, contentWidth-2-ageWidth-1)
	displayTitle := truncate(agentDisplayTitle(managedAgent), titleWidth)
	gap := max(1, contentWidth-2-lipgloss.Width(displayTitle)-ageWidth)
	topContent := symbol + " " + displayTitle + strings.Repeat(" ", gap) + age

	state := string(managedAgent.Activity)
	if managedAgent.NeedsAttention() {
		state = "needs " + string(managedAgent.Attention)
	}
	details := []string{providerName, state}
	if location := agentLocation(managedAgent); location != "" {
		details = append(details, location)
	}
	bottomContent := truncate("  "+strings.Join(details, "  "), contentWidth)
	if focused {
		if !expanded {
			return renderSelectableRow(topContent, width, true)
		}
		return renderFocusedRow(topContent, bottomContent, width)
	}
	if selected {
		if !expanded {
			return renderSelectableRow(topContent, width, false)
		}
		return renderContextRow(topContent, bottomContent, width)
	}

	marker := "  "
	top := marker + statusStyle.Render(symbol) + " " +
		titleStyle.Render(displayTitle) +
		strings.Repeat(" ", gap) +
		mutedStyle.Render(age)
	bottom := marker + mutedStyle.Render(bottomContent)
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
	if width < 3 {
		style := lipgloss.NewStyle().
			Foreground(colorSelectedText).
			Background(colorSelect).
			Width(max(1, width))
		return lipgloss.JoinVertical(
			lipgloss.Left,
			style.Copy().Bold(true).Render(ansi.Truncate("▌"+top, width, "")),
			style.Render(ansi.Truncate("▌"+bottom, width, "")),
		)
	}

	contentWidth := width - 2
	markerStyle := lipgloss.NewStyle().
		Foreground(colorWaiting).
		Background(colorSelect).
		Bold(true)
	topStyle := lipgloss.NewStyle().
		Foreground(colorSelectedText).
		Background(colorSelect).
		Bold(true).
		Width(contentWidth).
		MaxWidth(contentWidth)
	bottomStyle := lipgloss.NewStyle().
		Foreground(colorSelectedText).
		Background(colorSelect).
		Width(contentWidth).
		MaxWidth(contentWidth)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		markerStyle.Render("▌ ")+topStyle.Render(top),
		markerStyle.Render("▌ ")+bottomStyle.Render(bottom),
	)
}

func renderContextRow(top, bottom string, width int) string {
	if width < 3 {
		style := lipgloss.NewStyle().
			Foreground(colorSelectedText).
			Background(colorSelect).
			Width(max(1, width))
		return lipgloss.JoinVertical(
			lipgloss.Left,
			style.Copy().Bold(true).Render(ansi.Truncate(top, width, "")),
			style.Render(ansi.Truncate(bottom, width, "")),
		)
	}

	contentWidth := width - 2
	markerStyle := lipgloss.NewStyle().
		Foreground(colorBorder).
		Background(colorSelect)
	topStyle := lipgloss.NewStyle().
		Foreground(colorSelectedText).
		Background(colorSelect).
		Bold(true).
		Width(contentWidth).
		MaxWidth(contentWidth)
	bottomStyle := lipgloss.NewStyle().
		Foreground(colorSelectedText).
		Background(colorSelect).
		Width(contentWidth).
		MaxWidth(contentWidth)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		markerStyle.Render("▏ ")+topStyle.Render(top),
		markerStyle.Render("▏ ")+bottomStyle.Render(bottom),
	)
}

func agentDisplayTitle(managedAgent agent.Agent) string {
	name := strings.TrimSpace(managedAgent.Name)
	task := strings.TrimSpace(managedAgent.Task)
	prefix := map[agent.Provider]string{
		agent.ProviderClaude: "cl-",
		agent.ProviderCodex:  "cx-",
		agent.ProviderShell:  "sh-",
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

func (m Model) renderInteraction(width, height int) string {
	managedAgent, ok := m.selectedAgent()
	if !ok {
		return mutedStyle.Render(truncate("No agent selected", width))
	}
	if m.mode == modeDelete && m.activePane != paneWorkspaces {
		return lipgloss.JoinVertical(lipgloss.Left,
			errorStyle.Render(truncate("Delete "+agentDisplayTitle(managedAgent)+"?", width)),
			"",
			mutedStyle.Render(ansi.Wrap(
				"The tmux window and any running process will be removed.",
				width,
				"",
			)),
		)
	}
	title := titleStyle.Render(truncate(agentDisplayTitle(managedAgent), width))
	metaText := strings.TrimSpace(fmt.Sprintf("%s  %s  %s",
		managedAgent.Provider,
		managedAgent.Activity,
		shortPath(managedAgent.Cwd),
	))
	meta := mutedStyle.Render(truncate(metaText, width))
	if managedAgent.Attention != agent.AttentionNone {
		meta = lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).
			Render(truncate("Needs "+string(managedAgent.Attention), width))
	}
	heading := lipgloss.JoinVertical(lipgloss.Left, title, meta, "")
	if action, ok := m.selectedPendingAction(); ok {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			heading,
			renderPendingAction(
				action,
				m.pendingOption,
				width,
				max(1, height-3),
			),
		)
	}

	composer := mutedStyle.Render(truncate("i compose  Enter open terminal", width))
	if m.mode == modeCompose {
		m.sendInput.SetWidth(max(1, width-2))
		composer = "> " + m.sendInput.View()
	}
	transcript := m.interaction.View() + ansi.ResetStyle
	if m.interactionID != managedAgent.ID {
		transcript = mutedStyle.Render(truncate("Loading interaction...", width))
	}
	return lipgloss.JoinVertical(
		lipgloss.Left,
		heading,
		transcript,
		composer,
	)
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
		pathStyle := titleStyle
		if m.formFocus == dispatchCustomPath {
			pathStyle = accentStyle
		}
		lines = append(lines,
			"",
			"  "+pathStyle.Render("Path"),
			"    "+m.cwdInput.View(),
		)
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

func (m Model) renderDispatchAt(width, height int) string {
	providerStyle := titleStyle
	if m.formFocus == dispatchProvider {
		providerStyle = accentStyle
	}

	directoryStyle := titleStyle
	taskStyle := titleStyle
	if m.formFocus == dispatchDirectory {
		directoryStyle = accentStyle
	}
	if m.formFocus == dispatchTask {
		taskStyle = accentStyle
	}
	contentWidth := max(1, width-4)
	m.cwdInput.SetWidth(max(10, contentWidth-2))
	m.taskInput.SetWidth(max(10, contentWidth-2))

	headerLeft := titleStyle.Render("  New agent")
	summaryWidth := max(1, width-lipgloss.Width(headerLeft)-3)
	headerRight := mutedStyle.Render(truncate(m.dispatchSummary(), summaryWidth))
	gap := max(1, width-lipgloss.Width(headerLeft)-lipgloss.Width(headerRight)-1)
	lines := []string{
		headerLeft + strings.Repeat(" ", gap) + headerRight,
		"",
		"  " + providerStyle.Render("Coding agent"),
		"  " + m.renderProviderSelector(),
	}
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
		fixedLines := 10
		if selected, ok := m.selectedDirectory(); ok &&
			selected.kind == directoryCustom {
			fixedLines = 13
		}
		lines = append(lines, m.renderDirectoryRows(
			contentWidth,
			clamp(height-fixedLines, 1, 8),
		)...)

		if selected, ok := m.selectedDirectory(); ok &&
			selected.kind == directoryCustom {
			pathStyle := titleStyle
			if m.formFocus == dispatchCustomPath {
				pathStyle = accentStyle
			}
			lines = append(lines,
				"",
				"  "+pathStyle.Render("Path"),
				"    "+m.cwdInput.View(),
			)
		}
	}

	lines = append(lines,
		"",
		"  "+taskStyle.Render("Input"),
		"    "+m.taskInput.View(),
	)
	return strings.Join(lines, "\n")
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
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render("  " + styled)
}

func renderSelectableRow(content string, width int, focused bool) string {
	if width < 3 {
		return lipgloss.NewStyle().
			Foreground(colorSelectedText).
			Background(colorSelect).
			Bold(focused).
			Width(max(1, width)).
			Render(ansi.Truncate(content, width, ""))
	}
	contentWidth := width - 2
	marker := "▏ "
	markerColor := colorBorder
	if focused {
		marker = "▌ "
		markerColor = colorWaiting
	}
	markerStyle := lipgloss.NewStyle().
		Foreground(markerColor).
		Background(colorSelect).
		Bold(focused)
	rowStyle := lipgloss.NewStyle().
		Foreground(colorSelectedText).
		Background(colorSelect).
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

func (m Model) renderProviderSelector() string {
	if len(m.providers) == 0 {
		return mutedStyle.Render("none")
	}

	options := make([]string, 0, len(m.providers))
	for index, info := range m.providers {
		label := info.Label
		if !info.Available {
			label += " ×"
		}
		if index == m.providerIndex {
			style := accentStyle
			if !info.Available {
				style = errorStyle
			}
			options = append(options, style.Render("["+label+"]"))
			continue
		}
		style := mutedStyle
		if !info.Available {
			style = errorStyle
		}
		options = append(options, style.Render(" "+label+" "))
	}
	return strings.Join(options, " ")
}

func (m Model) renderFooter() string {
	width := max(1, m.width-1)
	hints := m.commandHints()
	content := mutedStyle.Render(" " + truncate(hints, max(1, width-1)))
	if m.err != nil {
		content = renderFooterStatus(width, m.err.Error(), hints, errorStyle)
	} else if m.status != "Ready" {
		content = renderFooterStatus(width, m.status, hints, successStyle)
	}
	return lipgloss.NewStyle().Width(width).MaxHeight(1).Render(content)
}

func renderFooterStatus(
	width int,
	status string,
	hints string,
	statusStyle lipgloss.Style,
) string {
	available := max(1, width-1)
	statusWidth := clamp(available/3, 8, 28)
	hintWidth := available - statusWidth - 2
	if hintWidth < 12 {
		return mutedStyle.Render(" " + truncate(hints, available))
	}
	renderedStatus := statusStyle.Render(truncate(status, statusWidth))
	renderedHints := mutedStyle.Render(truncate(hints, hintWidth))
	return " " + renderedStatus + "  " + renderedHints
}

func (m Model) commandHints() string {
	switch m.mode {
	case modeCompose:
		return "Esc normal  Enter send"
	case modeDelete:
		return "d confirm  Esc cancel"
	case modeDispatch:
		if m.chooseDispatchDirectory {
			return "j/k location  Tab next  Ctrl-s launch  Esc cancel"
		}
		return "h/l choose agent  Tab next  Enter continue/launch  Esc cancel"
	case modeAddWorkspace:
		return "j/k select  Enter add  e edit path  Esc cancel"
	}
	rowMode := "z expand rows"
	if m.rowsExpanded {
		rowMode = "z compact rows"
	}
	if m.width < 72 {
		rowMode = ""
	}
	if m.activePane == paneInteraction {
		if action, ok := m.selectedPendingAction(); ok {
			hints := []string{"h agents", "j/k choose", "y allow"}
			for _, option := range action.Options {
				if strings.HasPrefix(option.ID, pending.OptionAlwaysPrefix) {
					hints = append(hints, "a always")
					break
				}
			}
			hints = append(hints, "n deny", "t terminal", "Enter confirm")
			return strings.Join(hints, "  ")
		}
	}
	switch m.activePane {
	case paneAgents:
		return strings.TrimSpace(
			"h/l panes  j/k select  n new  o choose path  " + rowMode + "  Enter open",
		)
	case paneInteraction:
		return strings.TrimSpace(
			"h agents  j/k scroll  i compose  n new  " + rowMode + "  Enter open",
		)
	default:
		return strings.TrimSpace(
			"j/k select  l agents  n add  d remove  " + rowMode + "  r refresh  q quit",
		)
	}
}

func (m *Model) focusForm() {
	m.cwdInput.Blur()
	m.taskInput.Blur()
	switch m.formFocus {
	case dispatchCustomPath:
		m.cwdInput.Focus()
	case dispatchTask:
		m.taskInput.Focus()
	}
}

func (m *Model) blurForm() {
	m.cwdInput.Blur()
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

func (m *Model) moveDispatchFocus(delta int) {
	if !m.chooseDispatchDirectory {
		focuses := []dispatchFocus{dispatchProvider, dispatchTask}
		current := 0
		if m.formFocus == dispatchTask {
			current = 1
		}
		m.formFocus = focuses[(current+delta+len(focuses))%len(focuses)]
		m.focusForm()
		return
	}
	focuses := []dispatchFocus{
		dispatchProvider,
		dispatchDirectory,
		dispatchTask,
	}
	if selected, ok := m.selectedDirectory(); ok &&
		selected.kind == directoryCustom {
		focuses = []dispatchFocus{
			dispatchProvider,
			dispatchDirectory,
			dispatchCustomPath,
			dispatchTask,
		}
	}
	current := 0
	for index, focus := range focuses {
		if focus == m.formFocus {
			current = index
			break
		}
	}
	current = (current + delta + len(focuses)) % len(focuses)
	m.formFocus = focuses[current]
	m.focusForm()
}

func (m Model) submitDispatch() (tea.Model, tea.Cmd) {
	if len(m.providers) == 0 {
		m.err = fmt.Errorf("no providers configured")
		return m, nil
	}
	request := app.DispatchRequest{
		Provider: m.providers[m.providerIndex].ID,
		Cwd:      strings.TrimSpace(m.cwdInput.Value()),
		Task:     strings.TrimSpace(m.taskInput.Value()),
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
	return m, dispatchCmd(m.backend, request)
}

func (m Model) selectedDirectory() (directoryChoice, bool) {
	if m.directoryIndex < 0 || m.directoryIndex >= len(m.directories) {
		return directoryChoice{}, false
	}
	return m.directories[m.directoryIndex], true
}

func (m Model) openYazi() (tea.Model, tea.Cmd) {
	if m.yaziPath == "" {
		m.err = fmt.Errorf("yazi is not installed or not on PATH")
		m.status = "Action failed"
		return m, nil
	}
	start := strings.TrimSpace(m.pickerStart)
	if m.mode != modeAddWorkspace {
		start = strings.TrimSpace(m.cwdInput.Value())
	}
	if !isDirectory(start) {
		start = m.initialCwd
	}
	command, err := yaziPickerCmd(m.yaziPath, start)
	if err != nil {
		m.err = err
		m.status = "Action failed"
		return m, nil
	}
	m.status = "Opening Yazi"
	return m, command
}

func yaziPickerCmd(binary, start string) (tea.Cmd, error) {
	choiceHandoff, err := createYaziHandoff("choice")
	if err != nil {
		return nil, err
	}
	cwdHandoff, err := createYaziHandoff("cwd")
	if err != nil {
		_ = os.Remove(choiceHandoff)
		return nil, err
	}

	pickerArgs := []string{
		"--chooser-file", choiceHandoff,
		"--cwd-file", cwdHandoff,
		start,
	}
	result := func(runErr error) tea.Msg {
		defer os.Remove(choiceHandoff)
		defer os.Remove(cwdHandoff)
		if runErr != nil {
			return directoryPickedMsg{err: fmt.Errorf("run Yazi: %w", runErr)}
		}
		choice, err := os.ReadFile(choiceHandoff)
		if err != nil {
			return directoryPickedMsg{err: fmt.Errorf("read Yazi choice: %w", err)}
		}
		cwd, err := os.ReadFile(cwdHandoff)
		if err != nil {
			return directoryPickedMsg{err: fmt.Errorf("read Yazi directory: %w", err)}
		}
		selected, err := resolveYaziDirectory(choice, cwd)
		if err != nil {
			return directoryPickedMsg{err: err}
		}
		if selected == "" {
			return directoryPickedMsg{}
		}
		return directoryPickedMsg{path: selected}
	}

	if os.Getenv("TMUX") != "" {
		tmuxPath, lookupErr := exec.LookPath("tmux")
		if lookupErr == nil {
			command := exec.Command(
				tmuxPath,
				yaziPopupArgs(binary, start, pickerArgs)...,
			)
			return func() tea.Msg {
				return result(command.Run())
			}, nil
		}
	}

	command := exec.Command(binary, pickerArgs...)
	command.Dir = start
	return tea.ExecProcess(command, result), nil
}

func yaziPopupArgs(binary, start string, pickerArgs []string) []string {
	args := []string{
		"display-popup",
		"-E",
		"-w", "78%",
		"-h", "76%",
		"-d", start,
		"-T", " Runstead · Choose directory ",
		"-S", "fg=#e5c07b",
		binary,
	}
	return append(args, pickerArgs...)
}

func createYaziHandoff(kind string) (string, error) {
	file, err := os.CreateTemp("", "runstead-yazi-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("create Yazi handoff file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Yazi handoff file: %w", err)
	}
	return path, nil
}

func resolveYaziDirectory(choice, cwd []byte) (string, error) {
	selected := firstYaziPath(choice)
	if selected == "" {
		selected = firstYaziPath(cwd)
	}
	if selected == "" {
		return "", nil
	}

	selected = filepath.Clean(selected)
	info, err := os.Stat(selected)
	if err != nil {
		return "", fmt.Errorf("Yazi selected path is unavailable: %s", selected)
	}
	if !info.IsDir() {
		selected = filepath.Dir(selected)
	}
	if !isDirectory(selected) {
		return "", fmt.Errorf("Yazi selected directory is unavailable: %s", selected)
	}
	return selected, nil
}

func firstYaziPath(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			return line
		}
	}
	return ""
}

func directoryKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

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

func (m *Model) rebuildGroups(preferredWorkspaceID, preferredAgentID string) {
	previousWorkspaceCursor := m.workspaceCursor
	previousAgentCursor := m.agentCursor
	m.groups = buildWorkspaceGroups(m.catalogWorkspaces, m.agents)
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

func (m *Model) moveSelection(delta int) {
	switch m.activePane {
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
	m.mode = modeDispatch
	m.dispatchPrefix = ""
	m.focusForm()
	m.err = nil
	m.status = "New agent"
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

func workspaceStats(agents []agent.Agent) (active int, attention int) {
	for _, managedAgent := range agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			active++
		}
		if managedAgent.NeedsAttention() {
			attention++
		}
	}
	return active, attention
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

func (m Model) interactionDimensions() (int, int) {
	contentHeight := max(1, m.height-8)
	width := max(1, m.width-1)
	if width < 72 {
		return max(1, width-2), contentHeight
	}
	workspaceWidth := clamp(width*24/100, 18, 30)
	agentWidth := clamp(width*33/100, 26, 44)
	interactionWidth := width - workspaceWidth - agentWidth
	if interactionWidth < 24 {
		agentWidth = max(22, agentWidth-(24-interactionWidth))
		interactionWidth = width - workspaceWidth - agentWidth
	}
	return max(1, interactionWidth-2), contentHeight
}

func (m Model) expandedRows() bool {
	return m.width < 72 || m.rowsExpanded
}

func (m Model) visibleRows() int {
	if m.activePane == paneInteraction && m.interaction.Height > 0 {
		return m.interaction.Height
	}
	return max(1, (m.height-3)/2)
}

func (m Model) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		actions, err := m.backend.ListPendingActions(ctx)
		if err != nil {
			return dashboardMsg{err: err}
		}
		agents, err := m.backend.ListAgents(ctx)
		if err != nil {
			return dashboardMsg{actions: actions, err: err}
		}
		workspaces, err := m.backend.ListWorkspaces(ctx)
		if err != nil {
			return dashboardMsg{
				agents:  agents,
				actions: actions,
				err:     err,
			}
		}
		return dashboardMsg{
			agents:     agents,
			workspaces: workspaces,
			actions:    actions,
		}
	}
}

func (m Model) loadInteractionCmd() tea.Cmd {
	id := m.selectedAgentID()
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		content, err := m.backend.Capture(ctx, id, 120)
		return interactionMsg{id: id, content: content, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func pulseTickCmd() tea.Cmd {
	return tea.Tick(650*time.Millisecond, func(t time.Time) tea.Msg {
		return pulseTickMsg(t)
	})
}

func actionCmd(status string, action func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := action(ctx)
		if err != nil {
			diagnostic.Logger().Error("dashboard command failed",
				"action", status,
				"error", err,
			)
		}
		return actionMsg{status: status, err: err}
	}
}

func resolvePendingActionCmd(
	backend Backend,
	action pending.Action,
	option pending.Option,
	managedAgent agent.Agent,
) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := backend.ResolvePendingAction(ctx, action.ID, option.ID)
		return pendingResolvedMsg{
			actionID: action.ID,
			optionID: option.ID,
			agentID:  managedAgent.ID,
			name:     agentDisplayTitle(managedAgent),
			terminal: option.ID == pending.OptionTerminal,
			err:      err,
		}
	}
}

func addWorkspaceCmd(backend Backend, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		value, err := backend.AddWorkspace(ctx, path)
		return workspaceAddedMsg{value: value, err: err}
	}
}

func attachCmd(backend Backend, id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := backend.Attach(ctx, id)
		return attachMsg{result: result, name: name, err: err}
	}
}

func dispatchCmd(backend Backend, request app.DispatchRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		managedAgent, err := backend.Dispatch(ctx, request)
		if err != nil {
			diagnostic.Logger().Error("dashboard dispatch failed",
				"provider", request.Provider,
				"error", err,
			)
			return actionMsg{err: err}
		}
		return actionMsg{status: "Dispatched " + agentDisplayTitle(managedAgent)}
	}
}

func statusVisual(managedAgent agent.Agent) (string, lipgloss.Style) {
	if managedAgent.NeedsAttention() {
		return "!", lipgloss.NewStyle().Foreground(colorWaiting).Bold(true)
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

func cleanInteraction(content string, width int, providerID agent.Provider) string {
	clean := sanitizeInteractionANSI(content)
	lines := focusConversation(strings.Split(clean, "\n"), providerID)
	lines = trimBlankInteractionLines(lines)
	if len(lines) == 0 {
		return mutedStyle.Render("No output yet")
	}

	compacted := make([]string, 0, len(lines))
	previousBlank := false
	for _, line := range lines {
		line = normalizeTerminalLine(line)
		wrapped := line
		if width > 0 {
			wrapped = ansi.Wrap(line, width, " /")
		}
		wrappedLines := strings.Split(wrapped, "\n")
		for index, wrappedLine := range wrappedLines {
			wrappedLine = trimANSIRight(wrappedLine)
			blank := strings.TrimSpace(ansi.Strip(wrappedLine)) == ""
			if blank && previousBlank {
				continue
			}
			if !blank && index == len(wrappedLines)-1 &&
				strings.Contains(line, "\x1b[") {
				wrappedLine += ansi.ResetStyle
			}
			compacted = append(compacted, wrappedLine)
			previousBlank = blank
		}
	}
	compacted = trimBlankInteractionLines(compacted)
	if len(compacted) == 0 {
		return mutedStyle.Render("No output yet")
	}
	return strings.Join(compacted, "\n")
}

func normalizeTerminalLine(line string) string {
	trimmedANSI := trimANSIWhitespace(line)
	trimmed := ansi.Strip(trimmedANSI)
	if isTerminalFrameBorder(trimmed) {
		return ""
	}
	if strings.HasPrefix(trimmed, "│") && strings.HasSuffix(trimmed, "│") {
		width := ansi.StringWidth(trimmedANSI)
		if width <= 2 {
			return ""
		}
		return trimANSIWhitespace(ansi.Cut(trimmedANSI, 1, width-1))
	}
	return trimANSIRight(line)
}

func sanitizeInteractionANSI(content string) string {
	content = strings.ReplaceAll(content, "\x00", "")
	var output strings.Builder
	var state byte
	for len(content) > 0 {
		sequence, cellWidth, consumed, nextState := ansi.DecodeSequence(content, state, nil)
		if consumed <= 0 {
			break
		}
		switch {
		case cellWidth > 0:
			output.WriteString(sequence)
		case sequence == "\n" || sequence == "\t":
			output.WriteString(sequence)
		case isPrintableZeroWidth(sequence):
			output.WriteString(sequence)
		case isSGRSequence(sequence):
			output.WriteString(sequence)
		}
		content = content[consumed:]
		state = nextState
	}
	return output.String()
}

func isPrintableZeroWidth(sequence string) bool {
	if sequence == "" {
		return false
	}
	first := sequence[0]
	if first >= utf8.RuneSelf {
		return utf8.ValidString(sequence)
	}
	return first >= 0x20 && first != 0x7f && first != 0x1b
}

func isSGRSequence(sequence string) bool {
	return len(sequence) >= 3 &&
		strings.HasPrefix(sequence, "\x1b[") &&
		strings.HasSuffix(sequence, "m")
}

func focusConversation(lines []string, providerID agent.Provider) []string {
	marker := ""
	switch providerID {
	case agent.ProviderClaude:
		marker = "❯"
	case agent.ProviderCodex:
		marker = "›"
	default:
		return lines
	}

	lines = trimBlankInteractionLines(lines)
	lines = trimTerminalComposer(lines, marker)
	for index, line := range lines {
		if promptHasContent(line, marker) {
			return lines[index:]
		}
	}
	return lines
}

func trimTerminalComposer(lines []string, marker string) []string {
	searchStart := max(0, len(lines)-10)
	for index := len(lines) - 1; index >= searchStart; index-- {
		text := strings.TrimSpace(ansi.Strip(lines[index]))
		if !strings.HasPrefix(text, marker) {
			continue
		}
		empty := strings.TrimSpace(strings.TrimPrefix(text, marker)) == ""
		if !empty && !composerStatusFollows(lines[index+1:]) {
			continue
		}
		cut := index
		for previous := index - 1; previous >= searchStart; previous-- {
			text := strings.TrimSpace(ansi.Strip(lines[previous]))
			if text == "" {
				continue
			}
			if isTerminalDivider(text) {
				cut = previous
			}
			break
		}
		return lines[:cut]
	}
	return lines
}

func composerStatusFollows(lines []string) bool {
	for _, line := range lines {
		text := strings.TrimSpace(ansi.Strip(line))
		if text == "" {
			continue
		}
		return isTerminalDivider(text) ||
			(strings.Contains(text, " · ") && strings.Contains(text, "/"))
	}
	return false
}

func promptHasContent(line, marker string) bool {
	text := strings.TrimSpace(ansi.Strip(line))
	if !strings.HasPrefix(text, marker) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, marker)) != ""
}

func isTerminalDivider(line string) bool {
	total := 0
	horizontal := 0
	for _, character := range line {
		if unicode.IsSpace(character) {
			continue
		}
		total++
		switch character {
		case '─', '━', '═', '╌', '-':
			horizontal++
		}
	}
	return horizontal >= 8 && horizontal*2 >= total
}

func trimBlankInteractionLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimANSIWhitespace(line string) string {
	plain := ansi.Strip(line)
	leftTrimmed := strings.TrimLeftFunc(plain, unicode.IsSpace)
	if leftTrimmed == "" {
		return ""
	}
	trimmed := strings.TrimRightFunc(leftTrimmed, unicode.IsSpace)
	leftWidth := ansi.StringWidth(plain[:len(plain)-len(leftTrimmed)])
	contentWidth := ansi.StringWidth(trimmed)
	return ansi.Cut(line, leftWidth, leftWidth+contentWidth)
}

func trimANSIRight(line string) string {
	plain := ansi.Strip(line)
	trimmed := strings.TrimRightFunc(plain, unicode.IsSpace)
	if trimmed == "" {
		return ""
	}
	return ansi.Truncate(line, ansi.StringWidth(trimmed), "")
}

func isTerminalFrameBorder(line string) bool {
	if line == "" {
		return false
	}
	for _, char := range line {
		switch char {
		case '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘', '─', '━', '╌', '═':
		default:
			return false
		}
	}
	return true
}

func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	return filepath.Clean(path)
}

func truncatePathTail(path string, width int) string {
	if width <= 0 || strings.TrimSpace(path) == "" {
		return ""
	}
	path = shortPath(path)
	pathWidth := ansi.StringWidth(path)
	if pathWidth <= width {
		return path
	}
	prefix := "…"
	removeWidth := pathWidth - max(1, width-ansi.StringWidth(prefix))
	return ansi.TruncateLeft(path, removeWidth, prefix)
}

func timeAgo(created time.Time) string {
	if created.IsZero() {
		return ""
	}
	elapsed := time.Since(created)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(strings.Join(strings.Fields(value), " "), width, "…")
}

func visibleRange(total, cursor, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	limit = min(limit, total)
	cursor = clamp(cursor, 0, total-1)
	start := clamp(cursor-limit/2, 0, total-limit)
	return start, start + limit
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
