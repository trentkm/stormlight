package app

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

type DispatchRequest struct {
	Provider agent.Provider
	Name     string
	Task     string
	Cwd      string
	Mode     agent.PermissionMode
	// Host is the machine to run on; empty is this one. Cwd is a path on
	// that machine, so the two travel together.
	Host string
}

type AttachResult = session.AttachResult

type OverlayRequest = session.OverlayRequest
type Overlay = session.Overlay

type Service struct {
	runtime    session.Runtime
	providers  *provider.Registry
	workspaces *workspace.Registry
	catalog    *workspace.Catalog
	// sessions is the permanent conversation log: every session id the
	// providers ever reported, kept so a conversation can be reopened long
	// after its window is gone.
	sessions *history.Log
	// and one-shot commands want.

	// resolved caches catalog-path resolution (one git spawn per path per
	// call otherwise); the dashboard polls fast, directories change slowly.
	// Keyed by catalog path, so added or removed workspaces never consult
	// a stale entry.
	resolveMu sync.Mutex
	resolved  map[workspace.Entry]resolvedWorkspace

	// transcripts caches renders of transcripts that had to cross a
	// tunnel to get here.
	transcriptMu sync.Mutex
	transcripts  map[string]cachedTranscript

	// localTranscripts caches parses of transcripts on this machine,
	// keyed by path and invalidated by mtime and size. A 14MB JSONL is
	// cheap to stat and expensive to re-parse, and the web polls.
	localTranscriptMu sync.Mutex
	localTranscripts  map[string]localTranscript
}

type localTranscript struct {
	modTime time.Time
	size    int64
	entries []provider.TranscriptEntry
	ok      bool
}

type cachedTranscript struct {
	entries  []provider.TranscriptEntry
	rendered string
	ok       bool
	at       time.Time
}

// remoteTranscriptTTL bounds how stale the settled part of a remote
// conversation can be. Short enough that a finished turn appears without
// anyone waiting for it; long enough that a refresh loop is not a file
// transfer.
const remoteTranscriptTTL = 2 * time.Second

type resolvedWorkspace struct {
	value workspace.Context
	// failure is why it did not resolve, remembered as deliberately as a
	// success: a host that is asleep costs a connection attempt to
	// discover, and re-discovering it on every refresh is a dashboard
	// that spends its whole life waiting on a machine nobody is using.
	failure error
	at      time.Time
}

// workspaceResolveTTL bounds how stale a cached resolution can get; a
// checkout converted to a worktree (or similar) is noticed within this.
const workspaceResolveTTL = 10 * time.Second

// unreachableResolveTTL is how long a host that could not answer is left
// alone. Long enough that a sleeping laptop is not dialled on every
// refresh, short enough that waking it up shows within the minute.
const unreachableResolveTTL = 30 * time.Second

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

func (s *Service) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for index := range agents {
		if agents[index].Workspace.IsComplete() {
			continue
		}
		// An agent's cwd is a path on its own machine, and the fleet has
		// already said which that is.
		value, resolveErr := s.workspaces.ResolveOn(
			ctx, agents[index].Host, agents[index].Cwd)
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
	return agents, nil
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
	// Resolution happens on the machine the path is on, and the context
	// it returns is what tells the runtime which daemon this dispatch
	// belongs to.
	workspaceContext, err := s.workspaces.ResolveOn(ctx, req.Host, req.Cwd)
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
	if err := s.catalog.Add(workspace.EntryOf(workspaceContext)); err != nil {
		diagnostic.Logger().Warn("workspace catalog update failed",
			"host", workspaceContext.Host,
			"path", workspaceContext.Root,
			"error", err,
		)
	}
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
	// A conversation reopens on the machine it happened on: its
	// transcript, its repository, and its provider's own session state
	// are all over there.
	workspaceContext, err := s.workspaces.ResolveOn(
		ctx, record.Workspace.Host, record.Cwd)
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
	entries, err := s.catalog.Entries()
	if err != nil {
		return nil, err
	}
	values := make([]workspace.Context, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		value, resolveErr := s.resolveCached(ctx, entry)
		if resolveErr != nil {
			// A host that is asleep cannot describe its workspaces, and
			// that is not a reason to fail the listing — the rest of the
			// catalog is still answerable.
			diagnostic.Logger().Warn("catalog workspace resolution failed",
				"host", entry.Host,
				"path", entry.Path,
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

// ListWorkspaceRoots expands every catalog workspace into its currently
// available execution roots. It is the shared source for headless callers and
// the dashboard's dispatch picker.
func (s *Service) ListWorkspaceRoots(ctx context.Context) ([]workspace.Context, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	var roots []workspace.Context
	for _, value := range workspaces {
		values, rootErr := s.workspaces.ExecutionRoots(ctx, value)
		if rootErr != nil {
			return nil, rootErr
		}
		roots = append(roots, values...)
	}
	s.applyWorkspaceNames(roots)
	sortWorkspaceRoots(roots)
	return roots, nil
}

// WorkspaceRoots resolves one path without adding it to the catalog, then
// returns all execution roots belonging to the same workspace.
func (s *Service) WorkspaceRoots(
	ctx context.Context,
	path string,
) ([]workspace.Context, error) {
	value, err := s.workspaces.Resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	values, err := s.workspaces.ExecutionRoots(ctx, value)
	if err != nil {
		return nil, err
	}
	s.applyWorkspaceNames(values)
	sortWorkspaceRoots(values)
	return values, nil
}

func sortWorkspaceRoots(values []workspace.Context) {
	slices.SortStableFunc(values, func(a, b workspace.Context) int {
		if order := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
			return order
		}
		if order := cmp.Compare(strings.ToLower(a.Root), strings.ToLower(b.Root)); order != 0 {
			return order
		}
		if order := cmp.Compare(a.ID, b.ID); order != 0 {
			return order
		}
		aPrimary := a.ExecutionRoot == a.Root
		bPrimary := b.ExecutionRoot == b.Root
		if aPrimary != bPrimary {
			if aPrimary {
				return -1
			}
			return 1
		}
		return cmp.Compare(
			strings.ToLower(a.ExecutionRoot),
			strings.ToLower(b.ExecutionRoot),
		)
	})
}

func (s *Service) resolveCached(
	ctx context.Context,
	entry workspace.Entry,
) (workspace.Context, error) {
	s.resolveMu.Lock()
	cached, ok := s.resolved[entry]
	s.resolveMu.Unlock()
	if ok && time.Since(cached.at) < s.resolveTTL(entry) {
		return cached.value, cached.failure
	}
	value, err := s.workspaces.ResolveOn(ctx, entry.Host, entry.Path)
	s.resolveMu.Lock()
	if s.resolved == nil {
		s.resolved = map[workspace.Entry]resolvedWorkspace{}
	}
	s.resolved[entry] = resolvedWorkspace{value: value, failure: err, at: time.Now()}
	s.resolveMu.Unlock()
	if err != nil {
		return workspace.Context{}, err
	}
	return value, nil
}

// resolveTTL is how long an answer stands. A directory on this machine
// changes shape rarely and costs nothing to re-read; a machine that could
// not be reached costs a connection attempt to ask again, so it is left
// alone for longer — the same reasoning the fleet applies to its members.
func (s *Service) resolveTTL(entry workspace.Entry) time.Duration {
	if entry.Host == "" {
		return workspaceResolveTTL
	}
	s.resolveMu.Lock()
	cached, ok := s.resolved[entry]
	s.resolveMu.Unlock()
	if ok && cached.failure != nil {
		return unreachableResolveTTL
	}
	return workspaceResolveTTL
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
		if name, ok := names[workspace.EntryOf(values[index])]; ok {
			values[index].Name = name
		}
	}
}

// remoteHistory is every other machine's conversation log.
//
// A conversation is recorded where it happened — the provider's hooks
// report to the Stormlight on that host — so without this the history
// browser can only offer to reopen the ones that happened here, and a
// remote agent's conversation dies with the agent.
//
// Each machine's records are stamped with it on the way in. The stamp is
// what sends a resumed conversation back to the machine it belongs to,
// and what keeps two machines' sessions from being read as one list of
// paths that half exist.
func (s *Service) remoteHistory(ctx context.Context) []history.Record {
	reader, ok := s.runtime.(session.HistoryReader)
	if !ok {
		return nil
	}
	logs, err := reader.ReadHistory(ctx)
	if err != nil {
		diagnostic.Logger().Warn("remote history unavailable", "error", err)
		return nil
	}
	var records []history.Record
	for host, log := range logs {
		for _, record := range history.Decode(log) {
			record.Workspace = record.Workspace.OnHost(host)
			records = append(records, record)
		}
	}
	return records
}

// AddWorkspace remembers a directory as a workspace. The host names the
// machine the path is on; empty is this one.
func (s *Service) AddWorkspace(
	ctx context.Context,
	host, path string,
) (workspace.Context, error) {
	value, err := s.workspaces.ResolveOn(ctx, host, path)
	if err != nil {
		return workspace.Context{}, err
	}
	if err := s.catalog.Add(workspace.EntryOf(value)); err != nil {
		return workspace.Context{}, err
	}
	return value, nil
}

func (s *Service) RemoveWorkspace(_ context.Context, value workspace.Context) error {
	return s.catalog.Remove(workspace.EntryOf(value))
}

func (s *Service) RenameWorkspace(
	_ context.Context,
	value workspace.Context,
	name string,
) error {
	return s.catalog.SetName(workspace.EntryOf(value), name)
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
// screen is all a capture can see of an alternate-screen agent, so the
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
		rendered, ok := s.renderTranscript(ctx, managedAgent)
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

// renderTranscript reads and renders an agent's transcript from the
// machine it is on.
//
// A remote transcript is cached for a beat. This runs on the dashboard's
// refresh path, and a conversation that has run all afternoon is not a
// file to pull across a tunnel several times a second. What the cache
// costs is nothing the eye can see: the live screen is appended below the
// divider from the terminal snapshot, which is cheap and never cached, so
// output in flight still arrives every frame — only the settled part of
// the conversation waits.
func (s *Service) renderTranscript(
	ctx context.Context,
	managedAgent agent.Agent,
) (string, bool) {
	if s.transcriptIsLocal(managedAgent) {
		// Reading a local file is cheap enough that a cache would only
		// add staleness.
		return provider.RenderClaudeTranscript(managedAgent.TranscriptPath)
	}
	cached := s.remoteTranscript(ctx, managedAgent)
	return cached.rendered, cached.ok
}

// Transcript returns an agent's conversation as parsed entries — the
// unstyled form the web client paints its own way. ok is false when the
// agent has no transcript, so callers fall back to the terminal.
func (s *Service) Transcript(
	ctx context.Context,
	id string,
) ([]provider.TranscriptEntry, bool) {
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return nil, false
	}
	for _, managedAgent := range agents {
		if managedAgent.ID != id {
			continue
		}
		if managedAgent.TranscriptPath == "" {
			return nil, false
		}
		if s.transcriptIsLocal(managedAgent) {
			return s.localTranscript(managedAgent.TranscriptPath)
		}
		cached := s.remoteTranscript(ctx, managedAgent)
		return cached.entries, cached.ok
	}
	return nil, false
}

// localTranscript parses a transcript on this machine, re-parsing only
// when the file's mtime or size moved. The cached slice is shared with
// callers and never mutated after the parse.
func (s *Service) localTranscript(path string) ([]provider.TranscriptEntry, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	s.localTranscriptMu.Lock()
	cached, hit := s.localTranscripts[path]
	s.localTranscriptMu.Unlock()
	if hit && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.entries, cached.ok
	}
	entries, ok := provider.ParseClaudeTranscript(path)
	s.localTranscriptMu.Lock()
	if s.localTranscripts == nil {
		s.localTranscripts = map[string]localTranscript{}
	}
	s.localTranscripts[path] = localTranscript{
		modTime: info.ModTime(),
		size:    info.Size(),
		entries: entries,
		ok:      ok,
	}
	s.localTranscriptMu.Unlock()
	return entries, ok
}

// transcriptIsLocal reports whether the agent's transcript is a file on
// this machine, readable directly.
func (s *Service) transcriptIsLocal(managedAgent agent.Agent) bool {
	if _, ok := s.runtime.(session.FileReader); !ok {
		return true
	}
	return managedAgent.Host == ""
}

// remoteTranscript fetches, parses, and renders a transcript that lives
// across a tunnel, under the TTL cache. The render is memoized with the
// fetch: entries only change when the file is re-read, so painting them
// once per fetch serves every refresh frame in between.
func (s *Service) remoteTranscript(
	ctx context.Context,
	managedAgent agent.Agent,
) cachedTranscript {
	reader, isReader := s.runtime.(session.FileReader)
	if !isReader {
		return cachedTranscript{}
	}

	key := managedAgent.Host + "\x00" + managedAgent.TranscriptPath
	s.transcriptMu.Lock()
	cached, hit := s.transcripts[key]
	s.transcriptMu.Unlock()
	if hit && time.Since(cached.at) < remoteTranscriptTTL {
		return cached
	}

	entries, ok := func() ([]provider.TranscriptEntry, bool) {
		source, err := reader.ReadAgentFile(ctx, managedAgent.ID, managedAgent.TranscriptPath)
		if err != nil {
			diagnostic.Logger().Warn("transcript unavailable",
				"agent_id", managedAgent.ID,
				"host", managedAgent.Host,
				"path", managedAgent.TranscriptPath,
				"error", err,
			)
			return nil, false
		}
		defer source.Close()
		return provider.ParseClaudeTranscriptFrom(source)
	}()

	fresh := cachedTranscript{entries: entries, ok: ok, at: time.Now()}
	if ok {
		fresh.rendered = provider.RenderTranscriptEntries(entries)
	}
	s.transcriptMu.Lock()
	if s.transcripts == nil {
		s.transcripts = map[string]cachedTranscript{}
	}
	s.transcripts[key] = fresh
	s.transcriptMu.Unlock()
	return fresh
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

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.runtime.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, id string, update session.Update) error {
	if err := s.runtime.Update(ctx, id, update); err != nil {
		return err
	}
	s.recordHistory(ctx, id)
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
	records = append(records, s.remoteHistory(ctx)...)
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
	// Each machine's log is newest-first on its own; together they are
	// several sorted lists laid end to end, and the browser reads as one.
	slices.SortStableFunc(past, func(a, b history.Record) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return past, nil
}

// CompactSessionHistory folds the log's accumulated per-event records down
// to one line per session. Meant for startup, off the event path.
func (s *Service) CompactSessionHistory() error {
	return s.sessions.Compact()
}

// find resolves an id — possibly shortened — against the live roster.
func (s *Service) find(ctx context.Context, id string) (agent.Agent, error) {
	if id == "" {
		return agent.Agent{}, fmt.Errorf("agent id is required")
	}
	agents, err := s.runtime.ListAgents(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	var match *agent.Agent
	for index, managedAgent := range agents {
		if managedAgent.ID == id {
			return managedAgent, nil
		}
		if strings.HasPrefix(managedAgent.ID, id) {
			if match != nil {
				return agent.Agent{}, fmt.Errorf("agent id %q is ambiguous", id)
			}
			match = &agents[index]
		}
	}
	if match == nil {
		return agent.Agent{}, fmt.Errorf("agent %q not found", id)
	}
	return *match, nil
}

func (s *Service) Providers() []provider.Info {
	return s.providers.Infos()
}

// StartOverlay floats a short-lived interactive program (the Yazi picker,
// the Neovim task editor) on a runtime-owned PTY, for the dashboard to
// render in a popup.
func (s *Service) StartOverlay(ctx context.Context, request session.OverlayRequest) (session.Overlay, error) {
	host, ok := s.runtime.(session.OverlayHost)
	if !ok {
		return nil, fmt.Errorf("runtime cannot host overlays")
	}
	return host.StartOverlay(ctx, request)
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
