package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/tool"
)

// blockingTool simulates a long-running tool call.
type blockingTool struct {
	name  string
	delay time.Duration
}

func (b *blockingTool) Name() string        { return b.name }
func (b *blockingTool) Description() string { return "blocks for a fixed delay" }
func (b *blockingTool) Schema() any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (b *blockingTool) Call(args string) (string, error) {
	time.Sleep(b.delay)
	return "ok", nil
}

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

func TestToolHeartbeat_LongRunningToolEmitsSignals(t *testing.T) {
	old := toolHeartbeatInterval
	toolHeartbeatInterval = 50 * time.Millisecond
	defer func() { toolHeartbeatInterval = old }()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"slow","arguments":"{}"}}]}}]}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	}))
	defer server.Close()

	slowTool := &blockingTool{name: "slow", delay: 300 * time.Millisecond}
	registry := tool.NewRegistry([]tool.Tool{slowTool})
	client := testChatClient(t, server.URL)
	engine := New(client, registry, 10, "", nil, 0)

	var mu sync.Mutex
	var signals []SignalEvent
	engine.SetSignalHandler(func(ev SignalEvent) {
		mu.Lock()
		signals = append(signals, ev)
		mu.Unlock()
	})

	if _, err := engine.Run(context.Background(), "run the slow tool"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	countBeats := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, ev := range signals {
			if ev.Type == "tool_running" {
				n++
			}
		}
		return n
	}

	mu.Lock()
	for _, ev := range signals {
		if ev.Type != "tool_running" {
			continue
		}
		if ev.Tool != "slow" {
			t.Errorf("tool_running signal tool = %q, want %q", ev.Tool, "slow")
		}
		if !strings.HasPrefix(ev.Detail, "running for ") {
			t.Errorf("tool_running signal detail = %q, want \"running for ...\"", ev.Detail)
		}
	}
	mu.Unlock()

	beats := countBeats()
	if beats == 0 {
		t.Fatal("expected at least one tool_running signal for a 300ms tool call at a 50ms interval")
	}

	// After the call returned the watchdog must have stopped: the count
	// stays stable across two more intervals.
	time.Sleep(2 * toolHeartbeatInterval)
	if after := countBeats(); after != beats {
		t.Errorf("tool_running signals kept firing after the call returned: %d -> %d", beats, after)
	}
}
