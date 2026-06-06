package loop

import "testing"

func TestEmitSignal_NilHandlerIsSafe(t *testing.T) {
	e := &Engine{}
	// No handler set — must be a no-op, not a panic.
	e.emitSignal(SignalEvent{Type: "context_trimmed"})
}

func TestSetSignalHandler_ReceivesEventsAndStampsTime(t *testing.T) {
	e := &Engine{}
	var got []SignalEvent
	e.SetSignalHandler(func(ev SignalEvent) { got = append(got, ev) })

	e.emitSignal(SignalEvent{Type: "context_trimmed", Detail: "survival", Count: 3})
	e.emitSignal(SignalEvent{Type: "tool_recovery", Tool: "shell", Detail: "try a different approach"})

	if len(got) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(got))
	}
	if got[0].Type != "context_trimmed" || got[0].Count != 3 || got[0].Detail != "survival" {
		t.Errorf("unexpected first signal: %+v", got[0])
	}
	if got[0].Timestamp.IsZero() {
		t.Error("expected timestamp to be stamped on emit")
	}
	if got[1].Tool != "shell" {
		t.Errorf("expected tool=shell, got %q", got[1].Tool)
	}
}

func TestSetSignalHandler_NilDisables(t *testing.T) {
	e := &Engine{}
	called := false
	e.SetSignalHandler(func(SignalEvent) { called = true })
	e.SetSignalHandler(nil)
	e.emitSignal(SignalEvent{Type: "tool_recovery"})
	if called {
		t.Error("handler should not fire after being set to nil")
	}
}
