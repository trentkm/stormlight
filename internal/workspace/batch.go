package workspace

import (
	"context"
	"fmt"
)

// ResolveQuery is one filesystem question sent to the machine that owns
// the path. Roots asks for every runnable checkout as part of the same
// answer.
type ResolveQuery struct {
	Path  string `json:"path"`
	Roots bool   `json:"roots,omitempty"`
}

// ResolveReply keeps failures beside their path so one bad catalog entry
// does not discard the other answers in a host-level batch.
type ResolveReply struct {
	Path    string    `json:"path"`
	Context Context   `json:"context,omitempty"`
	Roots   []Context `json:"roots,omitempty"`
	Error   string    `json:"error,omitempty"`
	Host    string    `json:"-"`
}

type batchResolver interface {
	ResolveBatch(context.Context, []ResolveQuery) ([]ResolveReply, error)
}

// ResolveBatchOn answers several questions about one machine together.
// Remote registries turn the unresolved portion into one transport request;
// local registries keep using their in-process resolver chain.
func (r *Registry) ResolveBatchOn(
	ctx context.Context,
	host string,
	queries []ResolveQuery,
) []ResolveReply {
	if host == "" {
		replies := make([]ResolveReply, 0, len(queries))
		for _, query := range queries {
			reply := ResolveReply{Path: query.Path}
			value, err := r.Resolve(ctx, query.Path)
			if err != nil {
				reply.Error = err.Error()
				replies = append(replies, reply)
				continue
			}
			reply.Context = value
			if query.Roots {
				reply.Roots, err = r.ExecutionRoots(ctx, value)
				if err != nil {
					reply.Error = err.Error()
				}
			}
			replies = append(replies, reply)
		}
		return replies
	}
	hosted, err := r.forHost(host)
	if err != nil {
		return failedResolveReplies(queries, err)
	}
	replies := hosted.resolveBatch(ctx, queries)
	for index := range replies {
		replies[index].Host = host
	}
	return replies
}

func (r *Registry) resolveBatch(
	ctx context.Context,
	queries []ResolveQuery,
) []ResolveReply {
	replies := make([]ResolveReply, len(queries))
	pending := make([]ResolveQuery, 0, len(queries))
	pendingIndexes := make([]int, 0, len(queries))
	for index, query := range queries {
		replies[index].Path = query.Path
		canonical, err := r.canonicalize(query.Path)
		if err != nil {
			replies[index].Error = err.Error()
			continue
		}
		query.Path = canonical
		replies[index].Path = canonical
		r.mu.RLock()
		cached, ok := r.cache[canonical]
		r.mu.RUnlock()
		if ok && !query.Roots {
			replies[index].Context = cached
			continue
		}
		pending = append(pending, query)
		pendingIndexes = append(pendingIndexes, index)
	}
	if len(pending) == 0 {
		return replies
	}
	if len(r.resolvers) != 1 {
		return failedResolveRepliesAt(replies, pendingIndexes,
			fmt.Errorf("remote registry has no batch resolver"))
	}
	resolver, ok := r.resolvers[0].(batchResolver)
	if !ok {
		return failedResolveRepliesAt(replies, pendingIndexes,
			fmt.Errorf("%s does not batch workspace resolution", r.resolvers[0].Name()))
	}
	answered, err := resolver.ResolveBatch(ctx, pending)
	if err != nil {
		return failedResolveRepliesAt(replies, pendingIndexes, err)
	}
	if len(answered) != len(pending) {
		return failedResolveRepliesAt(replies, pendingIndexes,
			fmt.Errorf("%s returned %d replies for %d queries",
				r.resolvers[0].Name(), len(answered), len(pending)))
	}
	for answerIndex, answer := range answered {
		index := pendingIndexes[answerIndex]
		replies[index].Path = pending[answerIndex].Path
		if answer.Context.ID == "" {
			replies[index].Error = answer.Error
			if replies[index].Error == "" {
				replies[index].Error = "resolver returned no workspace context"
			}
			continue
		}
		value, normalizeErr := r.normalize(answer.Context)
		if normalizeErr != nil {
			replies[index].Error = normalizeErr.Error()
			continue
		}
		replies[index].Context = value
		r.store(pending[answerIndex].Path, value, 0)
		if answer.Error != "" {
			replies[index].Error = answer.Error
			continue
		}
		if !pending[answerIndex].Roots {
			continue
		}
		roots := make([]Context, 0, len(answer.Roots))
		for _, candidate := range answer.Roots {
			candidate, normalizeErr = r.normalize(candidate)
			if normalizeErr == nil && candidate.ID == value.ID {
				roots = append(roots, candidate)
			}
		}
		if len(roots) == 0 {
			roots = []Context{value}
		}
		replies[index].Roots = r.cacheExecutionRoots(value.ID, roots)
	}
	return replies
}

func failedResolveReplies(queries []ResolveQuery, err error) []ResolveReply {
	replies := make([]ResolveReply, len(queries))
	for index, query := range queries {
		replies[index] = ResolveReply{Path: query.Path, Error: err.Error()}
	}
	return replies
}

func failedResolveRepliesAt(
	replies []ResolveReply,
	indexes []int,
	err error,
) []ResolveReply {
	for _, index := range indexes {
		replies[index].Error = err.Error()
	}
	return replies
}
