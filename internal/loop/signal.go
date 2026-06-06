package loop

import "time"

// SignalEvent represents an internal agent-loop signal that was previously
// invisible to the operator — moments where the engine silently intervened to
// keep the session alive or productive. Surfacing these closes observability
// gaps around context management and tool-failure recovery.
//
// Not every field is set for every Type; the zero value means "not applicable".
type SignalEvent struct {
	// Type is the signal kind. One of:
	//   "context_trimmed"  — prior message groups were dropped to fit the token
	//                        budget (Count = groups dropped, Detail = "proactive"
	//                        for the pre-call budget trim or "survival" for the
	//                        post-error nuclear trim)
	//   "tool_recovery"    — a tool failed repeatedly and the engine injected a
	//                        corrective hint so the model changes approach
	//                        (Tool = failing tool, Detail = the correction)
	Type      string
	Detail    string    // human-readable detail (mode, correction text, etc.)
	Tool      string    // tool name for tool_recovery
	Count     int       // groups dropped (context_trimmed)
	Timestamp time.Time // when the signal fired (UTC)
}

// SignalHandler receives agent-loop signal events. Implementations must be
// non-blocking — signals fire inside the hot loop.
type SignalHandler func(event SignalEvent)

// emitSignal fires the engine's signal handler if one is configured, stamping
// the timestamp when the caller left it zero. Safe to call unconditionally.
func (e *Engine) emitSignal(ev SignalEvent) {
	if e.signalHandler == nil {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	e.signalHandler(ev)
}

// SetSignalHandler sets the optional agent-loop signal callback. Passing nil
// disables signal emission.
func (e *Engine) SetSignalHandler(cb SignalHandler) { e.signalHandler = cb }
