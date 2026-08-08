package odek

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

// eventsTestServer answers one tool-call round then a final answer. The
// /models discovery request issued by New() is answered separately so it
// does not consume a scripted round.
func eventsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{
				"choices":[{"message":{
					"content":"Working.",
					"tool_calls":[{"id":"call_1","function":{"name":"echo","arguments":"{\"text\":\"hi\"}"}}]
				}}],
				"usage":{"prompt_tokens":10,"completion_tokens":4}
			}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"all done"}}],"usage":{"prompt_tokens":20,"completion_tokens":6}}`)
		}
	}))
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echoes input" }
func (echoTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (echoTool) Call(args string) (string, error) { return "echo ok", nil }

type eventList struct {
	mu  sync.Mutex
	evs []events.Event
}

func (l *eventList) handle(ev events.Event) {
	l.mu.Lock()
	l.evs = append(l.evs, ev)
	l.mu.Unlock()
}

func (l *eventList) types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ev := range l.evs {
		out = append(out, ev.Type)
	}
	return out
}

func (l *eventList) all() []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]events.Event(nil), l.evs...)
}

func TestAgent_Events_FullRunLifecycle(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	col := &eventList{}
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		Tools:         []Tool{echoTool{}},
		EventHandler:  col.handle,
	})
	if err != nil {
		t.Fatal(err)
	}

	if agent.RunID() == "" {
		t.Fatal("RunID() empty with EventHandler configured")
	}

	result, err := agent.Run(context.Background(), "test task")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "all done" {
		t.Fatalf("result = %q, want %q", result, "all done")
	}
	agent.Close() // drains the emitter so all events are delivered

	types := col.types()
	if len(types) == 0 || types[0] != events.TypeRunStarted {
		t.Fatalf("first event = %v, want run_started", types)
	}
	if types[len(types)-1] != events.TypeRunCompleted {
		t.Fatalf("last event = %v, want run_completed", types)
	}

	// Every event carries the same run_id; run_started has the run metadata;
	// run_completed has totals.
	evs := col.all()
	for i, ev := range evs {
		if ev.RunID != agent.RunID() {
			t.Errorf("event %d (%s) run_id = %q, want %q", i, ev.Type, ev.RunID, agent.RunID())
		}
	}
	start := evs[0]
	if start.Data["model"] != "test-model" {
		t.Errorf("run_started model = %v", start.Data["model"])
	}
	if start.Data["sandbox"] != false {
		t.Errorf("run_started sandbox = %v, want false", start.Data["sandbox"])
	}
	if start.Data["max_iterations"] != defaultMaxIter {
		t.Errorf("run_started max_iterations = %v, want %d", start.Data["max_iterations"], defaultMaxIter)
	}
	completed := evs[len(evs)-1]
	if completed.Data["input_tokens"] != 30 {
		t.Errorf("run_completed input_tokens = %v, want 30", completed.Data["input_tokens"])
	}
	if completed.Data["output_tokens"] != 10 {
		t.Errorf("run_completed output_tokens = %v, want 10", completed.Data["output_tokens"])
	}
	if _, ok := completed.Data["duration_ms"]; !ok {
		t.Error("run_completed missing duration_ms")
	}

	// The loop-level events flow through the same pipeline.
	seen := map[string]bool{}
	for _, ty := range types {
		seen[ty] = true
	}
	for _, want := range []string{events.TypeToolCallStarted, events.TypeToolCallCompleted, events.TypeIterationCompleted} {
		if !seen[want] {
			t.Errorf("missing %s in %v", want, types)
		}
	}
}

func TestAgent_Events_RunFailed(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	col := &eventList{}
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		EventHandler:  col.handle,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agent.Run(ctx, "test task"); err == nil {
		t.Fatal("expected error from cancelled context")
	}
	agent.Close() // drains the emitter

	types := col.types()
	if len(types) < 2 || types[0] != events.TypeRunStarted || types[len(types)-1] != events.TypeRunFailed {
		t.Fatalf("event types = %v, want [run_started … run_failed]", types)
	}
	last := col.all()[len(types)-1]
	if last.Data["error_class"] != "context_canceled" {
		t.Errorf("run_failed error_class = %v, want context_canceled", last.Data["error_class"])
	}
}

func TestAgent_Events_PanickingHandlerDoesNotCrashLoop(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		Tools:         []Tool{echoTool{}},
		EventHandler:  func(events.Event) { panic("boom") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	result, err := agent.Run(context.Background(), "test task")
	if err != nil {
		t.Fatalf("Run() error with panicking handler: %v", err)
	}
	if result != "all done" {
		t.Errorf("result = %q, want %q", result, "all done")
	}
}

func TestAgent_Events_SlowHandlerDoesNotStallLoop(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		Tools:         []Tool{echoTool{}},
		EventHandler:  func(events.Event) { time.Sleep(50 * time.Millisecond) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	done := make(chan error, 1)
	go func() {
		_, err := agent.Run(context.Background(), "test task")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error with slow handler: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loop stalled behind a slow event handler")
	}
}

func TestAgent_Events_SessionIDOnceKnown(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	col := &eventList{}
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		EventHandler:  col.handle,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	agent.SetEventSessionID("sess-42")
	// Caller-originated event (same path the CLI uses for session_saved).
	agent.EmitEvent(events.Event{Type: events.TypeSessionSaved, Data: map[string]any{"message_count": 3}})

	if _, err := agent.Run(context.Background(), "test task"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	agent.Close() // drains the emitter

	evs := col.all()
	// run_started fired in New, before the session was known.
	if evs[0].Type != events.TypeRunStarted || evs[0].SessionID != "" {
		t.Errorf("run_started session_id = %q, want empty", evs[0].SessionID)
	}
	// Everything after SetEventSessionID carries it.
	for i, ev := range evs[1:] {
		if ev.SessionID != "sess-42" {
			t.Errorf("event %d (%s) session_id = %q, want sess-42", i+1, ev.Type, ev.SessionID)
		}
	}
}

func TestAgent_Events_BudgetExceededPrecedesRunFailed(t *testing.T) {
	server := eventsTestServer(t)
	defer server.Close()

	col := &eventList{}
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		EventHandler:  col.handle,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	// WP6 wires the enforcement trigger; the emission site and constants are
	// exercised here via a direct emitter call, as a budget stop would do
	// right before returning its typed error.
	agent.EmitEvent(events.Event{
		Type: events.TypeBudgetExceeded,
		Data: map[string]any{
			"limit_name": events.LimitInputTokens,
			"observed":   150000,
			"limit":      128000,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = agent.Run(ctx, "test task")
	agent.Close() // drains the emitter

	types := col.types()
	budgetIdx, failedIdx := -1, -1
	for i, ty := range types {
		if ty == events.TypeBudgetExceeded {
			budgetIdx = i
		}
		if ty == events.TypeRunFailed {
			failedIdx = i
		}
	}
	if budgetIdx == -1 || failedIdx == -1 {
		t.Fatalf("event types = %v, want budget_exceeded and run_failed", types)
	}
	if budgetIdx >= failedIdx {
		t.Errorf("budget_exceeded (idx %d) must precede run_failed (idx %d)", budgetIdx, failedIdx)
	}
	for _, ev := range col.all() {
		if ev.Type != events.TypeBudgetExceeded {
			continue
		}
		if ev.Data["limit_name"] != events.LimitInputTokens {
			t.Errorf("limit_name = %v", ev.Data["limit_name"])
		}
		if ev.Data["observed"] != 150000 || ev.Data["limit"] != 128000 {
			t.Errorf("observed/limit = %v/%v", ev.Data["observed"], ev.Data["limit"])
		}
	}
}

func TestAgent_Events_NoHandlerNoEmitter(t *testing.T) {
	agent, err := New(Config{APIKey: "sk-test", NoProjectFile: true})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if agent.RunID() != "" {
		t.Error("RunID() should be empty without an EventHandler")
	}
	// These must be safe no-ops.
	agent.SetEventSessionID("x")
	agent.EmitEvent(events.Event{Type: events.TypeRunStarted})
}
