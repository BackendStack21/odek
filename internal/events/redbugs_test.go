package events

import (
	"strings"
	"testing"
	"time"
)

// RED #10 (S2): A handler that calls Emitter.Close runs on the dispatch
// goroutine; Close blocks on wg.Wait() for that same goroutine's Done,
// so the dispatch goroutine deadlocks forever.
func TestRED_CloseFromHandlerDoesNotDeadlock(t *testing.T) {
	var e *Emitter
	handlerDone := make(chan struct{})
	e = NewEmitter(func(ev Event) {
		// Handlers are allowed to tear the emitter down (e.g. a run_failed
		// handler closing the stream).
		e.Close()
		close(handlerDone)
	}, "run-1")

	e.Emit(Event{Type: TypeRunFailed})

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Emitter.Close deadlocked the dispatch goroutine when called from a handler")
	}

	extDone := make(chan struct{})
	go func() { e.Close(); close(extDone) }()
	select {
	case <-extDone:
	case <-time.After(2 * time.Second):
		t.Fatal("external Close blocked after handler-initiated close")
	}
}

// RED #11 (S5): Emit redacts string values by writing through the
// caller's Data map, mutating state the caller still owns (and racing
// if the same map is emitted twice concurrently).
func TestRED_EmitDoesNotMutateCallerData(t *testing.T) {
	e := NewEmitter(nil, "run-1")
	defer e.Close()

	data := map[string]any{
		"note": "my key is sk-" + strings.Repeat("a1", 20),
		"n":    42,
	}
	e.Emit(Event{Type: TypeIterationCompleted, Data: data})

	if data["note"] != "my key is sk-"+strings.Repeat("a1", 20) {
		t.Fatalf("Emit mutated the caller's Data map in place: %q", data["note"])
	}
	if _, ok := data["n"].(int); !ok {
		t.Fatalf("Emit changed non-string value type: %T", data["n"])
	}
}
