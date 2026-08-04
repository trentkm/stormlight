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
	"github.com/trentkm/stormlight/internal/provider"
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
	)
}

func NewServiceWithCatalog(
	runtime session.Runtime,
	providers *provider.Registry,
	workspaces *workspace.Registry,
	catalog *workspace.Catalog,
) *Service {
	if workspaces == nil {
		workspaces = workspace.NewRegistry()
	}
	if catalog == nil {
		catalog = workspace.NewCatalog()
	}
	return &Service{
		runtime:    runtime,
		providers:  providers,
		workspaces: workspaces,
		catalog:    catalog,
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
	return managedAgent, nil
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
	return s.runtime.Update(ctx, id, session.Update{ClearAttention: true})
}

// SetMark records the human's own reading of an agent's state, overriding
// what Stormlight inferred. agent.MarkNone removes an existing mark.
func (s *Service) SetMark(ctx context.Context, id string, mark agent.Mark) error {
	return s.runtime.Update(ctx, id, session.Update{
		Mark:      mark,
		ClearMark: mark == agent.MarkNone,
	})
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.runtime.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, id string, update session.Update) error {
	return s.runtime.Update(ctx, id, update)
}

func (s *Service) Providers() []provider.Info {
	return s.providers.Infos()
}

func (s *Service) SyncAgentWindows(ctx context.Context, width, height int) error {
	return s.runtime.SyncWindowSizes(ctx, width, height)
}

func (s *Service) Runtime() session.Runtime {
	return s.runtime
}
