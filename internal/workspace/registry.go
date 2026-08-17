package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/diagnostic"
)

type Resolver interface {
	Name() string
	Resolve(context.Context, string) (Context, bool, error)
}

type executionRootResolver interface {
	ExecutionRoots(context.Context, Context) ([]Context, bool, error)
}

type Registry struct {
	resolvers []Resolver
	mu        sync.RWMutex
	cache     map[string]Context
	routes    map[string]int
	roots     map[string]cachedExecutionRoots
}

type cachedExecutionRoots struct {
	values []Context
	at     time.Time
}

const (
	executionRootCacheTTL = 5 * time.Second
	executionRootTimeout  = time.Second
	noResolver            = -1
)

func NewRegistry() *Registry {
	resolvers, err := loadExternalResolvers(resolverDirectory())
	if err != nil {
		diagnostic.Logger().Warn("workspace resolver discovery failed", "error", err)
	}
	resolvers = append(resolvers, gitResolver{})
	return NewRegistryWithResolvers(resolvers...)
}

func NewRegistryWithResolvers(resolvers ...Resolver) *Registry {
	return &Registry{
		resolvers: append([]Resolver(nil), resolvers...),
		cache:     make(map[string]Context),
		routes:    make(map[string]int),
		roots:     make(map[string]cachedExecutionRoots),
	}
}

func (r *Registry) Resolve(ctx context.Context, path string) (Context, error) {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return Context{}, err
	}

	r.mu.RLock()
	cached, ok := r.cache[canonical]
	r.mu.RUnlock()
	if ok {
		return cached, nil
	}

	for index, resolver := range r.resolvers {
		value, matched, resolveErr := resolver.Resolve(ctx, canonical)
		if resolveErr != nil {
			if errors.Is(resolveErr, context.Canceled) ||
				errors.Is(resolveErr, context.DeadlineExceeded) ||
				ctx.Err() != nil {
				return Context{}, resolveErr
			}
			diagnostic.Logger().Warn("workspace resolver failed",
				"resolver", resolver.Name(),
				"path", canonical,
				"error", resolveErr,
			)
			continue
		}
		if !matched {
			continue
		}
		value, err = normalizeContext(value)
		if err != nil {
			diagnostic.Logger().Warn("workspace resolver returned invalid context",
				"resolver", resolver.Name(),
				"path", canonical,
				"error", err,
			)
			continue
		}
		r.store(canonical, value, index)
		return value, nil
	}

	value := DirectoryContext(canonical)
	r.store(canonical, value, noResolver)
	return value, nil
}

// ExecutionRoots returns every runnable checkout currently belonging to a
// workspace. Enumeration is best-effort: unsupported, invalid, failed, and
// timed-out discovery all cache and return the resolved path.
func (r *Registry) ExecutionRoots(ctx context.Context, value Context) ([]Context, error) {
	r.mu.RLock()
	cached, ok := r.roots[value.ID]
	resolverIndex, routed := r.routes[value.ID]
	r.mu.RUnlock()
	if ok && time.Since(cached.at) < executionRootCacheTTL {
		return slices.Clone(cached.values), nil
	}

	if !routed {
		resolved, err := r.Resolve(ctx, value.Root)
		if err != nil {
			return r.cacheExecutionRoots(value.ID, []Context{value}), nil
		}
		value = resolved
		r.mu.RLock()
		resolverIndex, routed = r.routes[value.ID]
		r.mu.RUnlock()
	}
	if !routed || resolverIndex == noResolver {
		return r.cacheExecutionRoots(value.ID, []Context{value}), nil
	}
	resolver := r.resolvers[resolverIndex]
	discoverer, ok := resolver.(executionRootResolver)
	if !ok {
		return r.cacheExecutionRoots(value.ID, []Context{value}), nil
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, executionRootTimeout)
	defer cancel()
	values, matched, err := discoverer.ExecutionRoots(discoveryCtx, value)
	if err != nil {
		diagnostic.Logger().Warn("workspace execution-root discovery failed",
			"resolver", resolver.Name(),
			"workspace_id", value.ID,
			"error", err,
		)
		return r.cacheExecutionRoots(value.ID, []Context{value}), nil
	}
	if !matched {
		return r.cacheExecutionRoots(value.ID, []Context{value}), nil
	}

	normalized := make([]Context, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, candidate := range values {
		candidate, err = normalizeContext(candidate)
		if err != nil {
			diagnostic.Logger().Warn("workspace resolver returned invalid execution root",
				"resolver", resolver.Name(),
				"workspace_id", value.ID,
				"error", err,
			)
			continue
		}
		if candidate.ID != value.ID {
			diagnostic.Logger().Warn("workspace resolver returned execution root for another workspace",
				"resolver", resolver.Name(),
				"workspace_id", value.ID,
				"candidate_id", candidate.ID,
			)
			continue
		}
		if seen[candidate.ExecutionRoot] {
			continue
		}
		seen[candidate.ExecutionRoot] = true
		normalized = append(normalized, candidate)
	}
	if len(normalized) == 0 {
		normalized = []Context{value}
	}
	return r.cacheExecutionRoots(value.ID, normalized), nil
}

func (r *Registry) cacheExecutionRoots(id string, values []Context) []Context {
	result := slices.Clone(values)
	r.mu.Lock()
	r.roots[id] = cachedExecutionRoots{
		values: slices.Clone(result),
		at:     time.Now(),
	}
	r.mu.Unlock()
	return result
}

func (r *Registry) store(path string, value Context, resolverIndex int) {
	r.mu.Lock()
	r.cache[path] = value
	r.routes[value.ID] = resolverIndex
	r.mu.Unlock()
}

func resolverDirectory() string {
	if configured := os.Getenv("STORMLIGHT_RESOLVERS_DIR"); configured != "" {
		return configured
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	stormlightDirectory := filepath.Join(configHome, "stormlight", "resolvers")
	if info, err := os.Stat(stormlightDirectory); err == nil && info.IsDir() {
		return stormlightDirectory
	}
	return stormlightDirectory
}
