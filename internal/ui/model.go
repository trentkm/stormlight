package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/spanreed"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/ptyview"
	"github.com/trentkm/stormlight/internal/resurrect"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/theme"
	"github.com/trentkm/stormlight/internal/workspace"
)

type Backend interface {
	ListAgents(context.Context) ([]agent.Agent, error)
	ListWorkspaces(context.Context) ([]workspace.Context, error)
	AddWorkspace(context.Context, string) (workspace.Context, error)
	RemoveWorkspace(context.Context, workspace.Context) error
	Dispatch(context.Context, app.DispatchRequest) (agent.Agent, error)
	Capture(context.Context, string, int) (string, error)
	Attach(context.Context, string) (app.AttachResult, error)
	Send(context.Context, string, string) error
	Interrupt(context.Context, string) error
	ClearAttention(context.Context, string) error
	SetMark(context.Context, string, agent.Mark) error
	Delete(context.Context, string) error
	Rename(context.Context, string, string) error
	RenameWorkspace(context.Context, workspace.Context, string) error
	// The raw-byte transport behind the PTY Spanreed
	// (session.PaneStreamer, surfaced through the service).
	PipePaneOpen(ctx context.Context, id, fifoPath string) error
	PipePaneClose(ctx context.Context, id string) error
	CaptureRaw(ctx context.Context, id string, history int) (string, error)
	PaneView(ctx context.Context, id string) (session.PaneView, error)
	ResizeAgentWindow(ctx context.Context, id string, width, height int) error
	SendRaw(ctx context.Context, id string, data []byte) error
	Providers() []provider.Info
	SessionHistory(context.Context) ([]history.Record, error)
	Resume(context.Context, history.Record) (agent.Agent, error)
	// RestoreCandidates reads the agents remembered from before the runtime
	// that is hosting this dashboard existed, and Restore brings them back.
	RestoreCandidates(context.Context) ([]resurrect.Candidate, error)
	Restore(context.Context, ...string) ([]app.RestoreResult, error)
	Forget(context.Context, ...string) error
}

type mode int

const (
	modeNormal mode = iota
	modeDispatch
	modeCompose
	modeSearch
	modeDelete
	modeAddWorkspace
	modeRename
	modeMark
	modeInfo
	modeHelp
	modeHistory
	modeRestore
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
	dispatchName
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
	nameInput               lineInput
	taskInput               textarea.Model
	sendInput               textarea.Model
	initialCwd              string
	initialWorkspaceID      string
	interactionContent      string
	search                  transcriptSearch
	selectionActive         bool
	selectionDragging       bool
	selectionAnchor         int
	selectionHead           int
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
	markAgentID             string
	markIndex               int
	historyRecords          []history.Record
	historyCursor           int
	historyLoading          bool
	historyFilter           lineInput
	historyFiltering        bool
	pathNav                 pathNav
	pickerStart             string
	chooseDispatchDirectory bool

	interactionID       string
	interactionLoadedAt time.Time
	status              string
	err                 error
	shimmerPhase        int
	shimmerRunning      bool

	restoreCandidates []resurrect.Candidate
	restoreChosen     map[string]bool
	restoreCursor     int
	restoreOffered    bool

	normalPrefix   string
	sortMode       sortMode
	dispatchPrefix string
	columns        ColumnPrefs

	// The PTY Spanreed. The manager keeps one live terminal session per
	// agent for the agent's whole life; ptyEnabled says which view the
	// Spanreed renders (terminal by default, transcript after `t`), and
	// ptyArmed tracks which widgets already have a frame listener so
	// redraw wake-ups never double up. Pointers and maps on purpose: they
	// must survive the Model's value copies.
	ptyEnabled bool
	// ptyZoom collapses the sidebars so the portal spans the full body —
	// still the same layout pipeline, not a separate mode. It survives a
	// trip through the transcript view: t out, t back, still zoomed.
	ptyZoom    bool
	ptyManager *ptyview.Manager
	ptyArmed   map[int64]bool
}

type dashboardMsg struct {
	agents     []agent.Agent
	workspaces []workspace.Context
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

type historyMsg struct {
	records []history.Record
	err     error
}

type restoreCandidatesMsg struct {
	candidates []resurrect.Candidate
	// offer marks the unprompted reading taken at startup, which opens the
	// picker on its own if there is something worth opening it for.
	offer bool
	err   error
}

type tickMsg time.Time
type shimmerTickMsg time.Time

func NewModel(backend Backend) Model {
	return NewModelWithSurface(backend, surface.NewDirect())
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
	// SelectWorkspaceID names the workspace the first dashboard refresh
	// should land on, for launches like `stormlight <path>`.
	SelectWorkspaceID string
	// Columns restores the user's saved < > pane-width adjustments.
	Columns ColumnPrefs
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

	nameInput := newLineInput("Named after the task")

	taskInput := newTaskInput("What should the agent do?")

	sendInput := newTaskInput("Reply to the selected agent")
	sendInput.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "> "
		}
		return "  "
	})

	model := Model{
		backend:            backend,
		surface:            current,
		providers:          backend.Providers(),
		cwdInput:           cwdInput,
		nameInput:          nameInput,
		taskInput:          taskInput,
		sendInput:          sendInput,
		initialCwd:         cwd,
		initialWorkspaceID: options.SelectWorkspaceID,
		yaziPath:           yaziPath,
		nvimPath:           nvimPath,
		activePane:         paneWorkspaces,
		rowsExpanded:       options.ExpandedRows,
		dispatchMode:       dispatchMode,
		modeForDir:         options.ModeForDir,
		providerForDir:     options.ProviderForDir,
		shimmerRunning:     true,
		status:             "Ready",
		columns:            options.Columns,
		ptyEnabled:         true,
		ptyManager:         ptyview.NewManager(backend, ptyStateDir()),
		ptyArmed:           make(map[int64]bool),
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
	return tea.Batch(
		m.refreshCmd(),
		tickCmd(),
		shimmerTickCmd(),
		// Read the snapshot once at startup. Whether it opens the picker
		// depends on what the first listing finds; asking now means the
		// answer is already in hand when that lands.
		restoreCandidatesCmd(m.backend, false),
		// Ask the terminal what it is painted on. Lip Gloss v1 answered this
		// itself by querying behind the program's back on first use; v2 does
		// not, so the palette runs on its dark default until this comes back.
		tea.RequestBackgroundColor,
	)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.BackgroundColorMsg:
		// Settling the palette changes every color the next frame draws,
		// and the transcript renderer reads the same answer when it rebuilds
		// its markdown stylesheet.
		theme.Resolve(msg.IsDark())
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		interactionWidth, contentHeight := m.interactionDimensions()
		if !m.ready {
			m.interaction = viewport.New(viewport.WithWidth(interactionWidth), viewport.WithHeight(contentHeight))
			m.ready = true
		} else {
			m.interaction.SetWidth(interactionWidth)
			m.interaction.SetHeight(contentHeight)
		}
		// A shrink can drop the optional name row out of the form; typing
		// must not keep landing in a field that is no longer drawn.
		if m.mode == modeDispatch && m.formFocus == dispatchName &&
			!m.dispatchNameVisible() {
			m.formFocus = dispatchTask
			m.focusForm()
		}
		m.syncTaskComposerSize()
		if m.ptyEnabled {
			// Ensure re-reads the grid size and moves every window and
			// emulator with it.
			return m, m.ensurePTYCmd()
		}
		return m, tea.Batch(m.loadInteractionCmd(), m.ensurePTYCmd())

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
		if workspaceID == "" {
			// First refresh: land on the workspace a `stormlight <path>`
			// launch asked for.
			workspaceID = m.initialWorkspaceID
		}
		agentID := m.selectedAgentID()
		previous, _ := m.selectedAgent()
		if msg.err != nil {
			m.err = msg.err
			diagnostic.Logger().Error("dashboard refresh failed", "error", msg.err)
		} else {
			m.agents = msg.agents
			m.catalogWorkspaces = msg.workspaces
			m.rebuildGroups(workspaceID, agentID)
			m.initialWorkspaceID = ""
			if m.mode == modeCompose {
				if selected, ok := m.selectedAgent(); ok &&
					selected.ProcessLive && selected.Attention.TerminalOwned() {
					// A prompt arrived mid-compose: the agent can no longer
					// receive text, so the composer yields to the attention
					// band without waiting for an Esc. The draft stays in
					// the box.
					m.mode = modeNormal
					m.sendInput.Blur()
					m.status = "Agent needs input"
				}
			}
		}
		var cmds []tea.Cmd
		// Reconcile the terminal herd against the roster on every refresh:
		// new agents get sessions, deleted agents lose them, and when
		// nothing changed this is a cheap map diff.
		cmds = append(cmds, m.ensurePTYCmd())
		if m.anyAgentsActive() && !m.shimmerRunning {
			m.shimmerRunning = true
			cmds = append(cmds, shimmerTickCmd())
		}
		if !m.ptyEnabled && m.shouldReloadInteraction(previous) {
			m.interactionLoadedAt = time.Now()
			cmds = append(cmds, m.loadInteractionCmd())
		}
		if m.ptyEnabled {
			if selected, ok := m.selectedAgent(); ok &&
				!selected.ProcessLive && previous.ProcessLive &&
				selected.ID == previous.ID {
				// remain-on-exit keeps the pane, so the frame freezes on
				// the death screen; say why nothing moves anymore.
				m.status = "Process exited — terminal view frozen"
			}
		}
		if !m.restoreOffered && msg.err == nil {
			// An empty roster on the first listing is the shape a lost tmux
			// server leaves behind. Ask the snapshot whether it agrees, once:
			// the offer is for the dashboard you open after the reboot, not
			// for every moment you happen to have no agents running.
			m.restoreOffered = true
			if len(m.agents) == 0 && m.mode == modeNormal {
				cmds = append(cmds, restoreCandidatesCmd(m.backend, true))
			}
		}
		return m, tea.Batch(cmds...)

	case historyMsg:
		m.historyLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.historyRecords = msg.records
		m.historyCursor = clamp(m.historyCursor, 0, max(0, len(msg.records)-1))
		return m, nil

	case restoreCandidatesMsg:
		m.applyRestoreCandidates(msg)
		return m, nil

	case ptyEnsuredMsg:
		// Fresh sessions may include the selected agent's; listen to it.
		return m, m.armPTYWait()

	case spanreed.FrameMsg:
		return m.handlePTYFrame(msg)

	case interactionMsg:
		if msg.id == m.selectedAgentID() {
			if m.interactionID != msg.id {
				m.selectionActive = false
			}
			followOutput := m.interactionID != msg.id || m.interaction.AtBottom()
			previousOffset := m.interaction.YOffset()
			m.interactionID = msg.id
			if msg.err != nil {
				m.interaction.SetContent(errorStyle().Render(msg.err.Error()))
			} else {
				selected, _ := m.selectedAgent()
				m.interactionContent = cleanInteraction(
					msg.content,
					m.interaction.Width(),
					selected.Provider,
				)
				m.refreshSearch()
				m.interaction.SetContent(m.searchDecorated())
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
				return attachReturnedMsg{name: msg.name, err: err}
			})
		}
		m.err = nil
		m.status = "Attached"
		return m, nil

	case attachReturnedMsg:
		m.err = msg.err
		if msg.err != nil {
			m.status = "Action failed"
		} else {
			m.status = "Returned from " + msg.name
		}
		// The attached client owned the window sizes while it looked;
		// reassert the herd's 1:1 grid now that the dashboard is back.
		return m, tea.Batch(m.refreshCmd(), resizeAllPTYCmd(m.ptyManager))

	case actionMsg:
		m.err = msg.err
		if msg.err != nil {
			m.status = "Action failed"
			diagnostic.Logger().Error("dashboard action failed", "error", msg.err)
		} else {
			m.status = msg.status
		}
		return m, m.refreshCmd()

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
		m.syncTaskComposerSize()
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
		m.syncTaskComposerSize()
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

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.PasteMsg:
		return m.updatePaste(msg)

	case tea.KeyPressMsg:
		if m.err != nil {
			m.err = nil
			if m.status == "Action failed" {
				m.status = "Ready"
			}
		}
		if m.ptyEnabled && m.mode == modeNormal &&
			m.activePane == paneInteraction {
			// The Spanreed is not a preview: while it holds focus, the
			// keyboard belongs to the agent's terminal, exactly like a
			// focused terminal tab. ctrl+q is the one key that stays ours.
			return m.updateTerminalKey(msg)
		}
		switch m.mode {
		case modeDispatch:
			return m.updateDispatch(msg)
		case modeCompose:
			return m.updateCompose(msg)
		case modeSearch:
			return m.updateSearch(msg)
		case modeDelete:
			return m.updateDelete(msg)
		case modeAddWorkspace:
			return m.updateAddWorkspace(msg)
		case modeRename:
			return m.updateRename(msg)
		case modeMark:
			return m.updateMark(msg)
		case modeHistory:
			return m.updateHistory(msg)
		case modeRestore:
			return m.updateRestore(msg)
		case modeInfo, modeHelp:
			// Any key dismisses an informational overlay.
			m.mode = modeNormal
			m.status = "Ready"
			return m, nil
		default:
			// Reading the result is the presence proof: if an unseen result
			// is on screen right now (selected, transcript loaded) and the
			// human engages with it, it has been seen.
			seenID := ""
			if selected, ok := m.selectedAgent(); ok &&
				selected.ProcessLive &&
				(selected.Attention == agent.AttentionWaiting ||
					selected.EffectiveMark() == agent.MarkAttention) &&
				m.interactionID == selected.ID &&
				marksResultSeen(msg.String(), m.activePane) {
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

	if m.ptyEnabled && m.ready {
		// Whatever the dashboard did not claim may belong to the selected
		// terminal widget — its scroll-flush ticks arrive as unexported
		// message types. Foreign or unknown messages come back as no-ops.
		if widget, ok := m.selectedPTY(); ok {
			_, cmd := widget.Update(message)
			return m, cmd
		}
		return m, nil
	}
	if m.mode == modeNormal && m.ready &&
		m.activePane == paneInteraction {
		var cmd tea.Cmd
		m.interaction, cmd = m.interaction.Update(message)
		return m, cmd
	}
	return m, nil
}

// View draws the dashboard and, with it, declares the terminal state the
// dashboard needs.
//
// The alt screen and mouse reporting are fields here rather than commands
// sent from Update because they are properties of the view, not events. That
// distinction is what retires a standing workaround: an external attach or a
// suspended overlay runs through ExecProcess, which returns with mouse
// reporting off, and under v1 every message that could follow one had to
// remember to re-assert it. A field is re-asserted by every frame, so there
// is nothing left to forget.
func (m Model) View() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	if !m.ready {
		view.SetContent("\n  Loading...")
		return view
	}

	header := m.renderHeader()
	body := m.renderBody()
	footer := m.renderFooter()
	view.SetContent(lipgloss.JoinVertical(lipgloss.Left, header, body, footer))
	// The real terminal cursor sits where the agent's program put it —
	// the Spanreed is the terminal, not a picture of one.
	view.Cursor = m.ptyCursor()
	return view
}

func (m Model) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		return m, m.interactionFollowCmd()
	}
	if m.normalPrefix == "g" {
		m.normalPrefix = ""
		if key == "g" {
			m.moveSelectionToStart()
			return m, m.interactionFollowCmd()
		}
		return m, nil
	}

	switch key {
	case "g":
		m.normalPrefix = "g"
		return m, nil
	case ",":
		m.normalPrefix = ","
		return m, nil
	case "q", "ctrl+c":
		// No pane pipe or FIFO outlives the dashboard; the agents
		// themselves live on in tmux, terminals intact for the next run.
		return m, tea.Sequence(closeAllPTYCmd(m.ptyManager), tea.Quit)
	case "t":
		return m, m.togglePTY()
	case "F":
		// The full-screen escape hatch: the same terminal, every column
		// of the display, via an interactive attachment.
		if selected, ok := m.selectedAgent(); ok {
			displayTitle := agentDisplayTitle(selected)
			m.status = "Opening " + displayTitle
			return m, attachCmd(m.backend, selected.ID, displayTitle)
		}
	case "j", "down":
		m.moveSelection(1)
		return m, m.interactionFollowCmd()
	case "k", "up":
		m.moveSelection(-1)
		return m, m.interactionFollowCmd()
	case "home":
		m.moveSelectionToStart()
		return m, m.interactionFollowCmd()
	case "G", "end":
		m.moveSelectionToEnd()
		return m, m.interactionFollowCmd()
	case "ctrl+d":
		m.moveSelection(max(1, m.visibleRows()/2))
		return m, m.interactionFollowCmd()
	case "ctrl+u":
		m.moveSelection(-max(1, m.visibleRows()/2))
		return m, m.interactionFollowCmd()
	case "pgdown", "ctrl+f":
		m.moveSelection(m.visibleRows())
		return m, m.interactionFollowCmd()
	case "pgup", "ctrl+b":
		m.moveSelection(-m.visibleRows())
		return m, m.interactionFollowCmd()
	case "h", "left":
		if m.activePane > paneWorkspaces {
			m.activePane--
		}
		return m, nil
	case "l", "right":
		if m.activePane < paneInteraction {
			m.activePane++
		}
		return m, m.interactionFollowCmd()
	case "tab":
		m.activePane = (m.activePane + 1) % 3
		return m, m.interactionFollowCmd()
	case "shift+tab":
		m.activePane = (m.activePane + 2) % 3
		return m, m.interactionFollowCmd()
	case "z":
		m.rowsExpanded = !m.rowsExpanded
		return m, nil
	case "Z", "alt+z":
		// Zoom from the roster: the sidebars collapse and the keyboard
		// walks into the portal in the same stroke.
		if _, ok := m.selectedAgent(); ok {
			m.ptyEnabled = true
			m.ptyZoom = true
			m.activePane = paneInteraction
			m.status = "Terminal — ctrl+space to leave"
			return m, tea.Batch(m.ensurePTYCmd(), m.armPTYWait())
		}
		return m, nil
	case "ctrl+space", "ctrl+@":
		// The seam key works from this side too: in, symmetric with out.
		if m.ptyEnabled {
			if _, ok := m.selectedAgent(); ok {
				m.activePane = paneInteraction
				m.status = "Terminal — ctrl+space to leave"
				return m, m.armPTYWait()
			}
		}
		return m, nil
	case "<", ">":
		return m.resizeColumns(key)
	case "enter":
		if m.activePane == paneWorkspaces {
			m.activePane = paneAgents
			return m, m.interactionFollowCmd()
		}
		if m.ptyEnabled {
			// The terminal is right there — Enter walks into it. Z is the
			// zoomed version.
			if _, ok := m.selectedAgent(); ok {
				m.activePane = paneInteraction
				m.status = "Terminal — ctrl+space to leave"
				return m, m.armPTYWait()
			}
			return m, nil
		}
		if selected, ok := m.selectedAgent(); ok {
			displayTitle := agentDisplayTitle(selected)
			m.status = "Opening " + displayTitle
			return m, attachCmd(m.backend, selected.ID, displayTitle)
		}
	case "/":
		if m.activePane == paneInteraction && m.interactionSearchable() {
			m.beginSearch()
			return m, nil
		}
	case "n":
		if m.activePane == paneWorkspaces {
			return m.beginAddWorkspace()
		}
		if m.activePane == paneInteraction && m.search.query != "" {
			m.jumpSearchMatch(1)
			return m, nil
		}
		return m.beginDispatch(false)
	case "N":
		if m.activePane == paneInteraction && m.search.query != "" {
			m.jumpSearchMatch(-1)
			return m, nil
		}
	case "esc", "ctrl+[":
		if m.selectionActive {
			m.selectionActive = false
			return m, nil
		}
		if m.activePane == paneInteraction && m.search.query != "" {
			m.clearSearch()
			m.status = "Ready"
			return m, nil
		}
	case "o":
		return m.beginDispatch(true)
	case "i", "s":
		if m.ptyEnabled {
			// The live terminal is the composer; walking in is replying.
			if _, ok := m.selectedAgent(); ok {
				m.activePane = paneInteraction
				m.status = "Terminal — ctrl+q to leave"
				return m, m.armPTYWait()
			}
			return m, nil
		}
		if selected, ok := m.selectedAgent(); ok {
			if selected.ProcessLive && selected.Attention.TerminalOwned() {
				// An active prompt owns the agent's input; composing here
				// would type into it. The band already says where to go.
				m.err = fmt.Errorf(
					"agent is waiting on a prompt — Enter opens its terminal")
				return m, nil
			}
			m.activePane = paneInteraction
			m.mode = modeCompose
			// Whatever is in the box is a draft — including one parked
			// there when a prompt auto-closed the composer — so entering
			// again picks it back up rather than discarding it.
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
	case "ctrl+x":
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
	case "ctrl+r":
		return m.beginRestore()
	case "m":
		return m.beginMark()
	case "M":
		return m.clearAttention()
	case "K":
		if m.activePane == paneWorkspaces {
			if _, ok := m.selectedWorkspace(); ok {
				m.mode = modeInfo
				m.status = "Workspace info"
			}
			return m, nil
		}
	case "H":
		return m.beginHistory()
	case "?":
		m.mode = modeHelp
		m.status = "Keys"
		return m, nil
	}

	if m.activePane != paneInteraction || m.ptyEnabled {
		return m, nil
	}
	var cmd tea.Cmd
	m.interaction, cmd = m.interaction.Update(msg)
	return m, cmd
}

// marksResultSeen reports whether a keypress engages with the unseen result
// rather than merely happening near it. Acting on the agent counts from
// anywhere; paging through the transcript counts only while the transcript
// pane holds focus, because that is the one place those keys are reading
// rather than moving the cursor.
//
// Bare navigation is deliberately absent. `h` back out of the transcript,
// `l` into it, tab between panes, `j`/`k` down the agent list — every one of
// those is how a human leaves a result, and treating them as proof made
// amber clear on the way past. Nothing here is lost by waiting: `M` marks
// seen outright, and the amber costs nothing while it waits.
func marksResultSeen(key string, active pane) bool {
	switch key {
	case "enter", "i", "s", "x":
		// Opening the terminal, replying, and interrupting all act on the
		// result itself, wherever the cursor happens to be.
		return true
	}
	if active != paneInteraction {
		return false
	}
	switch key {
	case "j", "k", "up", "down",
		"ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "pgup", "pgdown",
		"g", "G",
		"/", "n", "N":
		return true
	}
	return false
}

func (m Model) anyAgentsActive() bool {
	return agent.Count(m.agents).Working > 0
}

// shimmerPhaseOrRest returns the sweep phase while the shimmer is running
// and the resting (uniform base shade) phase otherwise.
func (m Model) shimmerPhaseOrRest() int {
	if m.shimmerRunning {
		return m.shimmerPhase
	}
	return -1
}
