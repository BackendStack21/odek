package llmclient

import (
	"fmt"
	"sync"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/redact"

	sdk "github.com/BackendStack21/go-llm-sdk"
)

// LearnEvent reports a provider learn-once fallback engaging (SDK v0.7.0):
// the provider rejected streaming or a request parameter and the SDK
// permanently adjusted its behavior for that provider.
type (
	LearnEvent = sdk.LearnEvent
	LearnKind  = sdk.LearnKind
)

const (
	LearnBuffered       = sdk.LearnBuffered
	LearnResponses      = sdk.LearnResponses
	LearnNoneEffort     = sdk.LearnNoneEffort
	LearnDropStreamOpts = sdk.LearnDropStreamOpts
)

// learnDetailMax is the clamp for provider-supplied text (message and
// provider id) in client-facing detail lines — a verbose or hostile
// gateway must not be able to push megabytes through a signal frame.
const learnDetailMax = 2048

// learnDetailClamp truncates s to learnDetailMax bytes, marking the cut.
func learnDetailClamp(s string) string {
	if len(s) <= learnDetailMax {
		return s
	}
	// Cut on a rune boundary.
	n := learnDetailMax
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// LearnDetail renders the event as one human-readable line for clients and
// logs. Provider-supplied text (id, error message) is redacted and clamped:
// it reaches WebSocket clients and UI toasts verbatim otherwise.
func LearnDetail(e LearnEvent) string {
	provider := learnDetailClamp(redact.RedactSecrets(e.Provider))
	message := learnDetailClamp(redact.RedactSecrets(e.Message))
	detail := fmt.Sprintf("provider %q fallback engaged: %s", provider, e.Kind)
	switch e.Kind {
	case LearnBuffered:
		detail = fmt.Sprintf("provider %q downgraded to buffered responses; streaming (live reasoning) is off for this provider", provider)
	case LearnResponses:
		detail = fmt.Sprintf("provider %q requires the /responses endpoint for reasoning with tools", provider)
	case LearnNoneEffort:
		detail = fmt.Sprintf("provider %q rejected reasoning_effort with tools; effort pinned to none", provider)
	case LearnDropStreamOpts:
		detail = fmt.Sprintf("provider %q rejected stream_options; field omitted", provider)
	}
	if e.Status != 0 {
		detail += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if message != "" {
		detail += ": " + message
	}
	return detail
}

// learnSinks holds process-wide subscribers for learn events. Serve
// connections register on connect and unregister on disconnect so every
// attached client sees provider fallbacks; the diagnostics emission gives
// operational logging a permanent record regardless of subscribers.
var learnSinks struct {
	mu    sync.RWMutex
	next  int
	sinks map[int]func(LearnEvent)
}

// RegisterLearnSink subscribes fn to provider learn events and returns an
// idempotent unregister function. Sinks run on the request goroutine that
// triggered the fallback (at most once per provider per flag) — keep them
// fast and never call back into the SDK.
func RegisterLearnSink(fn func(LearnEvent)) (unregister func()) {
	learnSinks.mu.Lock()
	id := learnSinks.next
	learnSinks.next++
	if learnSinks.sinks == nil {
		learnSinks.sinks = make(map[int]func(LearnEvent))
	}
	learnSinks.sinks[id] = fn
	learnSinks.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			learnSinks.mu.Lock()
			delete(learnSinks.sinks, id)
			learnSinks.mu.Unlock()
		})
	}
}

// dispatchLearnEvent fans one event out to diagnostics and all sinks.
func dispatchLearnEvent(ev LearnEvent) {
	diagnostics.Emit(events.Event{
		Type: "provider_learn_fallback",
		Data: map[string]any{
			"provider": ev.Provider,
			"kind":     string(ev.Kind),
			"status":   ev.Status,
			"message":  ev.Message,
		},
	})
	learnSinks.mu.RLock()
	fns := make([]func(LearnEvent), 0, len(learnSinks.sinks))
	for _, fn := range learnSinks.sinks {
		fns = append(fns, fn)
	}
	learnSinks.mu.RUnlock()
	for _, fn := range fns {
		fn(ev)
	}
}

var learnBridgeOnce sync.Once

// installLearnBridge wires the SDK's process-wide learn observer to odek's
// dispatch chain exactly once. NewSDK calls it on every construction; the
// SDK observer itself is a single process-wide slot, so first install wins
// and later ones are no-ops.
func installLearnBridge() {
	learnBridgeOnce.Do(func() {
		sdk.SetLearnObserver(func(ev sdk.LearnEvent) {
			dispatchLearnEvent(ev)
		})
	})
}
