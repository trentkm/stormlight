package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/trentkm/runstead/internal/diagnostic"
)

type Resolver interface {
	Name() string
	Resolve(context.Context, string) (Context, bool, error)
}

type Registry struct {
	resolvers []Resolver
	mu        sync.RWMutex
	cache     map[string]Context
}

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

	for _, resolver := range r.resolvers {
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
		r.store(canonical, value)
		return value, nil
	}

	value := DirectoryContext(canonical)
	r.store(canonical, value)
	return value, nil
}

func (r *Registry) store(path string, value Context) {
	r.mu.Lock()
	r.cache[path] = value
	r.mu.Unlock()
}

func resolverDirectory() string {
	if configured := os.Getenv("RUNSTEAD_RESOLVERS_DIR"); configured != "" {
		return configured
	}
	if configured := os.Getenv("AGENTMUX_RESOLVERS_DIR"); configured != "" {
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
	runsteadDirectory := filepath.Join(configHome, "runstead", "resolvers")
	if info, err := os.Stat(runsteadDirectory); err == nil && info.IsDir() {
		return runsteadDirectory
	}
	legacyDirectory := filepath.Join(configHome, "agentmux", "resolvers")
	if info, err := os.Stat(legacyDirectory); err == nil && info.IsDir() {
		return legacyDirectory
	}
	return runsteadDirectory
}
