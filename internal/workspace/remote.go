package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/remote"
)

const remoteWorkspaceConcurrency = 4

var remoteWorkspaceSlots = make(chan struct{}, remoteWorkspaceConcurrency)

// NewRegistryForHost resolves workspaces on another machine.
//
// Resolution is filesystem work — `git rev-parse`, a directory that has to
// exist, an executable resolver in that host's config — so it runs where
// the filesystem is, through `stormlight _resolve`. Doing it here would be
// wrong in the quietest possible way: /home/me/src/api may well exist on
// both machines, and a local answer about a remote path looks entirely
// correct until two different repositories share one workspace group.
func NewRegistryForHost(host remote.Host) *Registry {
	registry := NewRegistryWithResolvers(&remoteResolver{
		transport: remote.NewTransport(host),
		host:      host.Name,
	})
	registry.host = host.Name
	return registry
}

// AddHost teaches a local registry to answer about another machine as
// well, so one registry can serve a dashboard that works several. The
// host's own registry is kept whole rather than folded in: its resolvers,
// its caches, and its rules about paths are all different from this
// machine's.
func (r *Registry) AddHost(host remote.Host) {
	if host.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hosts == nil {
		r.hosts = make(map[string]*Registry)
	}
	if _, known := r.hosts[host.Name]; known {
		return
	}
	r.hosts[host.Name] = NewRegistryForHost(host)
}

// ResolveOn resolves a path on a named machine. An empty host is this
// one, which is the only case that can take a relative path — "here"
// means nothing said from somewhere else.
func (r *Registry) ResolveOn(ctx context.Context, host, path string) (Context, error) {
	if host == "" {
		return r.Resolve(ctx, path)
	}
	hosted, err := r.forHost(host)
	if err != nil {
		return Context{}, err
	}
	return hosted.Resolve(ctx, path)
}

// forHost is the registry that answers about one machine, made on first
// use. A host does not have to be configured to exist: naming it is
// enough, and AddHost only supplies the details that differ from what ssh
// would do with the name on its own.
func (r *Registry) forHost(host string) (*Registry, error) {
	r.mu.RLock()
	hosted, ok := r.hosts[host]
	r.mu.RUnlock()
	if ok {
		return hosted, nil
	}
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("no host named")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if hosted, ok := r.hosts[host]; ok {
		return hosted, nil
	}
	if r.hosts == nil {
		r.hosts = make(map[string]*Registry)
	}
	hosted = NewRegistryForHost(remote.Host{Name: host})
	r.hosts[host] = hosted
	return hosted, nil
}

type remoteResolver struct {
	transport *remote.Transport
	host      string
	mu        sync.Mutex
}

func (r *remoteResolver) Name() string { return "remote:" + r.host }

func (r *remoteResolver) Resolve(ctx context.Context, path string) (Context, bool, error) {
	output, err := r.run(ctx, "_resolve", path)
	if err != nil {
		return Context{}, false, err
	}
	var value Context
	if err := json.Unmarshal(output, &value); err != nil {
		return Context{}, false, fmt.Errorf("%s: %w", r.Name(), err)
	}
	return value, true, nil
}

func (r *remoteResolver) ExecutionRoots(
	ctx context.Context,
	value Context,
) ([]Context, bool, error) {
	output, err := r.run(ctx, "_resolve", "--roots", value.Root)
	if err != nil {
		return nil, true, err
	}
	var values []Context
	if err := json.Unmarshal(output, &values); err != nil {
		return nil, true, fmt.Errorf("%s: %w", r.Name(), err)
	}
	return values, true, nil
}

func (r *remoteResolver) ResolveBatch(
	ctx context.Context,
	queries []ResolveQuery,
) ([]ResolveReply, error) {
	input, err := json.Marshal(queries)
	if err != nil {
		return nil, err
	}
	output, err := r.runInput(
		ctx, bytes.NewReader(input), len(queries), "_resolve", "--batch")
	if err != nil {
		return nil, err
	}
	var replies []ResolveReply
	if err := json.Unmarshal(output, &replies); err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name(), err)
	}
	return replies, nil
}

func (r *remoteResolver) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.runInput(ctx, nil, 1, args...)
}

func (r *remoteResolver) runInput(
	ctx context.Context,
	stdin io.Reader,
	count int,
	args ...string,
) ([]byte, error) {
	select {
	case remoteWorkspaceSlots <- struct{}{}:
		defer func() { <-remoteWorkspaceSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	started := time.Now()
	command := r.transport.CommandContext(ctx, nil, args...)
	var stdout, stderr bytes.Buffer
	command.Stdin = stdin
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		diagnostic.Logger().Warn("remote workspace request failed",
			"request_type", "workspace_resolve",
			"host", r.host,
			"request_count", count,
			"duration", time.Since(started),
			"retry_reason", "transport_error",
			"error", err,
		)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The far side's own complaint says which of the many first-run
		// problems this is: no stormlight there, no such directory, a
		// key that is not accepted.
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, errors.New(remote.Explain(r.host, message))
		}
		return nil, fmt.Errorf("%s: %w", r.Name(), err)
	}
	diagnostic.Logger().Debug("remote workspace request completed",
		"request_type", "workspace_resolve",
		"host", r.host,
		"request_count", count,
		"duration", time.Since(started),
	)
	return stdout.Bytes(), nil
}
