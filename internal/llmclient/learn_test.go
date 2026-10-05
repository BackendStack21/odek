package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/session"
)

// The learn bridge must fan SDK learn-once fallbacks out to registered
// sinks and to the diagnostics/event stream. Driven end to end: a custom
// provider that answers a streamed request with plain JSON (the exact
// silent-downgrade failure mode that hides mid-turn reasoning from
// clients) must produce one sink event AND one llm event.
func TestLearnBridge_FanOutOnBufferedDowngrade(t *testing.T) {
	restoreDiag := installLearnEventCapture(t)
	defer restoreDiag()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"buffered"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()

	s, err := NewSDK(Options{
		Provider: "gw",
		Model:    "m",
		Providers: map[string]ProviderOverride{
			"gw": {APIKey: "k", BaseURL: srv.URL, Format: "openai"},
		},
	})
	if err != nil {
		t.Fatalf("NewSDK: %v", err)
	}
	c, err := New(s, "gw", "m")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sinkCh := make(chan LearnEvent, 4)
	unregister := RegisterLearnSink(func(ev LearnEvent) {
		sinkCh <- ev
	})
	defer unregister()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.CallStream(ctx, []session.Message{{Role: "user", Content: "hi"}}, nil, func(Delta) error {
		return nil
	}); err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	select {
	case ev := <-sinkCh:
		if ev.Kind != LearnBuffered {
			t.Errorf("sink Kind = %q, want %q", ev.Kind, LearnBuffered)
		}
		if ev.Provider != "gw" {
			t.Errorf("sink Provider = %q, want gw", ev.Provider)
		}
		if ev.Status != 0 {
			t.Errorf("sink Status = %d, want 0 (non-SSE 2xx path carries no APIError)", ev.Status)
		}
		if LearnDetail(ev) == "" {
			t.Error("LearnDetail must produce a human-readable line")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no learn event reached the sink within 5s")
	}

	ev := <-learnEventCapture
	if ev.Type != "provider_learn_fallback" {
		t.Errorf("event Type = %q, want provider_learn_fallback", ev.Type)
	}
	if ev.Data["provider"] != "gw" || ev.Data["kind"] != string(LearnBuffered) {
		t.Errorf("event Data = %+v, want provider/kind fields", ev.Data)
	}
}

// Unregistered sinks must not receive events, and a nil-safe unregister is
// part of the contract (serve connections unregister on disconnect).
func TestLearnBridge_UnregisterStopsFanOut(t *testing.T) {
	installLearnBridge()

	var mu sync.Mutex
	calls := 0
	unregister := RegisterLearnSink(func(LearnEvent) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	unregister()
	unregister() // double-unregister must be a no-op

	dispatchLearnEvent(LearnEvent{Kind: LearnBuffered, Provider: "x"})
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("calls = %d, want 0 after unregister", calls)
	}
}

// ── test helpers ──────────────────────────────────────────────────────────

var learnEventCapture = make(chan events.Event, 8)

func installLearnEventCapture(t *testing.T) func() {
	t.Helper()
	installLearnBridge()
	restore := diagnostics.Install(func(ev events.Event) {
		if ev.Type == "provider_learn_fallback" {
			select {
			case learnEventCapture <- ev:
			default:
			}
		}
	})
	t.Cleanup(restore)
	return restore
}
