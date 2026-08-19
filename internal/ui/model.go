package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/ptyview"
	"github.com/trentkm/stormlight/internal/theme"
	"github.com/trentkm/stormlight/internal/workspace"
)

type Backend interface {
	ListAgents(context.Context) ([]agent.Agent, error)
	ListWorkspaces(context.Context) ([]workspace.Context, error)
	ListWorkspaceRoots(context.Context) ([]workspace.Context, error)
	AddWorkspace(ctx context.Context, host, path string) (workspace.Context, error)
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
	// AttachTerminal is the transport behind the live terminal view: one
	// attachment per agent, seed plus stream plus input and resize.
	AttachTerminal(ctx context.Context, id string, cols, rows int) (pty.Transport, error)
	// StartOverlay floats a short-lived interactive program (the picker,
	// the task editor) on a runtime-owned PTY for the popup to render.
	StartOverlay(ctx context.Context, request app.OverlayRequest) (app.Overlay, error)
	Providers() []provider.Info
	SessionHistory(context.Context) ([]history.Record, error)
	Resume(context.Context, history.Record) (agent.Agent, error)
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
	modeAlert
)

// isForm reports that a mode is one the human is filling in — a field, a
// path, a task. Those own their complaints: the objection dies with the
// form. The informational overlays are not forms; any key closes them, so
// treating that key as an answer to a failure would be the keystroke-wipe
// this whole surface exists to remove.
func (m mode) isForm() bool {
	switch m {
	case modeDispatch, modeAddWorkspace, modeRename, modeCompose:
		return true
	}
	return false
}

// isReading reports an overlay that is read once and closes on any key.
// Those are sized to their own content, so shrinking them to make room for
// the card cuts their last lines off with nothing to scroll them back —
// the card floats over them instead.
//
// Everything else makes room, including the surfaces that merely look
// passive: Sessions is a list the reader moves through, filters and
// resumes from, and the detail view scrolls. A card over either covers
// rows they need, and Esc there belongs to the surface, not the card.
func (m mode) isReading() bool {
	switch m {
	case modeHelp, modeInfo:
		return true
	}
	return false
}

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

// addWorkspaceTab is which half of the Add Workspace modal is showing.
// Local is the common answer and the one it opens on.
type addWorkspaceTab int

const (
	tabLocal addWorkspaceTab = iota
	tabRemote
)

// machineCheck is the state of asking a machine whether it can host
// agents at all.
//
// Reaching another machine takes as long as SSH takes, which is not
// instant and is sometimes forever. Asking when a machine is opened —
// rather than when someone finally presses Browse — is what lets the
// answer be "this machine is not set up yet" instead of a picker that
// hangs and then fails.
type machineCheck struct {
	host    string
	running bool
	// err is why the machine could not be reached or read.
	err error
	// ready says Stormlight is installed there; without it nothing else
	// on that machine can work.
	ready bool
	// yazi says it can be browsed. Its absence costs browsing and
	// nothing else, so it is a note rather than a refusal.
	yazi   bool
	detail string
}

// HostStatus is what a machine reported about itself.
type HostStatus struct {
	Ready  bool
	Yazi   bool
	Detail string
}

type machineCheckedMsg struct {
	host   string
	status HostStatus
	err    error
}

// machineChoice is one row of the Remote tab: a machine from the user's
// SSH configuration, or the row for naming one it does not list.
type machineChoice struct {
	kind    machineChoiceKind
	name    string
	detail  string
	summary string
}

type machineChoiceKind int

const (
	machineHost machineChoiceKind = iota
	machineTyped
)

type directoryChoiceKind int

const (
	directoryPath directoryChoiceKind = iota
	directoryYazi
	directoryCustom
	// directorySetup is not a directory at all: it is the row that
	// prepares the machine so the others can work.
	directorySetup
)

type directoryChoice struct {
	kind directoryChoiceKind
	// detail overrides the row's right-hand column, for rows whose
	// meaning changes with the machine they act on.
	detail string
	// host is the machine the path is on; empty is this one. Two
	// machines can offer the same path, and they are different choices.
	host          string
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

	agents            []agent.Agent
	catalogWorkspaces []workspace.Context
	workspaceRoots    []workspace.Context
	groups            []workspaceGroup
	// machineState is what the machine being opened said about itself,
	// and whether it has said anything yet.
	machineState machineCheck
	// The Add Workspace modal: which tab is showing, which machine the
	// Remote tab has been opened on, and the machines it can offer.
	addWorkspaceTab  addWorkspaceTab
	addWorkspaceHost string
	machines         []machineChoice
	machineIndex     int
	hostInput        lineInput
	checkHost        func(context.Context, string) (HostStatus, error)
	// dispatchHost is the machine the open dispatch form is aimed at. It
	// travels with the directory rather than being asked for separately:
	// a path only means anything alongside the machine it is on.
	dispatchHost    string
	workspaceCursor int
	agentCursor     int
	activePane      pane
	rowsExpanded    bool
	interaction     viewport.Model
	width           int
	height          int
	ready           bool

	mode               mode
	formFocus          dispatchFocus
	providers          []provider.Info
	providerIndex      int
	cwdInput           lineInput
	nameInput          lineInput
	taskInput          textarea.Model
	sendInput          textarea.Model
	initialCwd         string
	initialWorkspaceID string
	interactionContent string
	search             transcriptSearch
	selectionActive    bool
	selectionDragging  bool
	selectionAnchor    int
	selectionHead      int
	directories        []directoryChoice
	directoryIndex     int
	yaziPath           string
	nvimPath           string
	dispatchMode       agent.PermissionMode
	modeForDir         func(string) (agent.PermissionMode, bool)
	providerForDir     func(string) (agent.Provider, bool)
	renameInput        lineInput
	renameAgentID      string
	renameWorkspace    workspace.Context
	markAgentID        string
	markIndex          int
	historyRecords     []history.Record
	historyCursor      int
	historyLoading     bool
	// historyFailed is why the shelf is empty, when it is empty because
	// nothing could be read rather than because nothing is there.
	historyFailed           error
	historyFilter           lineInput
	historyFiltering        bool
	pathNav                 pathNav
	pickerStart             string
	chooseDispatchDirectory bool

	interactionID       string
	interactionLoadedAt time.Time
	// The failure surface. See alert.go.
	alert alert
	// lastFailure is the most recent failure's full text, kept past its
	// card so `e` can still reach it; alertDetail is the copy an open
	// detail view is reading, which nothing may overwrite mid-read.
	lastFailure alertDetail
	alertDetail alertDetail
	// hushedPoll is a self-repeating failure the reader has dismissed,
	// silenced until the poll recovers. heldBack is one that timed out
	// where no key could answer it — inside the portal — kept back only
	// until the reader is somewhere they can.
	hushedPoll     string
	heldBack       string
	shimmerPhase   int
	shimmerRunning bool

	normalPrefix   string
	sortMode       sortMode
	dispatchPrefix string
	columns        ColumnPrefs

	// The PTY Spanreed. The manager keeps one live terminal session per
	// agent for the agent's whole life; ptyEnabled says which view the
	// Spanreed renders (terminal by default, transcript after `t`), and
	// ptyWaiting reports one outstanding frame-gate listener so
	// redraw wake-ups never double up.
	ptyEnabled bool
	// ptyZoom collapses the sidebars so the portal spans the full body —
	// still the same layout pipeline, not a separate mode. It survives a
	// trip through the transcript view: t out, t back, still zoomed.
	ptyZoom    bool
	ptyManager *ptyview.Manager
	ptyWaiting bool
	// keys are the seam chords, config-rebindable; see KeyBindings.
	keys KeyBindings

	// Terminal drag selection: character-precise over the grid, tmux
	// style — first line from the anchor, middles whole, last to the
	// head — copied on release.
	ptySelecting  bool
	ptySelDragged bool
	ptySelAnchor  gridCell
	ptySelHead    gridCell

	// The floating program overlay (Yazi picker, Neovim task editor): a
	// windrunner session rendered through the same widget machinery as
	// the Spanreed, composited over the dashboard. The generation ties
	// async open/exit messages to the opening they belong to.
	overlay           *overlayView
	overlayGeneration int
}

// machineChoices is the Remote tab's rows: what the SSH configuration
// names, and then the row for a machine it does not. Naming one is what
// makes it usable, so the typed row is always there — most people's
// configuration names nothing at all.
func machineChoices(hosts []HostChoice) []machineChoice {
	choices := make([]machineChoice, 0, len(hosts)+1)
	for _, host := range hosts {
		choices = append(choices, machineChoice{
			kind:    machineHost,
			name:    host.Name,
			detail:  "SSH",
			summary: host.Summary,
		})
	}
	return append(choices, machineChoice{
		kind:   machineTyped,
		name:   "Type a destination…",
		detail: "SSH",
	})
}

type dashboardMsg struct {
	agents     []agent.Agent
	workspaces []workspace.Context
	roots      []workspace.Context
	err        error
}

type interactionMsg struct {
	id      string
	content string
	err     error
}

type actionMsg struct {
	err error
}

type attachMsg struct {
	result app.AttachResult
	name   string
	err    error
}

type directoryPickedMsg struct {
	// host is the machine the picker browsed; empty is this one. It
	// travels with the path because a path alone does not say where it
	// is, and the two are one answer.
	host string
	path string
	err  error
}

// machinePreparedMsg reports the end of a host setup run.
type machinePreparedMsg struct {
	host string
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

type tickMsg time.Time
type shimmerTickMsg time.Time

func NewModel(backend Backend) Model {
	return NewModelWithOptions(backend, Options{})
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
	// Keys rebinds the seam chords; empty lists keep the defaults.
	Keys KeyBindings
	// CheckHost asks a machine what it has. It is what the Add Workspace
	// modal waits on before offering to browse one.
	CheckHost func(context.Context, string) (HostStatus, error)
	// Hosts are the machines to offer when adding a workspace, in the
	// order they should appear — read from the user's SSH configuration.
	// This machine is not among them; it has its own tab.
	Hosts []HostChoice
}

// HostChoice is a machine the Add Workspace modal can offer: the name
// Stormlight will call it, and what its configuration says it is.
type HostChoice struct {
	Name    string
	Summary string
}

func NewModelWithOptions(backend Backend, options Options) Model {
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
		columns:            options.Columns,
		ptyEnabled:         true,
		ptyManager:         ptyview.NewManager(backend),
		keys:               fillKeyDefaults(options.Keys),
		machines:           machineChoices(options.Hosts),
		checkHost:          options.CheckHost,
		hostInput:          newLineInput("user@host"),
	}
	for index, info := range model.providers {
		if info.ID == options.DefaultProvider {
			model.providerIndex = index
			break
		}
	}
	model.prepareDirectoryChoices("", cwd)
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.refreshCmd(),
		tickCmd(),
		shimmerTickCmd(),
		// Ask the terminal what it is painted on. Lip Gloss v1 answered this
		// itself by querying behind the program's back on first use; v2 does
		// not, so the palette runs on its dark default until this comes back.
		tea.RequestBackgroundColor,
	)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.ptyEnabled && m.ptyManager != nil {
		// The coalesced wheel tick goes to the terminal that scheduled
		// it — selection may have moved since; an unrouted tick would
		// latch that terminal's wheel shut.
		if widgetID, ok := pty.ScrollOwner(message); ok {
			if widget, ok := m.ptyManager.WidgetByID(widgetID); ok {
				widget.Handle(message)
			}
			return m, nil
		}
	}
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
		m.syncTaskComposerSize()
		m.refitForms()
		if m.overlay != nil {
			outerWidth, outerHeight := m.overlayDimensions()
			_, resize := m.overlay.widget.SetSize(
				max(2, outerWidth-2), max(2, outerHeight-2))
			if resize != nil {
				return m, tea.Batch(resize, m.ensurePTYCmd())
			}
		}
		if m.ptyEnabled {
			// Ensure re-reads the grid size and moves every window and
			// emulator with it.
			return m, m.ensurePTYCmd()
		}
		return m, tea.Batch(m.loadInteractionCmd(), m.ensurePTYCmd())

	case tickMsg:
		m.ageAlert(time.Time(msg))
		return m, tea.Batch(m.refreshCmd(), tickCmd())

	case machineCheckedMsg:
		// A late answer for a machine nobody is looking at any more is
		// not an answer to anything.
		if m.machineState.host != msg.host {
			return m, nil
		}
		m.machineState = machineCheck{
			host:   msg.host,
			err:    msg.err,
			ready:  msg.status.Ready,
			yazi:   msg.status.Yazi,
			detail: msg.status.Detail,
		}
		m.setAddWorkspaceChoices()
		return m, nil

	case shimmerTickMsg:
		if !m.anyAgentsActive() && !m.machineState.running {
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
			m.raisePolled(msg.err)
			diagnostic.Logger().Error("dashboard refresh failed", "error", msg.err)
		} else {
			// The poll that failed is the poll that reports its own
			// recovery; a card still insisting the daemon is down after
			// it came back is worse than no card.
			m.resolvePolled()
			m.agents = msg.agents
			m.catalogWorkspaces = msg.workspaces
			m.workspaceRoots = msg.roots
			m.rebuildGroups(workspaceID, agentID)
			m.initialWorkspaceID = ""
			if m.mode == modeCompose {
				if selected, ok := m.selectedAgent(); ok &&
					selected.ProcessLive && selected.Attention.TerminalOwned() {
					// A prompt arrived mid-compose: the agent can no longer
					// receive text, so the composer yields to the attention
					// band without waiting for an Esc. The draft stays in
					// the box.
					//
					// The composer's own objection goes with it. This close
					// arrives on a message rather than a keystroke, so the
					// sweep in Update never sees it.
					m.clearComplaint(modeCompose)
					m.mode = modeNormal
					m.sendInput.Blur()
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
		return m, tea.Batch(cmds...)

	case historyMsg:
		m.historyLoading = false
		m.historyFailed = msg.err
		if msg.err != nil {
			m.raise(msg.err)
			return m, nil
		}
		m.historyRecords = msg.records
		m.historyCursor = clamp(m.historyCursor, 0, max(0, len(msg.records)-1))
		return m, nil

	case ptyEnsuredMsg:
		// Fresh sessions may include the selected agent's; listen to it.
		return m, m.armPTYWait()

	case pty.FrameMsg:
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
			m.raise(msg.err)
			diagnostic.Logger().Error("dashboard open failed",
				"agent", msg.name,
				"error", msg.err,
			)
			return m, nil
		}
		if msg.result.Command != nil {
			return m, tea.ExecProcess(msg.result.Command, func(err error) tea.Msg {
				if err != nil {
					diagnostic.Logger().Error("external attach failed",
						"agent", msg.name,
						"error", err,
					)
				}
				return attachReturnedMsg{name: msg.name, err: err}
			})
		}
		return m, nil

	case attachReturnedMsg:
		if msg.err != nil {
			m.raise(msg.err)
		}
		// The attached client owned the window sizes while it looked;
		// reassert the herd's 1:1 grid now that the dashboard is back.
		return m, tea.Batch(m.refreshCmd(), resizeAllPTYCmd(m.ptyManager))

	case actionMsg:
		// Only a failure speaks here. Succeeding at something else is not
		// an answer to the card standing unread from the last failure —
		// copying a selection used to wipe it.
		if msg.err != nil {
			m.raise(msg.err)
			diagnostic.Logger().Error("dashboard action failed", "error", msg.err)
		}
		return m, m.refreshCmd()

	case directoryPickedMsg:
		if msg.err != nil {
			m.raise(msg.err)
			diagnostic.Logger().Error("directory picker failed", "error", msg.err)
			return m, nil
		}
		if msg.path == "" {
			return m, nil
		}
		if m.mode == modeAddWorkspace {
			m.cwdInput.SetValue(msg.path)
			return m, addWorkspaceCmd(m.backend, msg.host, msg.path)
		}
		m.prepareDirectoryChoices(msg.host, msg.path)
		m.cwdInput.SetValue(msg.path)
		m.formFocus = dispatchTask
		m.focusForm()
		m.syncTaskComposerSize()
		return m, nil

	case machinePreparedMsg:
		if msg.err != nil {
			m.raise(fmt.Errorf("set up %s: %w", msg.host, msg.err))
		}
		return m, nil

	case taskEditedMsg:
		if msg.err != nil {
			m.raise(msg.err)
			diagnostic.Logger().Error("task editor failed", "error", msg.err)
			return m, nil
		}
		m.taskInput.SetValue(msg.task)
		m.formFocus = dispatchTask
		m.focusForm()
		m.syncTaskComposerSize()
		m.clearComplaint(modeDispatch)
		return m, nil

	case workspaceAddedMsg:
		if msg.err != nil {
			m.raise(msg.err)
			diagnostic.Logger().Error("add workspace failed", "error", msg.err)
			return m, nil
		}
		// The form's objection to a path is disproved by a path that
		// worked. Its own close arrives here rather than on a keystroke,
		// so the keypress sweep never sees it.
		m.clearComplaint(modeAddWorkspace)
		m.mode = modeNormal
		m.blurForm()
		m.catalogWorkspaces = appendWorkspace(m.catalogWorkspaces, msg.value)
		m.rebuildGroups(msg.value.ID, "")
		m.activePane = paneWorkspaces
		return m, nil

	case overlayOpenedMsg:
		return m.handleOverlayOpened(msg)

	case overlayExitedMsg:
		return m.handleOverlayExited(msg)

	case tea.MouseMsg:
		if m.overlay != nil {
			// The popup owns the screen; clicks must not move the panes
			// beneath it.
			return m, nil
		}
		return m.handleMouse(msg)

	case tea.PasteMsg:
		if m.ptyEnabled && m.mode == modeNormal &&
			m.activePane == paneInteraction {
			// Paste lands in the agent's terminal as one bracketed paste,
			// the same shape Send uses — never a burst of keystrokes.
			if widget, ok := m.selectedPTY(); ok {
				widget.ScrollToBottom()
				return m, writeTerminalCmd(widget,
					[]byte("\x1b[200~"+msg.Content+"\x1b[201~"))
			}
			return m, nil
		}
		if m.overlay != nil {
			// Bracketed paste, so a multi-line paste lands in the hosted
			// program as one paste rather than a burst of keys.
			return m, writeTerminalCmd(m.overlay.widget,
				[]byte("\x1b[200~"+msg.Content+"\x1b[201~"))
		}
		return m.updatePaste(msg)

	case tea.KeyPressMsg:
		// A failure card is not swept away by the next keystroke — it is
		// read, dismissed, or replaced. The one keystroke that does end it
		// is the one that closes the form it was raised in: a complaint
		// about a name or a path has nothing left to say once the field
		// is gone. A card the dashboard itself raised belongs to no form,
		// so it survives a trip through the help and back.
		// Compared by message rather than by error value: two interfaces
		// holding the same non-comparable dynamic type panic on ==, and a
		// dependency's value-typed error would take the dashboard down on
		// the next keypress.
		standing, raisedIn, mode := m.alert.message(), m.alert.raisedIn, m.mode
		updated, cmd := m.updateKey(msg)
		if model, ok := updated.(Model); ok &&
			standing != "" && model.alert.message() == standing &&
			mode == raisedIn && raisedIn.isForm() && model.mode != mode {
			model.retract()
			return model, cmd
		}
		return updated, cmd
	}

	if m.ptyEnabled && m.ready {
		// Everything a terminal owns has already been handled above. An
		// unclaimed message must not leak into the transcript surface.
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

// updateKey routes one keypress to whatever owns the keyboard: a floating
// program, the portal, the mode's own handler, or the dashboard itself.
func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.overlay != nil {
		// The floating program owns the keyboard while it is up;
		// ctrl+q cancels it.
		return m.updateOverlayKey(msg)
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
	case modeAlert:
		return m.updateAlertDetail(msg)
	case modeInfo, modeHelp:
		// Any key dismisses an informational overlay.
		m.mode = modeNormal
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
	// The seam chords work from this side too: the same keys that cycle
	// inside the terminal cycle here, so the hands never relearn.
	switch {
	case slices.Contains(m.keys.QueueNext, key):
		return m.jumpQueue(agent.QueueForward)
	case slices.Contains(m.keys.QueuePrevious, key):
		return m.jumpQueue(agent.QueueBack)
	case slices.Contains(m.keys.AgentsNext, key):
		m.moveSelectionIn(paneAgents, 1)
		return m, m.interactionFollowCmd()
	case slices.Contains(m.keys.AgentsPrevious, key):
		m.moveSelectionIn(paneAgents, -1)
		return m, m.interactionFollowCmd()
	case slices.Contains(m.keys.Zoom, key):
		if _, ok := m.selectedAgent(); ok {
			m.ptyEnabled = true
			m.ptyZoom = true
			m.activePane = paneInteraction
			return m, m.ensurePTYCmd()
		}
	}
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
			return m, nil
		}
		m.sortMode = mode
		m.rebuildGroups(m.selectedWorkspaceID(), m.selectedAgentID())
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
		// The agents themselves live on in the windrunner daemon,
		// terminals intact for the next run.
		return m, tea.Sequence(closeAllPTYCmd(m.ptyManager), tea.Quit)
	case "t":
		return m, m.togglePTY()
	case "F":
		// The full-screen escape hatch: the same terminal, every column
		// of the display, via an interactive attachment.
		if selected, ok := m.selectedAgent(); ok {
			displayTitle := agentDisplayTitle(selected)
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
	case "Z":
		// Zoom from the roster: the sidebars collapse and the keyboard
		// walks into the portal in the same stroke.
		if _, ok := m.selectedAgent(); ok {
			m.ptyEnabled = true
			m.ptyZoom = true
			m.activePane = paneInteraction
			return m, tea.Batch(m.ensurePTYCmd(), m.armPTYWait())
		}
		return m, nil
	case "ctrl+space", "ctrl+@":
		// The seam key works from this side too: in, symmetric with out.
		if m.ptyEnabled {
			if _, ok := m.selectedAgent(); ok {
				m.activePane = paneInteraction
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
				return m, m.armPTYWait()
			}
			return m, nil
		}
		if selected, ok := m.selectedAgent(); ok {
			displayTitle := agentDisplayTitle(selected)
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
		// The card is last in line. Esc means what it always meant —
		// drop the selection, clear the search — and only once there is
		// nothing else to undo does it answer the card. A failure that
		// stands for as long as the daemon is down must not quietly
		// redefine the key for that whole time.
		if m.selectionActive {
			m.selectionActive = false
			return m, nil
		}
		if m.activePane == paneInteraction && m.search.query != "" {
			m.clearSearch()
			return m, nil
		}
		if m.alert.active() {
			// The card waited to be read; Esc is the reader saying so.
			m.dismissAlert()
			return m, nil
		}
	case "o":
		return m.beginDispatch(true)
	case "i", "s":
		if m.ptyEnabled {
			// The live terminal is the composer; walking in is replying.
			if _, ok := m.selectedAgent(); ok {
				m.activePane = paneInteraction
				return m, m.armPTYWait()
			}
			return m, nil
		}
		if selected, ok := m.selectedAgent(); ok {
			if selected.ProcessLive && selected.Attention.TerminalOwned() {
				// An active prompt owns the agent's input; composing here
				// would type into it. The band already says where to go.
				m.raise(fmt.Errorf(
					"agent is waiting on a prompt — Enter opens its terminal"))
				return m, nil
			}
			m.activePane = paneInteraction
			m.mode = modeCompose
			// Whatever is in the box is a draft — including one parked
			// there when a prompt auto-closed the composer — so entering
			// again picks it back up rather than discarding it.
			m.syncComposerSize()
			m.sendInput.Focus()
			m.clearComplaint(modeCompose)
			return m, nil
		}
	case "x":
		if selected, ok := m.selectedAgent(); ok {
			return m, actionCmd("Interrupted", func(ctx context.Context) error {
				return m.backend.Interrupt(ctx, selected.ID)
			})
		}
	case "ctrl+x":
		// The hint row names the victim while modeDelete holds; nothing
		// else to announce here.
		if m.activePane == paneWorkspaces {
			if _, ok := m.selectedWorkspace(); !ok {
				return m, nil
			}
			m.mode = modeDelete
			return m, nil
		}
		if _, ok := m.selectedAgent(); ok {
			m.mode = modeDelete
			return m, nil
		}
	case "r", "ctrl+l":
		// Asking again is asking to be told. A hushed failure that is
		// still failing has to answer an explicit refresh, or the retry
		// looks exactly like success.
		m.hushedPoll = ""
		return m, tea.Batch(m.refreshCmd(), m.loadInteractionCmd())
	case "R":
		return m.beginRename()
	case "m":
		return m.beginMark()
	case "M":
		return m.clearAttention()
	case "K":
		if m.activePane == paneWorkspaces {
			if _, ok := m.selectedWorkspace(); ok {
				m.mode = modeInfo
			}
			return m, nil
		}
	case "H":
		return m.beginHistory()
	case "e":
		// Not only while a card stands: the failures hardest to read are
		// the ones whose card is already gone.
		if m.readableFailure() {
			return m.openAlertDetail()
		}
	case "?":
		m.mode = modeHelp
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
// startShimmer begins the animation tick if it is not already running.
// It drives the working glow and the machine-check spinner both, so
// whichever starts first carries the other.
func (m *Model) startShimmer() tea.Cmd {
	if m.shimmerRunning {
		return nil
	}
	m.shimmerRunning = true
	return shimmerTickCmd()
}

func (m Model) shimmerPhaseOrRest() int {
	if m.shimmerRunning {
		return m.shimmerPhase
	}
	return -1
}
