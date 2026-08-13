package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/resurrect"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

type DispatchRequest struct {
	Provider agent.Provider
	Name     string
	Task     string
	Cwd      string
	Mode     agent.PermissionMode
}

type AttachResult = session.AttachResult

type Service struct {
	runtime    session.Runtime
	providers  *provider.Registry
	workspaces *workspace.Registry
	catalog    *workspace.Catalog
	// sessions is the permanent conversation log: every session id the
	// providers ever reported, kept so a conversation can be reopened long
	// after its window is gone.
	sessions *history.Log
	// store keeps the roster on disk so it outlives the runtime hosting it.
	// A nil store is a service that does not remember, which is what tests
	// and one-shot commands want.
	store *resurrect.Store

	// resolved caches catalog-path resolution (one git spawn per path per
	// call otherwise); the dashboard polls fast, directories change slowly.
	// Keyed by catalog path, so added or removed workspaces never consult
	// a stale entry.
	resolveMu sync.Mutex
	resolved  map[string]resolvedWorkspace
}

type resolvedWorkspace struct {
	value workspace.Context
	at    time.Time
}

// workspaceResolveTTL bounds how stale a cached resolution can get; a
// checkout converted to a worktree (or similar) is noticed within this.
const workspaceResolveTTL = 10 * time.Second

func NewService(
	runtime session.Runtime,
	providers *provider.Registry,
	workspaces *workspace.Registry,
) *Service {
	return NewServiceWithCatalog(
		runtime,
		providers,
		workspaces,
		workspace.NewCatalog(),
		history.NewLog(),
	)
}

func NewServiceWithCatalog(
	runtime session.Runtime,
	providers *provider.Registry,
	workspaces *workspace.Registry,
	catalog *workspace.Catalog,
	sessions *history.Log,
) *Service {
	if workspaces == nil {
		workspaces = workspace.NewRegistry()
	}
	if catalog == nil {
		catalog = workspace.NewCatalog()
	}
	if sessions == nil {
		sessions = history.NewLog()
	}
	return &Service{
		runtime:    runtime,
		providers:  providers,
		workspaces: workspaces,
		catalog:    catalog,
		sessions:   sessions,
	}
}

// Remembering gives the service a snapshot store, which is what turns a
// roster into something that survives its runtime. It is opt-in because
// remembering means writing to the user's state directory, and a service
// built for a test or a single query has no business doing that.
func (s *Service) Remembering(store *resurrect.Store) *Service {
	s.store = store
	return s
}

func (s *Service) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for index := range agents {
		if agents[index].Workspace.IsComplete() {
			continue
		}
		value, resolveErr := s.workspaces.Resolve(ctx, agents[index].Cwd)
		if resolveErr != nil {
			diagnostic.Logger().Warn("legacy agent workspace resolution failed",
				"agent_id", agents[index].ID,
				"path", agents[index].Cwd,
				"error", resolveErr,
			)
			continue
		}
		agents[index].Workspace = value
		if persistErr := s.runtime.SetWorkspace(
			ctx,
			agents[index].ID,
			value,
		); persistErr != nil {
			diagnostic.Logger().Warn("legacy agent workspace backfill failed",
				"agent_id", agents[index].ID,
				"error", persistErr,
			)
		}
	}
	contexts := make([]workspace.Context, len(agents))
	for index := range agents {
		contexts[index] = agents[index].Workspace
	}
	s.applyWorkspaceNames(contexts)
	for index := range agents {
		agents[index].Workspace = contexts[index]
	}
	s.publishStatus(ctx, agents)
	s.snapshot(ctx, agents)
	return agents, nil
}

// publishStatus hands the runtime's own chrome the tally the dashboard is
// about to draw. It rides on the listing rather than on state changes
// because a listing is the only thing that notices a pane dying, and the
// dashboard polls it — so whatever the dashboard knows, the status bar
// knows a moment later. Best-effort: a bar that cannot be written is not a
// reason to fail the listing behind it.
func (s *Service) publishStatus(ctx context.Context, agents []agent.Agent) {
	publisher, ok := s.runtime.(session.StatusPublisher)
	if !ok {
		return
	}
	if err := publisher.PublishStatus(ctx, agent.Count(agents)); err != nil {
		diagnostic.Logger().Debug("status bar tally not published", "error", err)
	}
}

func (s *Service) Dispatch(ctx context.Context, req DispatchRequest) (agent.Agent, error) {
	req.Task = strings.TrimSpace(req.Task)
	if req.Mode == "" {
		req.Mode = agent.DefaultMode
	}
	launch, err := s.providers.Resolve(req.Provider, req.Task, req.Mode)
	if err != nil {
		return agent.Agent{}, err
	}
	workspaceContext, err := s.workspaces.Resolve(ctx, req.Cwd)
	if err != nil {
		return agent.Agent{}, fmt.Errorf("resolve workspace: %w", err)
	}
	managedAgent, err := s.runtime.Dispatch(ctx, session.DispatchRequest{
		Provider:  req.Provider,
		Name:      req.Name,
		Task:      req.Task,
		Cwd:       req.Cwd,
		Mode:      req.Mode,
		Launch:    launch,
		Workspace: workspaceContext,
	})
	if err != nil {
		return agent.Agent{}, err
	}
	if err := s.catalog.Add(workspaceContext.Root); err != nil {
		diagnostic.Logger().Warn("workspace catalog update failed",
			"path", workspaceContext.Root,
			"error", err,
		)
	}
	s.resnapshot(ctx)
	return managedAgent, nil
}

// Resume reopens a recorded provider session as a new managed agent: same
// conversation, fresh window. The record supplies everything a dispatch
// asks for — task, cwd, mode — so the resumed agent lands in the workspace
// it left, and its hooks re-report whatever session id the provider
// assigns the continuation.
func (s *Service) Resume(
	ctx context.Context,
	record history.Record,
) (agent.Agent, error) {
	mode := record.Mode
	if mode == "" {
		mode = agent.DefaultMode
	}
	launch, err := s.providers.Resume(record.Provider, record.SessionID, mode)
	if err != nil {
		return agent.Agent{}, err
	}
	task := strings.TrimSpace(record.Task)
	if task == "" {
		task = "Resume session " + record.SessionID
	}
	workspaceContext, err := s.workspaces.Resolve(ctx, record.Cwd)
	if err != nil {
		return agent.Agent{}, fmt.Errorf("resolve workspace: %w", err)
	}
	return s.runtime.Dispatch(ctx, session.DispatchRequest{
		Provider:  record.Provider,
		Name:      record.Name,
		Task:      task,
		Cwd:       record.Cwd,
		Mode:      mode,
		Launch:    launch,
		Workspace: workspaceContext,
	})
}

func (s *Service) ListWorkspaces(ctx context.Context) ([]workspace.Context, error) {
	paths, err := s.catalog.Paths()
	if err != nil {
		return nil, err
	}
	values := make([]workspace.Context, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		value, resolveErr := s.resolveCached(ctx, path)
		if resolveErr != nil {
			diagnostic.Logger().Warn("catalog workspace resolution failed",
				"path", path,
				"error", resolveErr,
			)
			continue
		}
		if seen[value.ID] {
			continue
		}
		seen[value.ID] = true
		values = append(values, value)
	}
	s.applyWorkspaceNames(values)
	slices.SortStableFunc(values, func(a, b workspace.Context) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return values, nil
}

func (s *Service) resolveCached(
	ctx context.Context,
	path string,
) (workspace.Context, error) {
	s.resolveMu.Lock()
	cached, ok := s.resolved[path]
	s.resolveMu.Unlock()
	if ok && time.Since(cached.at) < workspaceResolveTTL {
		return cached.value, nil
	}
	value, err := s.workspaces.Resolve(ctx, path)
	if err != nil {
		return workspace.Context{}, err
	}
	s.resolveMu.Lock()
	if s.resolved == nil {
		s.resolved = map[string]resolvedWorkspace{}
	}
	s.resolved[path] = resolvedWorkspace{value: value, at: time.Now()}
	s.resolveMu.Unlock()
	return value, nil
}

// applyWorkspaceNames overlays user-chosen display names from the catalog.
func (s *Service) applyWorkspaceNames(values []workspace.Context) {
	names, err := s.catalog.Names()
	if err != nil {
		diagnostic.Logger().Warn("workspace names unavailable", "error", err)
		return
	}
	if len(names) == 0 {
		return
	}
	for index := range values {
		if name, ok := names[values[index].Root]; ok {
			values[index].Name = name
		}
	}
}

func (s *Service) AddWorkspace(ctx context.Context, path string) (workspace.Context, error) {
	value, err := s.workspaces.Resolve(ctx, path)
	if err != nil {
		return workspace.Context{}, err
	}
	if err := s.catalog.Add(value.Root); err != nil {
		return workspace.Context{}, err
	}
	return value, nil
}

func (s *Service) RemoveWorkspace(_ context.Context, value workspace.Context) error {
	return s.catalog.Remove(value.Root)
}

func (s *Service) RenameWorkspace(
	_ context.Context,
	value workspace.Context,
	name string,
) error {
	return s.catalog.SetName(value.Root, name)
}

func (s *Service) Rename(ctx context.Context, id, name string) error {
	return s.runtime.Rename(ctx, id, name)
}

func (s *Service) Capture(ctx context.Context, id string, lines int) (string, error) {
	if rendered, ok := s.transcriptCapture(ctx, id, lines); ok {
		return rendered, nil
	}
	return s.runtime.Capture(ctx, id, lines)
}

// transcriptCapture renders the conversation from the provider's own
// transcript file when the agent's hooks have reported one. The terminal
// screen is all tmux can see of an alternate-screen agent, so the
// transcript file is the only complete history; the live screen is
// appended while a turn is in flight so streaming output stays visible.
func (s *Service) transcriptCapture(ctx context.Context, id string, lines int) (string, bool) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return "", false
	}
	for _, managedAgent := range agents {
		if managedAgent.ID != id {
			continue
		}
		if managedAgent.TranscriptPath == "" {
			return "", false
		}
		rendered, ok := provider.RenderClaudeTranscript(managedAgent.TranscriptPath)
		if !ok {
			return "", false
		}
		busy := managedAgent.Activity == agent.ActivityWorking ||
			managedAgent.Attention.Urgent()
		if busy && managedAgent.ProcessLive {
			if live, err := s.runtime.Capture(ctx, id, lines); err == nil {
				rendered += "\n" + provider.LiveDivider() + "\n" + live
			}
		}
		return rendered, true
	}
	return "", false
}

func (s *Service) Attach(ctx context.Context, id string) (AttachResult, error) {
	return s.runtime.Attach(ctx, id)
}

func (s *Service) Send(ctx context.Context, id, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message cannot be empty")
	}
	return s.runtime.Send(ctx, id, message)
}

func (s *Service) Interrupt(ctx context.Context, id string) error {
	return s.runtime.Interrupt(ctx, id)
}

// ClearAttention marks an agent's notification as seen, taking down a
// manual attention mark with it — both mean the same thing to the human.
func (s *Service) ClearAttention(ctx context.Context, id string) error {
	return s.Update(ctx, id, session.Update{ClearAttention: true})
}

// SetMark records the human's own reading of an agent's state, overriding
// what Stormlight inferred. agent.MarkNone removes an existing mark.
func (s *Service) SetMark(ctx context.Context, id string, mark agent.Mark) error {
	return s.Update(ctx, id, session.Update{
		Mark:      mark,
		ClearMark: mark == agent.MarkNone,
	})
}

// Delete removes an agent and forgets it. Deleting is the human saying they
// are done with this one, and that has to be recorded here — a later listing
// cannot tell an agent someone dismissed from one whose window went down with
// its server, and guessing wrong either resurrects the dead or buries the
// living.
func (s *Service) Delete(ctx context.Context, id string) error {
	managedAgent, findErr := s.find(ctx, id)
	if err := s.runtime.Delete(ctx, id); err != nil {
		return err
	}
	if s.store != nil && findErr == nil {
		if err := s.store.Forget(managedAgent.ID); err != nil {
			diagnostic.Logger().Warn("agent not forgotten",
				"agent_id", managedAgent.ID,
				"error", err,
			)
		}
	}
	s.resnapshot(ctx)
	return nil
}

func (s *Service) Update(ctx context.Context, id string, update session.Update) error {
	if err := s.runtime.Update(ctx, id, update); err != nil {
		return err
	}
	s.recordHistory(ctx, id)
	s.resnapshot(ctx)
	return nil
}

// recordHistory mirrors the agent's current state into the session history
// log. It rides on Update because provider events are the only moments the
// record changes — and because the events are also what carry the session
// id, the one field that makes a record worth keeping. Best-effort by
// design: history is a byproduct of the update, never a reason to fail it.
func (s *Service) recordHistory(ctx context.Context, id string) {
	managedAgent, err := s.find(ctx, id)
	if err != nil {
		diagnostic.Logger().Warn("session history listing failed",
			"agent_id", id,
			"error", err,
		)
		return
	}
	if managedAgent.SessionID == "" {
		return
	}
	if err := s.sessions.Append(history.Record{
		SessionID:      managedAgent.SessionID,
		Provider:       managedAgent.Provider,
		AgentID:        managedAgent.ID,
		Name:           managedAgent.Name,
		Task:           managedAgent.Task,
		Summary:        managedAgent.Summary,
		Cwd:            managedAgent.Cwd,
		Mode:           managedAgent.Mode,
		TranscriptPath: managedAgent.TranscriptPath,
		Workspace:      managedAgent.Workspace,
		CreatedAt:      managedAgent.CreatedAt,
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		diagnostic.Logger().Warn("session history append failed",
			"agent_id", id,
			"error", err,
		)
	}
}

// SessionHistory returns past conversations: every session the log knows
// that no current window claims. A dead pane still on the board is not
// history yet — its window is the live record, and deleting it is what
// hands the session over to the log.
func (s *Service) SessionHistory(ctx context.Context) ([]history.Record, error) {
	records, err := s.sessions.Records()
	if err != nil {
		return nil, err
	}
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(agents))
	for _, managedAgent := range agents {
		if managedAgent.SessionID != "" {
			live[managedAgent.SessionID] = true
		}
	}
	past := records[:0]
	for _, record := range records {
		if !live[record.SessionID] {
			past = append(past, record)
		}
	}
	return past, nil
}

// CompactSessionHistory folds the log's accumulated per-event records down
// to one line per session. Meant for startup, off the event path.
func (s *Service) CompactSessionHistory() error {
	return s.sessions.Compact()
}

// find resolves an id — possibly shortened — against the live roster.
func (s *Service) find(ctx context.Context, id string) (agent.Agent, error) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	for _, managedAgent := range agents {
		if managedAgent.ID == id || strings.HasPrefix(managedAgent.ID, id) {
			return managedAgent, nil
		}
	}
	return agent.Agent{}, fmt.Errorf("agent %q not found", id)
}

func (s *Service) Providers() []provider.Info {
	return s.providers.Infos()
}

// AttachTerminal surfaces the runtime's live terminal attachment as the
// widget-facing transport: an exact snapshot seed, then the byte stream,
// with input and resize flowing back.
func (s *Service) AttachTerminal(ctx context.Context, id string, cols, rows int) (pty.Transport, error) {
	streamer, ok := s.runtime.(session.TerminalStreamer)
	if !ok {
		return nil, fmt.Errorf("runtime does not stream terminals")
	}
	return streamer.AttachTerminal(ctx, id, cols, rows)
}
