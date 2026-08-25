package app

// Questions for other machines.
//
// A dashboard refresh runs several times a second and asks two kinds of
// question: what is here, and what is over there. The first is a syscall
// away. The second is an SSH handshake bounded at ten seconds — and a
// refresh that waits for it is a dashboard drawing nothing for ten
// seconds, on the chance the answer changed since the last one.
//
// So it is not asked on the refresh path at all. What a machine last said
// stands until something replaces it, the replacement is fetched behind
// whoever wanted it, and a machine nobody has finished asking says so
// rather than being reported as empty. That last part is the whole point:
// "reaching devbox…" and "nothing on devbox" are different sentences, and
// only one of them is true while a connection is being made.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// errReaching marks an answer that does not exist yet because the machine
// is still being asked. It is neither a failure nor an empty result, and a
// caller that cannot tell those three apart will draw one as another.
var errReaching = errors.New("reaching")

// stillReaching names the machine in the error, so whatever is drawing
// can name it too.
func stillReaching(host string) error {
	return fmt.Errorf("%w %s", errReaching, host)
}

// answerTTL is how long another machine's answer stands.
//
// A directory changes shape rarely, so a good answer is re-asked on a
// leisurely cadence. A machine that could not be reached costs a
// connection attempt to ask again, so it is left alone for longer — the
// same reasoning the fleet applies to its members. Both bounds are about
// how often the question is worth asking, not about how long anyone
// waits: nobody waits.
func answerTTL(failure error) time.Duration {
	if failure != nil {
		return unreachableResolveTTL
	}
	return workspaceResolveTTL
}

// remoteAskTimeout bounds a background ask. The refresh that wanted it is
// long gone by the time this matters, so the bound is here to stop a hung
// connection from pinning the question open forever — not to keep anyone
// waiting.
const remoteAskTimeout = 30 * time.Second

// away caches one kind of answer from other machines and re-asks them
// behind whoever wanted to know.
type away[T any] struct {
	mu      sync.Mutex
	answers map[string]awayAnswer[T]
	// asking maps a question in flight to the machine being asked, which
	// is what lets a dashboard say which machine it is waiting on.
	asking map[string]string
}

type awayAnswer[T any] struct {
	value T
	err   error
	at    time.Time
}

// ask answers with what the machine last said and starts a fresh ask when
// that has gone stale. It never waits. A machine that has never answered
// returns errReaching, which is the caller's cue to say so.
func (a *away[T]) ask(
	key, host string,
	question func(context.Context) (T, error),
) (T, error) {
	a.mu.Lock()
	answer, known := a.answers[key]
	a.mu.Unlock()
	if known && time.Since(answer.at) < answerTTL(answer.err) {
		return answer.value, answer.err
	}
	a.begin(key, host, question)
	if known {
		// Stale, and still better than a workspace that drops out of the
		// list every time its answer expires and reappears when the next
		// one lands.
		return answer.value, answer.err
	}
	var nothing T
	return nothing, stillReaching(host)
}

// here is the same question asked of this machine, which is close enough
// to wait for. The answer is kept beside the ones from elsewhere, under
// the same cache, but never under the unreachable window: a directory
// here that could not be read costs a syscall to try again, and one that
// reappears belongs back in the list on the next refresh rather than half
// a minute later.
func (a *away[T]) here(
	ctx context.Context,
	key string,
	question func(context.Context) (T, error),
) (T, error) {
	a.mu.Lock()
	answer, known := a.answers[key]
	a.mu.Unlock()
	if known && time.Since(answer.at) < workspaceResolveTTL {
		return answer.value, answer.err
	}
	value, err := question(ctx)
	a.remember(key, value, err)
	return value, err
}

func (a *away[T]) begin(
	key, host string,
	question func(context.Context) (T, error),
) {
	a.mu.Lock()
	if a.asking == nil {
		a.asking = map[string]string{}
	}
	if _, already := a.asking[key]; already {
		a.mu.Unlock()
		return
	}
	a.asking[key] = host
	a.mu.Unlock()
	go func() {
		// Its own deadline. The refresh that started this carries a short
		// one and has already returned.
		ctx, cancel := context.WithTimeout(context.Background(), remoteAskTimeout)
		defer cancel()
		value, err := question(ctx)
		a.remember(key, value, err)
		a.mu.Lock()
		delete(a.asking, key)
		a.mu.Unlock()
	}()
}

func (a *away[T]) remember(key string, value T, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.answers == nil {
		a.answers = map[string]awayAnswer[T]{}
	}
	a.answers[key] = awayAnswer[T]{value: value, err: err, at: time.Now()}
}

// hosts are the machines with a question outstanding right now.
func (a *away[T]) hosts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var hosts []string
	for _, host := range a.asking {
		if host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
