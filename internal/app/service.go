package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/pending"
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
	actions    *pending.Store
}

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
	return NewServiceWithStores(
		runtime,
		providers,
		workspaces,
		catalog,
		pending.NewStore(),
	)
}

func NewServiceWithStores(
	runtime session.Runtime,
	providers *provider.Registry,
	workspaces *workspace.Registry,
	catalog *workspace.Catalog,
	actions *pending.Store,
) *Service {
	if workspaces == nil {
		workspaces = workspace.NewRegistry()
	}
	if catalog == nil {
		catalog = workspace.NewCatalog()
	}
	if actions == nil {
		actions = pending.NewStore()
	}
	return &Service{
		runtime:    runtime,
		providers:  providers,
		workspaces: workspaces,
		catalog:    catalog,
		actions:    actions,
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
		value, resolveErr := s.workspaces.Resolve(ctx, path)
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
	return s.runtime.Capture(ctx, id, lines)
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

// ClearAttention marks an agent's notification as seen.
func (s *Service) ClearAttention(ctx context.Context, id string) error {
	return s.runtime.Update(ctx, id, session.Update{ClearAttention: true})
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.runtime.Delete(ctx, id)
}

func (s *Service) ListPendingActions(
	ctx context.Context,
) ([]pending.Action, error) {
	return s.actions.List(ctx)
}

func (s *Service) ResolvePendingAction(
	ctx context.Context,
	actionID string,
	optionID string,
) error {
	return s.actions.Resolve(ctx, actionID, optionID)
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
