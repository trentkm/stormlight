package app

// Questions for other machines.
//
// Some one-off questions should not make their caller wait on another
// machine. A successful answer stands for the process lifetime; a failed
// answer gets a retry window, and the replacement is fetched behind the
// caller that noticed it was due.

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

// ask answers with what the machine last said and retries failed answers
// after their window. It never waits. A machine that has never answered
// returns errReaching.
func (a *away[T]) ask(
	key, host string,
	question func(context.Context) (T, error),
) (T, error) {
	a.mu.Lock()
	answer, known := a.answers[key]
	a.mu.Unlock()
	if known && (answer.err == nil ||
		time.Since(answer.at) < unreachableResolveTTL) {
		return answer.value, answer.err
	}
	a.begin(key, host, question)
	if known {
		// Failed, but still a more useful answer than pretending the
		// question has never been asked.
		return answer.value, answer.err
	}
	var nothing T
	return nothing, stillReaching(host)
}

// here is the same question asked of this machine, which is close enough
// to wait for. Local failures use a shorter retry window.
func (a *away[T]) here(
	ctx context.Context,
	key string,
	question func(context.Context) (T, error),
) (T, error) {
	a.mu.Lock()
	answer, known := a.answers[key]
	a.mu.Unlock()
	if known && (answer.err == nil ||
		time.Since(answer.at) < localResolveFailureTTL) {
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
