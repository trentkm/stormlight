package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/pending"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/workspace"
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
	ClearAttention(context.Context, string) error
	Delete(context.Context, string) error
	Rename(context.Context, string, string) error
	RenameWorkspace(context.Context, workspace.Context, string) error
	Providers() []provider.Info
}

type mode int

const (
	modeNormal mode = iota
	modeDispatch
	modeCompose
	modeDelete
	modeAddWorkspace
	modeRename
)

// sortMode orders workspaces and agents. Sorting is always an explicit
// user choice (the yazi-style `,` chord) — rows never rearrange on their
// own; the default is stable newest-first.
type sortMode int

const (
	sortByCreated sortMode = iota
	sortByName
	sortByAttention
)

func (s sortMode) label() string {
	switch s {
	case sortByName:
		return "name"
	case sortByAttention:
		return "attention"
	default:
		return "newest"
	}
}

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
	surface surface.Surface

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
	taskInput               textarea.Model
	sendInput               textarea.Model
	initialCwd              string
	directories             []directoryChoice
	directoryIndex          int
	yaziPath                string
	nvimPath                string
	dispatchMode            agent.PermissionMode
	modeForDir              func(string) (agent.PermissionMode, bool)
	providerForDir          func(string) (agent.Provider, bool)
	renameInput             lineInput
	renameAgentID           string
	renameWorkspace         workspace.Context
	pickerStart             string
	chooseDispatchDirectory bool

	interactionID  string
	status         string
	err            error
	pendingActions []pending.Action
	pendingOption  int
	shimmerPhase   int
	shimmerRunning bool

	normalPrefix   string
	sortMode       sortMode
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

type taskEditedMsg struct {
	task string
	err  error
}

type workspaceAddedMsg struct {
	value workspace.Context
	err   error
}

type tickMsg time.Time
type shimmerTickMsg time.Time

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
	colorDangerBg     = lipgloss.AdaptiveColor{Light: "#F2D5D1", Dark: "#552B29"}

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	mutedStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle  = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	errorStyle   = lipgloss.NewStyle().Foreground(colorFailed)
	successStyle = lipgloss.NewStyle().Foreground(colorDone)
)

// Working things glow: a brighter band sweeps across their text — the
// closest a terminal gets to holding stormlight. stormlightGlow orders the
// shades base → mid → bright → crest; the crest sits at the band's center
// and falls off on both sides. The sweep replaces blinking as the working
// indicator for the header, workspace names, and agent titles.
const stormlightTitle = "Stormlight"

// shimmerRest adds off-screen travel on both ends of each sweep so the glow
// rests at the base shade between passes instead of wrapping abruptly.
const shimmerRest = 14

var stormlightGlow = []lipgloss.AdaptiveColor{
	{Light: "#0F7A90", Dark: "#3BA8BD"},
	{Light: "#0A93AE", Dark: "#5CC6DB"},
	{Light: "#00A9C9", Dark: "#8AE7F8"},
	{Light: "#00C2E8", Dark: "#C4F5FF"},
}

// shimmerText renders text in the glow palette. A negative phase (or a
// resting band position) yields the uniform base shade; otherwise the
// bright band centers on one rune and sweeps as the phase advances.
// background, when non-nil, preserves row highlighting behind the glow.
// shimmerBand computes the crest position for a text of the given length; a
// negative phase parks the band off-text so everything renders at base.
func shimmerBand(length, phase int) int {
	if phase < 0 {
		return -shimmerRest
	}
	return phase%(length+shimmerRest) - 4
}

func shimmerText(text string, phase int, background lipgloss.TerminalColor) string {
	runes := []rune(text)
	band := shimmerBand(len(runes), phase)
	var out strings.Builder
	for index, letter := range runes {
		distance := index - band
		if distance < 0 {
			distance = -distance
		}
		shade := max(0, len(stormlightGlow)-1-distance)
		style := lipgloss.NewStyle().
			Bold(true).
			Foreground(stormlightGlow[shade])
		if background != nil {
			style = style.Background(background)
		}
		out.WriteString(style.Render(string(letter)))
	}
	return out.String()
}

// rowTheme colors a selected list row. selectTheme is the normal selection;
// dangerTheme marks a row awaiting delete confirmation.
type rowTheme struct {
	background lipgloss.TerminalColor
	text       lipgloss.TerminalColor
	focusMark  lipgloss.TerminalColor
	restMark   lipgloss.TerminalColor
}

var (
	selectTheme = rowTheme{
		background: colorSelect,
		text:       colorSelectedText,
		focusMark:  colorWaiting,
		restMark:   colorBorder,
	}
	dangerTheme = rowTheme{
		background: colorDangerBg,
		text:       colorSelectedText,
		focusMark:  colorFailed,
		restMark:   colorFailed,
	}
)

func rowThemeFor(danger bool) rowTheme {
	if danger {
		return dangerTheme
	}
	return selectTheme
}

func NewModel(backend Backend) Model {
	return NewModelWithSurface(backend, surface.NewDirect())
}

func codingAgentProviders(infos []provider.Info) []provider.Info {
	return slices.DeleteFunc(slices.Clone(infos), func(info provider.Info) bool {
		return info.ID == agent.ProviderShell
	})
}

// Options carries user-configuration defaults into the dashboard. Zero
// values mean "use the built-in behavior".
type Options struct {
	YaziPath        string
	NvimPath        string
	DefaultMode     agent.PermissionMode
	DefaultProvider agent.Provider
	ExpandedRows    bool
	ModeForDir      func(string) (agent.PermissionMode, bool)
	ProviderForDir  func(string) (agent.Provider, bool)
}

func NewModelWithSurface(backend Backend, current surface.Surface) Model {
	return NewModelWithOptions(backend, current, Options{})
}

func NewModelWithOptions(backend Backend, current surface.Surface, options Options) Model {
	cwd, _ := os.Getwd()
	yaziPath := options.YaziPath
	if yaziPath == "" {
		yaziPath, _ = exec.LookPath("yazi")
	}
	nvimPath := options.NvimPath
	if nvimPath == "" {
		nvimPath, _ = exec.LookPath("nvim")
	}
	dispatchMode := options.DefaultMode
	if dispatchMode == "" {
		dispatchMode = agent.DefaultMode
	}
	if current == nil {
		current = surface.NewDirect()
	}

	cwdInput := newLineInput("")
	cwdInput.SetValue(cwd)

	taskInput := newTaskInput("What should the agent do?")

	sendInput := newTaskInput("Reply to the selected agent")
	sendInput.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "> "
		}
		return "  "
	})

	model := Model{
		backend:        backend,
		surface:        current,
		providers:      codingAgentProviders(backend.Providers()),
		cwdInput:       cwdInput,
		taskInput:      taskInput,
		sendInput:      sendInput,
		initialCwd:     cwd,
		yaziPath:       yaziPath,
		nvimPath:       nvimPath,
		activePane:     paneWorkspaces,
		rowsExpanded:   options.ExpandedRows,
		dispatchMode:   dispatchMode,
		modeForDir:     options.ModeForDir,
		providerForDir: options.ProviderForDir,
		shimmerRunning: true,
		status:         "Ready",
	}
	for index, info := range model.providers {
		if info.ID == options.DefaultProvider {
			model.providerIndex = index
			break
		}
	}
	model.prepareDirectoryChoices(cwd)
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tickCmd(), shimmerTickCmd())
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

	case shimmerTickMsg:
		if !m.anyAgentsActive() {
			m.shimmerRunning = false
			m.shimmerPhase = 0
			return m, nil
		}
		m.shimmerPhase = (m.shimmerPhase + 1) % (1 << 20)
		return m, shimmerTickCmd()

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
		if m.anyAgentsActive() && !m.shimmerRunning {
			m.shimmerRunning = true
			return m, tea.Batch(m.loadInteractionCmd(), shimmerTickCmd())
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

	case taskEditedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = "Action failed"
			diagnostic.Logger().Error("task editor failed", "error", msg.err)
			return m, nil
		}
		m.taskInput.SetValue(msg.task)
		m.formFocus = dispatchTask
		m.focusForm()
		m.err = nil
		m.status = "Task updated"
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
		case modeRename:
			return m.updateRename(msg)
		default:
			// The keypress is the presence proof: if an unseen result is
			// on screen right now (selected, transcript loaded) and the
			// human acts, it has been seen.
			seenID := ""
			if selected, ok := m.selectedAgent(); ok &&
				selected.ProcessLive &&
				selected.Attention == agent.AttentionWaiting &&
				m.interactionID == selected.ID {
				seenID = selected.ID
			}
			updated, cmd := m.updateNormal(msg)
			model, isModel := updated.(Model)
			if seenID == "" || !isModel {
				return updated, cmd
			}
			model.markAttentionSeen(seenID)
			return model, tea.Batch(cmd, clearAttentionCmd(model.backend, seenID))
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
	if m.normalPrefix == "," {
		m.normalPrefix = ""
		mode := m.sortMode
		switch key {
		case "a":
			mode = sortByAttention
		case "n":
			mode = sortByName
		case "c":
			mode = sortByCreated
		default:
			m.status = "Sort unchanged"
			return m, nil
		}
		m.sortMode = mode
		m.rebuildGroups(m.selectedWorkspaceID(), m.selectedAgentID())
		m.status = "Sorted by " + mode.label()
		return m, m.loadInteractionCmd()
	}
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
	case ",":
		m.normalPrefix = ","
		m.status = "Sort: a attention  n name  c newest"
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
		m.rowsExpanded = !m.rowsExpanded
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
			m.syncComposerSize()
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
	case "d", "ctrl+x":
		if m.activePane == paneWorkspaces {
			selected, ok := m.selectedWorkspace()
			if !ok {
				return m, nil
			}
			m.mode = modeDelete
			m.err = nil
			if count := len(m.groups[m.workspaceCursor].agents); count > 0 {
				m.status = fmt.Sprintf(
					"Delete %s and %d agent(s)? press X",
					m.selectedWorkspaceLabel(),
					count,
				)
			} else {
				m.status = "Remove " + selected.Name + "? press again"
			}
			return m, nil
		}
		if selected, ok := m.selectedAgent(); ok {
			m.mode = modeDelete
			m.err = nil
			m.status = "Delete " + agentDisplayTitle(selected) + "? press again"
			return m, nil
		}
	case "r", "ctrl+l":
		m.status = "Refreshing"
		return m, tea.Batch(m.refreshCmd(), m.loadInteractionCmd())
	case "R":
		return m.beginRename()
	case "M":
		return m.clearAttention()
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
		var cmd tea.Cmd
		m.taskInput, cmd = m.taskInput.Update(msg)
		return m, cmd
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
	case "ctrl+j", "shift+enter":
		m.sendInput.InsertString("\n")
		m.syncComposerSize()
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
	var cmd tea.Cmd
	m.sendInput, cmd = m.sendInput.Update(msg)
	m.syncComposerSize()
	return m, cmd
}

// syncComposerSize keeps the persisted reply textarea sized to the Spanreed
// pane. Sizing only a render-time copy is not enough: the textarea's
// internal wrap and scroll state evolve while typing, so it must live at
// the real dimensions.
func (m *Model) syncComposerSize() {
	// interactionDimensions already returns the pane's content width (the
	// same value renderInteraction receives) — no further adjustment.
	inner, _ := m.interactionDimensions()
	inner = max(1, inner)
	previous := m.sendInput.Height()
	m.sendInput.SetWidth(inner)
	height := composerHeight(m.sendInput.Value(), inner)
	m.sendInput.SetHeight(height)
	if height > previous {
		// The keystroke that grew the content was processed while the box
		// was still one row short, which scrolled the textarea's viewport —
		// and nothing scrolls it back once the box catches up (its
		// repositioning only ever chases the cursor). Rebuilding the value
		// resets the scroll; the cursor is put back where it was.
		row := m.sendInput.Line()
		info := m.sendInput.LineInfo()
		column := info.StartColumn + info.ColumnOffset
		m.sendInput.SetValue(m.sendInput.Value())
		for m.sendInput.Line() > row {
			m.sendInput.CursorUp()
		}
		m.sendInput.SetCursor(column)
	}
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
	case "d", "x", "ctrl+x", "y", "enter":
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

func (m Model) anyAgentsActive() bool {
	for _, managedAgent := range m.agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			return true
		}
	}
	return false
}

// shimmerPhaseOrRest returns the sweep phase while the shimmer is running
// and the resting (uniform base shade) phase otherwise.
func (m Model) shimmerPhaseOrRest() int {
	if m.shimmerRunning {
		return m.shimmerPhase
	}
	return -1
}

func (m Model) renderHeader() string {
	width := max(1, m.width-1)
	working := 0
	urgent := 0
	waiting := 0
	for _, managedAgent := range m.agents {
		if managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Activity == agent.ActivityStarting {
			working++
		}
		if !managedAgent.ProcessLive {
			continue
		}
		switch {
		case managedAgent.Attention.Urgent():
			urgent++
		case managedAgent.Attention == agent.AttentionWaiting:
			waiting++
		}
	}
	left := " " + shimmerText(stormlightTitle, m.shimmerPhaseOrRest(), nil)
	right := mutedStyle.Render(fmt.Sprintf("%d active", working))
	if waiting > 0 {
		right += "  " + lipgloss.NewStyle().Foreground(colorWaiting).
			Render(fmt.Sprintf("%d waiting", waiting))
	}
	if urgent > 0 {
		attentionLabel := fmt.Sprintf("%d need input", urgent)
		if urgent == 1 {
			attentionLabel = "1 needs input"
		}
		right += "  " + lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).
			Render(attentionLabel)
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) renderBody() string {
	contentHeight := max(1, m.height-4)
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
	case modeRename:
		return overlayCentered(
			dashboard,
			m.renderRenameModal(width, contentHeight),
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
		// One extra column of slack keeps row text from touching the
		// hierarchy connector drawn in the pane's padding column.
		m.renderWorkspaces(max(1, workspaceWidth-3), contentHeight-1),
		workspaceWidth,
		contentHeight,
		m.activePane == paneWorkspaces,
		true,
	)
	if workspaceRow, agentRow, ok := m.hierarchyConnectorRows(contentHeight); ok {
		workspaces = paintHierarchyConnector(
			workspaces,
			workspaceWidth,
			workspaceRow,
			agentRow,
		)
	}
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
		"Spanreed",
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

func (m Model) hierarchyConnectorRows(contentHeight int) (int, int, bool) {
	if len(m.groups) == 0 || contentHeight < 2 {
		return 0, 0, false
	}
	agents := m.agentsForSelectedWorkspace()
	if len(agents) == 0 {
		return 0, 0, false
	}

	expanded := m.expandedRows()
	listHeight := contentHeight - 1
	workspaceCapacity := listRowCapacity(listHeight, expanded)
	workspaceStart, workspaceEnd := visibleRange(
		len(m.groups),
		m.workspaceCursor,
		workspaceCapacity,
	)
	agentCapacity := listRowCapacity(listHeight, expanded)
	agentStart, agentEnd := visibleRange(
		len(agents),
		m.agentCursor,
		agentCapacity,
	)
	if m.workspaceCursor < workspaceStart ||
		m.workspaceCursor >= workspaceEnd ||
		m.agentCursor < agentStart ||
		m.agentCursor >= agentEnd {
		return 0, 0, false
	}

	rowStep := 1
	if expanded {
		rowStep = 3
	}
	workspaceRow := 1 + (m.workspaceCursor-workspaceStart)*rowStep
	agentRow := 1 + (m.agentCursor-agentStart)*rowStep
	return workspaceRow, agentRow, true
}

func paintHierarchyConnector(
	paneContent string,
	width int,
	workspaceRow int,
	agentRow int,
) string {
	if width < 2 {
		return paneContent
	}
	lines := strings.Split(paneContent, "\n")
	if workspaceRow < 0 ||
		workspaceRow >= len(lines) ||
		agentRow < 0 ||
		agentRow >= len(lines) {
		return paneContent
	}

	// The connector lives in the padding column between the workspace text
	// and the pane divider, so the divider stays continuous and the gold
	// arc spans exactly its two endpoint rows — rounded caps, no spill.
	style := lipgloss.NewStyle().Foreground(colorWaiting)
	first := min(workspaceRow, agentRow)
	last := max(workspaceRow, agentRow)
	for row := first; row <= last; row++ {
		glyph := "│"
		switch {
		case workspaceRow == agentRow:
			glyph = "─"
		case row == workspaceRow && workspaceRow < agentRow:
			glyph = "╮"
		case row == workspaceRow:
			glyph = "╯"
		case row == agentRow && agentRow < workspaceRow:
			glyph = "╭"
		case row == agentRow:
			glyph = "╰"
		}
		lines[row] = replaceStyledCell(
			lines[row],
			width,
			width-2,
			glyph,
			style,
		)
	}
	return strings.Join(lines, "\n")
}

func replaceStyledCell(
	line string,
	width int,
	column int,
	value string,
	style lipgloss.Style,
) string {
	if width <= 0 || column < 0 || column >= width {
		return line
	}
	line = fitLine(line, width)
	before := ansi.Cut(line, 0, column)
	after := ansi.Cut(line, column+1, width)
	restore := ""
	if column+1 < width {
		restore = sgrStateAt(line, column+1)
	}
	return fitLine(before, column) +
		ansi.ResetStyle +
		style.Render(value) +
		ansi.ResetStyle +
		restore +
		fitLine(after, width-column-1)
}

func (m Model) renderDispatchModal(width, height int) string {
	preferredWidth := 62
	preferredHeight := 18
	if m.chooseDispatchDirectory {
		preferredWidth = 78
		preferredHeight = 24
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
			"Spanreed",
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
		// Dividers are structure, not state: focus is shown by the header
		// underline and the single hot cursor row, never the frame.
		innerWidth = max(1, width-1)
		style = style.Width(innerWidth).
			BorderStyle(lipgloss.NormalBorder()).
			BorderRight(true).
			BorderForeground(colorBorder)
	}
	header := renderPaneHeader(label, contextLabel, innerWidth, active)
	body := lipgloss.NewStyle().
		Width(innerWidth).
		MaxWidth(innerWidth).
		Render(content)
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

// renderPaneHeader underlines the entire header cell, padding included, so
// the header reads as a full-width ruled box top rather than floating text.
func renderPaneHeader(label, contextLabel string, width int, active bool) string {
	underlined := func(style lipgloss.Style) lipgloss.Style {
		return style.Underline(true)
	}
	fill := underlined(lipgloss.NewStyle().Foreground(colorBorder))

	left := underlined(mutedStyle.Copy().Bold(true)).
		Render(truncate(" "+label, width))
	if active {
		// The active pane's header rule renders in accent — the pane's
		// "selected tab" indicator. The rule alone carries the signal; no
		// rail, no extra chrome.
		fill = underlined(lipgloss.NewStyle().Foreground(colorAccent))
		left = underlined(titleStyle.Copy()).
			Render(truncate(" "+label, width))
	}

	remaining := width - lipgloss.Width(left) - 2
	if strings.TrimSpace(contextLabel) == "" || remaining < 4 {
		pad := max(0, width-lipgloss.Width(left))
		return left + fill.Render(strings.Repeat(" ", pad))
	}

	rightStyle := underlined(mutedStyle.Copy())
	if strings.ContainsAny(contextLabel, "‹›") {
		rightStyle = underlined(accentStyle.Copy())
	}
	right := rightStyle.Render(truncate(contextLabel, remaining))
	gap := max(2, width-lipgloss.Width(left)-lipgloss.Width(right))
	tail := max(0, width-lipgloss.Width(left)-gap-lipgloss.Width(right))
	return left + fill.Render(strings.Repeat(" ", gap)) + right +
		fill.Render(strings.Repeat(" ", tail))
}

func (m Model) renderWorkspaces(width, height int) string {
	if len(m.groups) == 0 {
		return "\n" + mutedStyle.Render(" No workspaces")
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
	countLabel := fmt.Sprintf("%d agents", len(group.agents))
	if len(group.agents) == 1 {
		countLabel = "1 agent"
	}
	suffixes := []string{}
	if urgent > 0 {
		suffixes = append(suffixes, fmt.Sprintf("%d input", urgent))
	}
	if waiting > 0 {
		suffixes = append(suffixes, fmt.Sprintf("%d waiting", waiting))
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
	switch {
	case urgent > 0:
		activityMarker = "! "
	case waiting > 0:
		activityMarker = "○ "
	case active > 0:
		activityMarker = "● "
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
	path, kind, detailGap := workspaceDetail(group.context, contentWidth)
	bottomContent := path + strings.Repeat(" ", detailGap) + kind
	tier := attentionTierOf(urgent, waiting)
	if focused || danger {
		return renderSelectedWorkspaceRow(
			activityMarker,
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

	marker := "  "
	if selected {
		marker = lipgloss.NewStyle().Foreground(colorBorder).Render("› ")
	}
	activityStyle := mutedStyle
	renderedName := titleStyle.Render(name)
	suffixStyle := mutedStyle
	switch {
	case tier == tierUrgent:
		// Urgent attention outranks the working glow: the whole row goes
		// loud amber.
		attentionStyle := lipgloss.NewStyle().Foreground(colorWaiting).Bold(true)
		activityStyle = attentionStyle
		renderedName = attentionStyle.Render(name)
		suffixStyle = attentionStyle
	case tier == tierWaiting:
		// Soft tier: amber marker and count only; the row stays calm.
		softStyle := lipgloss.NewStyle().Foreground(colorWaiting)
		activityStyle = softStyle
		suffixStyle = softStyle
	case active > 0:
		activityStyle = lipgloss.NewStyle().
			Foreground(colorWorking).
			Bold(true)
		renderedName = shimmerText(name, m.shimmerPhaseOrRest(), nil)
	}
	top := marker + activityStyle.Render(activityMarker) +
		renderedName +
		strings.Repeat(" ", gap) +
		suffixStyle.Render(suffix)
	bottom := marker + mutedStyle.Render(path) +
		strings.Repeat(" ", detailGap) +
		mutedStyle.Render(kind)
	if !m.expandedRows() {
		return top
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
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

	marker := "▏ "
	markerColor := theme.restMark
	if focused {
		marker = "▌ "
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

	contentWidth := width - 2
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
	if badge := modeBadge(managedAgent.Mode); badge != "" {
		details = append(details, badge)
	}
	if location := agentLocation(managedAgent); location != "" {
		details = append(details, location)
	}
	bottomContent := truncate("  "+strings.Join(details, "  "), contentWidth)
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
	marker := "  "
	if selected {
		marker = lipgloss.NewStyle().Foreground(colorBorder).Render("› ")
	}
	top := marker + statusStyle.Render(symbol) + " " +
		renderedTitle +
		strings.Repeat(" ", gap) +
		detailStyle.Render(age)
	bottom := marker + detailStyle.Render(bottomContent)
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

	contentWidth := width - 2
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
		markerStyle.Render("▌ ")+topStyle.Render(top),
		markerStyle.Render("▌ ")+bottomStyle.Render(bottom),
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

	contentWidth := width - 2
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
	title := titleStyle.Render(truncate(agentDisplayTitle(managedAgent), width))
	if managedAgent.Activity == agent.ActivityWorking ||
		managedAgent.Activity == agent.ActivityStarting {
		title = shimmerText(
			truncate(agentDisplayTitle(managedAgent), width),
			m.shimmerPhaseOrRest(),
			nil,
		)
	}
	metaParts := []string{
		string(managedAgent.Provider),
		string(managedAgent.Activity),
	}
	if badge := modeBadge(managedAgent.Mode); badge != "" {
		metaParts = append(metaParts, badge)
	}
	metaParts = append(metaParts, shortPath(managedAgent.Cwd))
	metaText := strings.TrimSpace(strings.Join(metaParts, "  "))
	meta := mutedStyle.Render(truncate(metaText, width))
	if managedAgent.ProcessLive && managedAgent.Attention.Urgent() {
		meta = lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).
			Render(truncate("Needs "+string(managedAgent.Attention), width))
	} else if managedAgent.ProcessLive &&
		managedAgent.Attention == agent.AttentionWaiting {
		meta = lipgloss.NewStyle().Foreground(colorWaiting).
			Render(truncate("Unseen result", width))
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

	viewportCopy := m.interaction
	composer := mutedStyle.Render(truncate("i reply  Enter open terminal", width))
	if m.mode == modeCompose {
		m.sendInput.SetWidth(max(1, width))
		inputHeight := composerHeight(m.sendInput.Value(), max(1, width))
		m.sendInput.SetHeight(inputHeight)
		rule := mutedStyle.Render(strings.Repeat("─", max(1, width)))
		composer = lipgloss.JoinVertical(
			lipgloss.Left,
			rule,
			m.sendInput.View(),
			rule,
		)
		// The bordered composer grows; the transcript yields the rows.
		viewportCopy.Height = max(1, viewportCopy.Height-inputHeight-2+1)
	}
	transcript := viewportCopy.View() + ansi.ResetStyle
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
			customPathLines = 3
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

	roomy := height >= 15
	if roomy {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+m.renderDispatchModeLine(contentWidth))

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
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render("  " + styled)
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
	contentWidth := width - 2
	marker := "▏ "
	markerColor := t.restMark
	if focused {
		marker = "▌ "
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
			Render("  "+styled))
	}
	return rows
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
	rule := lipgloss.NewStyle().
		Foreground(colorBorder).
		Render(strings.Repeat("─", width))
	return lipgloss.NewStyle().Width(width).MaxHeight(2).Render(
		rule + "\n" + content,
	)
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
		return "Enter send  Ctrl-j newline  Esc cancel"
	case modeDelete:
		if m.activePane == paneWorkspaces &&
			m.workspaceCursor >= 0 && m.workspaceCursor < len(m.groups) &&
			len(m.groups[m.workspaceCursor].agents) > 0 {
			return "X delete workspace and agents  Esc cancel"
		}
		return "d/x confirm  Esc cancel"
	case modeDispatch:
		switch m.formFocus {
		case dispatchProvider:
			hints := "j/k choose  Enter task"
			if m.chooseDispatchDirectory {
				hints = "j/k choose  Enter location"
			}
			hints += "  m mode"
			if m.nvimPath != "" {
				hints += "  e Neovim"
			}
			return hints + "  Esc cancel"
		case dispatchDirectory:
			return "j/k location  Enter choose  m mode  e edit path  Esc cancel"
		case dispatchCustomPath:
			return "Enter accept path  Esc cancel"
		default:
			hints := "Enter launch"
			if m.nvimPath != "" {
				hints += "  Ctrl-o Neovim"
			}
			return hints + "  Esc cancel"
		}
	case modeAddWorkspace:
		return "j/k select  Enter add  e edit path  Esc cancel"
	case modeRename:
		return "Enter apply  Esc cancel"
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
			"h/l panes  j/k select  n new  M seen  , sort  " + rowMode + "  Enter open",
		)
	case paneInteraction:
		return strings.TrimSpace(
			"h agents  j/k scroll  i reply  n new  " + rowMode + "  Enter open",
		)
	default:
		return strings.TrimSpace(
			"j/k select  l agents  n add  d remove  , sort  " + rowMode + "  r refresh  q quit",
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
	return m, dispatchCmd(m.backend, request)
}

func (m Model) selectedDirectory() (directoryChoice, bool) {
	if m.directoryIndex < 0 || m.directoryIndex >= len(m.directories) {
		return directoryChoice{}, false
	}
	return m.directories[m.directoryIndex], true
}

func (m Model) openTaskEditor() (tea.Model, tea.Cmd) {
	if m.nvimPath == "" {
		m.err = fmt.Errorf("Neovim is not installed or not on PATH")
		m.status = "Action failed"
		return m, nil
	}
	cwd := strings.TrimSpace(m.cwdInput.Value())
	if !isDirectory(cwd) {
		cwd = m.initialCwd
	}
	command, err := taskEditorCmd(
		m.surface,
		m.nvimPath,
		cwd,
		m.taskInput.Value(),
	)
	if err != nil {
		m.err = err
		m.status = "Action failed"
		return m, nil
	}
	m.status = "Opening Neovim"
	return m, command
}

func taskEditorCmd(
	current surface.Surface,
	binary string,
	cwd string,
	task string,
) (tea.Cmd, error) {
	handoff, err := os.CreateTemp("", "stormlight-task-*.md")
	if err != nil {
		return nil, fmt.Errorf("create task editor file: %w", err)
	}
	handoffPath := handoff.Name()
	cleanup := func() {
		_ = os.Remove(handoffPath)
	}
	if _, err := handoff.WriteString(task); err != nil {
		_ = handoff.Close()
		cleanup()
		return nil, fmt.Errorf("write task editor file: %w", err)
	}
	if err := handoff.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close task editor file: %w", err)
	}

	result := func(runErr error) tea.Msg {
		defer cleanup()
		if runErr != nil {
			return taskEditedMsg{err: fmt.Errorf("run Neovim: %w", runErr)}
		}
		content, err := os.ReadFile(handoffPath)
		if err != nil {
			return taskEditedMsg{err: fmt.Errorf(
				"read task editor file: %w",
				err,
			)}
		}
		return taskEditedMsg{
			task: strings.TrimSuffix(string(content), "\n"),
		}
	}

	var popup *surface.Popup
	if current.Capabilities().Popups {
		popup = &surface.Popup{
			Width:       "82%",
			Height:      "76%",
			Title:       " Stormlight · Edit task ",
			BorderStyle: "fg=#e5c07b",
		}
	}
	presentation, err := current.Present(surface.Request{
		Command: surface.Command{
			Path: binary,
			Args: []string{handoffPath},
			Dir:  cwd,
		},
		Popup: popup,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	if presentation.Command == nil {
		cleanup()
		return nil, fmt.Errorf("surface returned an empty Neovim command")
	}
	switch presentation.Mode {
	case surface.PresentationOverlay:
		return func() tea.Msg {
			return result(presentation.Command.Run())
		}, nil
	case surface.PresentationSuspend:
		return tea.ExecProcess(presentation.Command, result), nil
	default:
		cleanup()
		return nil, fmt.Errorf(
			"surface returned unsupported presentation mode %d",
			presentation.Mode,
		)
	}
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
	command, err := yaziPickerCmd(m.surface, m.yaziPath, start)
	if err != nil {
		m.err = err
		m.status = "Action failed"
		return m, nil
	}
	m.status = "Opening Yazi"
	return m, command
}

func yaziPickerCmd(
	current surface.Surface,
	binary string,
	start string,
) (tea.Cmd, error) {
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

	var popup *surface.Popup
	if current.Capabilities().Popups {
		popup = &surface.Popup{
			Width:       "78%",
			Height:      "76%",
			Title:       " Stormlight · Choose directory ",
			BorderStyle: "fg=#e5c07b",
		}
	}
	presentation, err := current.Present(surface.Request{
		Command: surface.Command{
			Path: binary,
			Args: pickerArgs,
			Dir:  start,
		},
		Popup: popup,
	})
	if err != nil {
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, err
	}
	if presentation.Command == nil {
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, fmt.Errorf("surface returned an empty Yazi command")
	}
	switch presentation.Mode {
	case surface.PresentationOverlay:
		return func() tea.Msg {
			return result(presentation.Command.Run())
		}, nil
	case surface.PresentationSuspend:
		return tea.ExecProcess(presentation.Command, result), nil
	default:
		_ = os.Remove(choiceHandoff)
		_ = os.Remove(cwdHandoff)
		return nil, fmt.Errorf(
			"surface returned unsupported presentation mode %d",
			presentation.Mode,
		)
	}
}

func createYaziHandoff(kind string) (string, error) {
	file, err := os.CreateTemp("", "stormlight-yazi-"+kind+"-*")
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

func (m Model) interactionDimensions() (int, int) {
	contentHeight := max(1, m.height-9)
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
	return m.rowsExpanded
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

func shimmerTickCmd() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg {
		return shimmerTickMsg(t)
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

// composerHeight sizes the reply box to its wrapped content: the textarea
// wraps at its width minus the two-column prompt. One row minimum, six
// before the box scrolls internally. The height MUST match the textarea's
// own layout exactly — its scroll viewport is shared between model copies,
// and an undersized box wedges it scrolled down permanently.
func composerHeight(value string, width int) int {
	wrapWidth := max(1, width-2)
	total := 0
	for _, logical := range strings.Split(value, "\n") {
		total += textareaWrapCount([]rune(logical), wrapWidth)
	}
	return clamp(total, 1, 6)
}

// textareaWrapCount is a faithful port of bubbles/textarea's internal
// wrap(), reduced to counting soft lines. Note the closing `>=`: a line
// exactly filling the width spills its final word onto a new row.
func textareaWrapCount(runes []rune, width int) int {
	lines := [][]rune{{}}
	word := []rune{}
	row := 0
	spaces := 0

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if lipgloss.Width(string(lines[row]))+lipgloss.Width(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			}
			spaces = 0
			word = nil
		} else {
			lastCharLen := lipgloss.Width(string(word[len(word)-1]))
			if lipgloss.Width(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if lipgloss.Width(string(lines[row]))+lipgloss.Width(string(word))+spaces >= width {
		lines = append(lines, []rune{})
	}
	return len(lines)
}
