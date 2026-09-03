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

	// resolved and roots support asynchronous one-off questions. Successful
	// answers live for the process; failed remote questions retain a retry
	// window.
	resolved away[workspace.Context]
	roots    away[[]workspace.Context]

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

// localResolveFailureTTL keeps a temporarily unreadable local path from
// being retried in a tight caller loop. Successful answers do not expire.
const localResolveFailureTTL = 10 * time.Second

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
	req.Name = strings.TrimSpace(req.Name)
	if req.Mode == "" {
		req.Mode = agent.DefaultMode
	}
	launch, err := s.providers.ResolveNamed(
		req.Provider, req.Task, req.Name, req.Mode)
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
	launch, err := s.providers.ResumeNamed(
		record.Provider, record.SessionID, record.Name, mode)
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
	replies, err := s.resolveCatalog(ctx, false)
	if err != nil {
		return nil, err
	}
	values := make([]workspace.Context, 0, len(replies))
	seen := make(map[string]bool, len(replies))
	for _, reply := range replies {
		if reply.Error != "" {
			diagnostic.Logger().Warn("catalog workspace resolution failed",
				"request_type", "workspace_catalog",
				"host", reply.Host,
				"path", reply.Path,
				"retry_reason", "resolution_error",
				"error", reply.Error,
			)
			continue
		}
		value := reply.Context
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
	replies, err := s.resolveCatalog(ctx, true)
	if err != nil {
		return nil, err
	}
	var roots []workspace.Context
	for _, reply := range replies {
		if reply.Error != "" {
			diagnostic.Logger().Warn("workspace execution roots unavailable",
				"request_type", "workspace_roots",
				"host", reply.Host,
				"path", reply.Path,
				"retry_reason", "resolution_error",
				"error", reply.Error,
			)
			if reply.Context.ID != "" {
				roots = append(roots, reply.Context)
			}
			continue
		}
		if len(reply.Roots) == 0 {
			roots = append(roots, reply.Context)
			continue
		}
		roots = append(roots, reply.Roots...)
	}
	s.applyWorkspaceNames(roots)
	sortWorkspaceRoots(roots)
	return roots, nil
}

func (s *Service) resolveCatalog(
	ctx context.Context,
	includeRoots bool,
) ([]workspace.ResolveReply, error) {
	entries, err := s.catalog.Entries()
	if err != nil {
		return nil, err
	}
	type hostBatch struct {
		host    string
		indexes []int
		queries []workspace.ResolveQuery
	}
	byHost := make(map[string]*hostBatch)
	order := make([]string, 0)
	for index, entry := range entries {
		batch := byHost[entry.Host]
		if batch == nil {
			batch = &hostBatch{host: entry.Host}
			byHost[entry.Host] = batch
			order = append(order, entry.Host)
		}
		batch.indexes = append(batch.indexes, index)
		batch.queries = append(batch.queries, workspace.ResolveQuery{
			Path:  entry.Path,
			Roots: includeRoots,
		})
	}
	replies := make([]workspace.ResolveReply, len(entries))
	var requests sync.WaitGroup
	for _, host := range order {
		batch := byHost[host]
		requests.Add(1)
		go func() {
			defer requests.Done()
			answered := s.workspaces.ResolveBatchOn(ctx, batch.host, batch.queries)
			for index, reply := range answered {
				if index < len(batch.indexes) {
					replies[batch.indexes[index]] = reply
				}
			}
		}()
	}
	requests.Wait()
	return replies, nil
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

// resolveCached turns a catalog entry into a workspace. A directory on
// this machine is read now; one on another machine is answered from what
// that machine last said, and asked again behind the refresh rather than
// in front of it. See away.go for why.
func (s *Service) resolveCached(
	ctx context.Context,
	entry workspace.Entry,
) (workspace.Context, error) {
	question := func(ctx context.Context) (workspace.Context, error) {
		return s.workspaces.ResolveOn(ctx, entry.Host, entry.Path)
	}
	key := entry.Host + "\x00" + entry.Path
	if entry.Host == "" {
		return s.resolved.here(ctx, key, question)
	}
	return s.resolved.ask(key, entry.Host, question)
}

// executionRoots expands a workspace into the checkouts an agent can be
// dispatched into. The worktree list of a repository on another machine
// is that machine's answer to give, and asking for it is a second trip
// down the same tunnel — so it obeys the same rule the resolution does.
func (s *Service) executionRoots(
	ctx context.Context,
	value workspace.Context,
) ([]workspace.Context, error) {
	if value.Host == "" {
		// The registry already caches this one, and re-reading a local
		// worktree list is a process spawn rather than a handshake.
		return s.workspaces.ExecutionRoots(ctx, value)
	}
	return s.roots.ask(value.ID, value.Host, func(ctx context.Context) ([]workspace.Context, error) {
		return s.workspaces.ExecutionRoots(ctx, value)
	})
}

// Reaching names the machines with a question outstanding: hosts the
// fleet is connecting to, and hosts being asked about their directories.
// Nothing from them can be drawn yet, and "reaching devbox…" is what a
// dashboard should say in the place it would otherwise leave empty.
func (s *Service) Reaching() []string {
	hosts := append(s.resolved.hosts(), s.roots.hosts()...)
	if runtime, ok := s.runtime.(interface{ Reaching() []string }); ok {
		hosts = append(hosts, runtime.Reaching()...)
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
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
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if err := s.runtime.Rename(ctx, id, name); err != nil {
		return err
	}
	// A remote agent is synchronized by its next provider hook, which runs
	// beside that provider and therefore has the right binary and state.
	managedAgent, err := s.find(ctx, id)
	if err != nil {
		diagnostic.Logger().Warn("renamed agent could not be listed for session sync",
			"agent_id", id,
			"error", err,
		)
		return nil
	}
	if managedAgent.Host != "" {
		return nil
	}
	if err := s.SyncSessionName(ctx, id); err != nil {
		// Stormlight's name is authoritative for its own UI. A provider
		// index failure is retried by the next lifecycle event rather than
		// turning a completed rename into a misleading failed action.
		diagnostic.Logger().Warn("provider session name sync failed",
			"agent_id", id,
			"error", err,
		)
	}
	return nil
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

// SyncSessionName copies an explicit Stormlight name into the provider's own
// session index. It is called from provider hooks so the operation runs on
// the same machine as remote providers and their state.
func (s *Service) SyncSessionName(ctx context.Context, id string) error {
	managedAgent, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(managedAgent.Name)
	if name == "" || managedAgent.SessionID == "" ||
		managedAgent.SessionName == name {
		return nil
	}
	command, supported, err := s.providers.SessionNameCommand(
		managedAgent.Provider,
		name,
	)
	if err != nil {
		return err
	}
	if managedAgent.ProcessLive && supported {
		if sender, ok := s.runtime.(session.CommandSender); ok {
			if err := sender.SendCommand(ctx, id, command); err != nil {
				return err
			}
			return s.runtime.Update(
				ctx,
				id,
				session.Update{SessionName: name},
			)
		}
	}
	supported, err = s.providers.SetSessionName(
		ctx,
		managedAgent.Provider,
		managedAgent.SessionID,
		name,
	)
	if err != nil {
		return err
	}
	if !supported {
		return nil
	}
	return s.runtime.Update(ctx, id, session.Update{SessionName: name})
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
