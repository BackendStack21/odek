package odek

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/events"
)

// TestAgent_Budget_LimitsWiredAndEventOrder pins the public-API wiring:
// Config.Limits reaches the loop engine, the run returns a typed
// budget.Error, and the event stream carries budget_exceeded BEFORE
// run_failed (schema odek.event/v1, docs/EXTENSIONS.md).
func TestAgent_Budget_LimitsWiredAndEventOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			// /models discovery etc. — no context window discovered.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// One tool call with usage that blows the input-token budget.
		fmt.Fprint(w, `{
			"choices":[{"message":{"content":"working","tool_calls":[{
				"id":"call_1","type":"function",
				"function":{"name":"noop","arguments":"{}"}
			}]}}],
			"usage":{"prompt_tokens":1000,"completion_tokens":10}
		}`)
	}))
	defer server.Close()

	var mu sync.Mutex
	var evs []events.Event
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		Model:         "test-model",
		NoProjectFile: true,
		MemoryDir:     t.TempDir(),
		Limits:        budget.Limits{MaxInputTokens: 100},
		EventHandler: func(ev events.Event) {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer agent.Close()

	_, runErr := agent.Run(context.Background(), "do work")
	berr, ok := budget.As(runErr)
	if !ok {
		t.Fatalf("expected typed budget.Error from Run, got %v", runErr)
	}
	if berr.Limit != budget.LimitInputTokens {
		t.Errorf("limit = %q, want %q", berr.Limit, budget.LimitInputTokens)
	}

	// Drain the async emitter before asserting on the stream.
	agent.Close()

	mu.Lock()
	defer mu.Unlock()
	budgetIdx, failedIdx := -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case events.TypeBudgetExceeded:
			if budgetIdx < 0 {
				budgetIdx = i
				if ev.Data["limit_name"] != budget.LimitInputTokens {
					t.Errorf("budget_exceeded limit_name = %v", ev.Data["limit_name"])
				}
			}
		case events.TypeRunFailed:
			if failedIdx < 0 {
				failedIdx = i
			}
		}
	}
	if budgetIdx < 0 {
		t.Fatal("budget_exceeded missing from event stream")
	}
	if failedIdx < 0 {
		t.Fatal("run_failed missing from event stream")
	}
	if budgetIdx > failedIdx {
		t.Errorf("budget_exceeded (idx %d) must precede run_failed (idx %d)", budgetIdx, failedIdx)
	}
}
