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
// First, one rerank call gets what is left of the recall budget minus a
// small reserve, so when it does not answer recall still has time to return
// the blended ranking, and never more than rerankMaxBudget. A memory model
// that answers inside the recall budget therefore still reranks, however
// slow it is; one that hangs costs at most one near-full budget per turn
// until the breaker opens.
//
// Second, a circuit breaker: after rerankBreakerThreshold consecutive
// failures the rerank is skipped for a cooldown that starts at
// rerankBreakerBaseCooldown and doubles on each failed probe up to
// rerankBreakerMaxCooldown. Once a cooldown has passed, one call probes the
// memory LLM; a success closes the breaker and resets the backoff. While it
// is open, recall uses the blended order and makes no rerank call.
//
// Only outcomes that say something about the memory LLM count toward the
// breaker: an error from the LLM, or a timeout of a call that had at least
// rerankFairBudget. A call the caller cancelled, or one that timed out with
// less than a fair budget (the vector search ate the rest of the recall
// budget), changes no breaker state. Such a short-budget timeout is still
// counted as a recall timeout and logged, at most once a minute.
const (
	// rerankMaxBudget caps one rerank call: the 5 s turn-start recall
	// budget, so a model that answers within it reranks as it did before
	// the rerank was bounded. It also bounds callers that set no deadline
	// (explicit atom search), which would otherwise wait for the memory-LLM
	// client timeout.
	rerankMaxBudget = 5 * time.Second
	// rerankFairBudget is the least budget under which a rerank timeout
	// says the memory LLM is unhealthy. The rerank asks for one line of
	// indices over a prompt of a few KB (a bounded query plus at most
	// top_k*overfetch atoms of atom_max_chars); a responsive model, local
	// or remote, answers that well inside 1.5 s, while a hung or overloaded
	// one does not answer at all.
	rerankFairBudget = 1500 * time.Millisecond
	// rerankMinBudget is the least time worth giving a rerank call. A
	// memory-LLM round trip (request, prompt processing of a few KB, one
	// line back) takes a few hundred milliseconds even on a responsive
	// model, so a call given less mostly times out and only spends the
	// turn's start time. With less than this left, the rerank is skipped and
	// the skip is not a failure.
	rerankMinBudget = 300 * time.Millisecond
	// rerankReserveMin is the least time held back from the rerank for
	// recall to sort, format and return once the call ends; the reserve is a
	// tenth of what is left when that is more.
	rerankReserveMin = 250 * time.Millisecond

	// rerankShortTimeoutLogEvery rate-limits the log line for rerank
	// timeouts under less than a fair budget, per breaker.
	rerankShortTimeoutLogEvery = time.Minute

	rerankBreakerThreshold    = 2
	rerankBreakerBaseCooldown = time.Minute
	rerankBreakerMaxCooldown  = 15 * time.Minute

	// rerankBreakerRegistryMax bounds the per-directory breaker registry. A
	// process normally opens one memory directory; when more are opened, the
	// least recently used breaker is forgotten.
	rerankBreakerRegistryMax = 64
)

// rerankLimits is the rerank budget rule: the cap, the fair budget, the
// floor and the minimum reserve.
type rerankLimits struct {
	max, fair, min, reserveMin time.Duration
}

// defaultRerankLimits is the rule recall runs with.
var defaultRerankLimits = rerankLimits{
	max:        rerankMaxBudget,
	fair:       rerankFairBudget,
	min:        rerankMinBudget,
	reserveMin: rerankReserveMin,
}

// scaledTo returns the rule with its cap set to max and every other limit
// scaled in proportion (tests run the rule in a fraction of its time).
func (l rerankLimits) scaledTo(max time.Duration) rerankLimits {
	scale := func(d time.Duration) time.Duration {
		return time.Duration(float64(d) * float64(max) / float64(l.max))
	}
	return rerankLimits{max: max, fair: scale(l.fair), min: scale(l.min), reserveMin: scale(l.reserveMin)}
}

// rerankBudgetWithin returns how long one rerank call may take under ctx:
// what is left on ctx minus a reserve (a tenth of it, at least
// l.reserveMin) so recall can still return the blended order, never more
// than l.max. Without a deadline it is l.max.
func rerankBudgetWithin(ctx context.Context, l rerankLimits) time.Duration {
	budget := l.max
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		reserve := max(left/10, l.reserveMin)
		if avail := left - reserve; avail < budget {
			budget = avail
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
	// shortLogged is when a short-budget timeout was last logged.
	shortLogged time.Time

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
// (the caller cancelled the call, or it timed out with less than a fair
// budget; rerankInconclusive) says nothing about the memory LLM: it changes
// no state, and an inconclusive probe lets the next turn probe.
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

// releaseProbe frees the probe slot of a probe whose outcome was never
// recorded (the call panicked), changing nothing else.
func (b *rerankBreaker) releaseProbe() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.probing = false
	b.mu.Unlock()
}

// logShortTimeout reports whether a rerank timeout under less than a fair
// budget should be logged now: at most once per rerankShortTimeoutLogEvery
// for this breaker. A nil breaker always logs.
func (b *rerankBreaker) logShortTimeout() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clockLocked()
	if !b.shortLogged.IsZero() && now.Sub(b.shortLogged) < rerankShortTimeoutLogEvery {
		return false
	}
	b.shortLogged = now
	return true
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
// the memory LLM: the caller cancelled it, or it timed out (timedOut) with
// less than a fair budget (fairBudget false) because the recall deadline was
// near. Errors from the LLM itself, and timeouts of a call that had a fair
// budget, are conclusive.
func rerankInconclusive(ctx context.Context, err error, timedOut, fairBudget bool) bool {
	return callerCancelled(ctx, err) || (timedOut && !fairBudget)
}
