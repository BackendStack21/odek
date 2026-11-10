package extended

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"time"
)

// The rerank is a memory-LLM call on the turn-start path: the model does not
// start until recall returns. Two guards keep a slow or failing memory LLM
// from stalling every turn.
//
// First, one rerank call gets at most half of the recall budget left when it
// starts, and never more than rerankMaxBudget, so when it does not answer
// recall still has time to return the blended ranking.
//
// Second, a circuit breaker: after rerankBreakerThreshold consecutive
// failures or timeouts the rerank is skipped for a cooldown that starts at
// rerankBreakerBaseCooldown and doubles on each failed probe up to
// rerankBreakerMaxCooldown. Once a cooldown has passed, one call probes the
// memory LLM; a success closes the breaker and resets the backoff. While it
// is open, recall uses the blended order and makes no rerank call.
//
// Only outcomes that say something about the memory LLM count toward the
// breaker: an error from the LLM, or a timeout of a call that had the full
// rerankMaxBudget. A call the caller cancelled, or one that timed out under
// a budget the recall deadline had shortened, changes no breaker state.
const (
	// rerankMaxBudget caps one rerank call. The rerank asks for one line of
	// indices over a prompt of a few KB (a bounded query plus at most
	// top_k*overfetch atoms of atom_max_chars), which a responsive model
	// answers well inside 2 s. Turn-start recall has a 5 s budget, so the
	// half-of-what-is-left rule alone would allow about 2.5 s there; the cap
	// keeps a slow call to 2 s, about 3 s short of that deadline. It also
	// bounds callers that set no deadline (explicit atom search), which
	// would otherwise wait for the memory-LLM client timeout.
	rerankMaxBudget = 2 * time.Second
	// rerankMinBudget is the least time worth giving a rerank call. A
	// memory-LLM round trip (request, prompt processing of a few KB, one
	// line back) takes a few hundred milliseconds even on a responsive
	// model, so a call given less mostly times out and only spends the
	// turn's start time. With less than this left, the rerank is skipped
	// and the skip is not a failure. Together with the shortened-budget rule
	// above, a slow vector search that eats the recall budget cannot trip
	// the breaker: either no call is made, or its timeout does not count.
	rerankMinBudget = 300 * time.Millisecond

	rerankBreakerThreshold    = 2
	rerankBreakerBaseCooldown = time.Minute
	rerankBreakerMaxCooldown  = 15 * time.Minute

	// rerankBreakerRegistryMax bounds the per-directory breaker registry. A
	// process normally opens one memory directory; when more are opened, the
	// least recently used breaker is forgotten.
	rerankBreakerRegistryMax = 64
)

// rerankBudget returns how long one rerank call may take under ctx.
func rerankBudget(ctx context.Context) time.Duration {
	return rerankBudgetWithin(ctx, rerankMaxBudget)
}

// rerankBudgetWithin is rerankBudget with the cap given: half of what is
// left on ctx, never more than max.
func rerankBudgetWithin(ctx context.Context, max time.Duration) time.Duration {
	budget := max
	if dl, ok := ctx.Deadline(); ok {
		if half := time.Until(dl) / 2; half < budget {
			budget = half
		}
	}
	return budget
}

// rerankBreaker is the circuit breaker for the rerank call of one
// extended-memory directory. It is shared by every Recall on that directory,
// because serve and Telegram build a new agent (and Recall) per connection,
// run or message, and concurrent sessions use the same memory LLM.
type rerankBreaker struct {
	mu        sync.Mutex
	now       func() time.Time // nil means time.Now
	failures  int              // consecutive counted failures
	cooldown  time.Duration    // current cooldown; zero while closed
	openUntil time.Time
	probing   bool

	used uint64 // registry recency, guarded by rerankBreakers' mutex
}

var rerankBreakers = struct {
	sync.Mutex
	byDir map[string]*rerankBreaker
	seq   uint64
}{byDir: make(map[string]*rerankBreaker)}

// rerankBreakerFor returns the breaker shared by every Recall on dir.
func rerankBreakerFor(dir string) *rerankBreaker {
	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	rerankBreakers.Lock()
	defer rerankBreakers.Unlock()
	rerankBreakers.seq++
	if b, ok := rerankBreakers.byDir[key]; ok {
		b.used = rerankBreakers.seq
		return b
	}
	if len(rerankBreakers.byDir) >= rerankBreakerRegistryMax {
		var oldest string
		for k, b := range rerankBreakers.byDir {
			if oldest == "" || b.used < rerankBreakers.byDir[oldest].used {
				oldest = k
			}
		}
		delete(rerankBreakers.byDir, oldest)
	}
	b := &rerankBreaker{used: rerankBreakers.seq}
	rerankBreakers.byDir[key] = b
	return b
}

// setClock replaces the breaker's clock (tests).
func (b *rerankBreaker) setClock(now func() time.Time) {
	b.mu.Lock()
	b.now = now
	b.mu.Unlock()
}

func (b *rerankBreaker) clockLocked() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// begin reports whether a rerank call may run now, and whether that call is
// the probe of an open breaker. A nil breaker always allows.
func (b *rerankBreaker) begin() (allowed, probe bool) {
	if b == nil {
		return true, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cooldown == 0 {
		return true, false
	}
	if b.probing || b.clockLocked().Before(b.openUntil) {
		return false, false
	}
	b.probing = true
	return true, true
}

// end records the outcome of a call begin allowed. An inconclusive outcome
// (the caller cancelled the call, or it timed out under a shortened budget;
// rerankInconclusive) says nothing about the memory LLM: it changes no state, and
// an inconclusive probe lets the next turn probe.
func (b *rerankBreaker) end(probe bool, err error, inconclusive bool) {
	if b == nil {
		if err != nil {
			log.Printf("extended memory: rerank LLM call failed: %v", err)
		}
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false
	}
	switch {
	case err == nil:
		if b.cooldown > 0 {
			log.Printf("extended memory: rerank answered again; reranking resumed")
		}
		b.failures, b.cooldown, b.openUntil = 0, 0, time.Time{}
	case inconclusive:
	case probe && b.cooldown > 0:
		b.failures++
		b.cooldown *= 2
		if b.cooldown > rerankBreakerMaxCooldown {
			b.cooldown = rerankBreakerMaxCooldown
		}
		b.openUntil = b.clockLocked().Add(b.cooldown)
		log.Printf("extended memory: rerank probe failed: %v; skipping rerank for %v", err, b.cooldown)
	case b.cooldown > 0:
		// A call that started before the breaker opened: already accounted.
	default:
		b.failures++
		if b.failures < rerankBreakerThreshold {
			log.Printf("extended memory: rerank LLM call failed: %v", err)
			return
		}
		b.cooldown = rerankBreakerBaseCooldown
		b.openUntil = b.clockLocked().Add(b.cooldown)
		log.Printf("extended memory: rerank failed %d times in a row (last: %v); skipping rerank for %v", b.failures, err, b.cooldown)
	}
}

// isOpen reports whether the breaker is currently skipping reranks (open or
// waiting on a probe).
func (b *rerankBreaker) isOpen() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cooldown > 0
}

// callerCancelled reports whether a rerank error comes from the caller
// stopping the turn rather than from the memory LLM.
func callerCancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
}

// rerankInconclusive reports whether a failed rerank call says nothing about
// the memory LLM: the caller cancelled it, or it timed out (timedOut) under
// a budget shorter than the full cap because the recall deadline was near.
// Errors from the LLM itself, and timeouts of a call that had the full cap,
// are conclusive.
func rerankInconclusive(ctx context.Context, err error, timedOut, fullBudget bool) bool {
	return callerCancelled(ctx, err) || (timedOut && !fullBudget)
}
