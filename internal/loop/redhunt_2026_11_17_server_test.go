package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/tool"
)

// serverCardCommands answers the first model call with one shell call per
// command and every later call with a final answer.
func serverCardCommands(t *testing.T, commands ...string) *httptest.Server {
	t.Helper()
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls > 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
			return
		}
		var b strings.Builder
		b.WriteString(`{"choices":[{"message":{"content":"","tool_calls":[`)
		for i, c := range commands {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"call_%d","function":{"name":"shell","arguments":"{\"command\":\"%s\"}"}}`, i, c)
		}
		b.WriteString(`]}}]}`)
		fmt.Fprint(w, b.String())
	}))
}

// serverCardsFor runs one batch of shell calls and returns the batch cards the
// approver received.
func serverCardsFor(t *testing.T, note string, commands ...string) []string {
	t.Helper()
	registry := tool.NewRegistry([]tool.Tool{&mockTool{name: "shell", result: "done"}})
	server := serverCardCommands(t, commands...)
	defer server.Close()
	approver := &cardCapture{}
	engine := New(testChatClient(t, server.URL), registry, 10, "", nil, 0)
	engine.SetApprover(approver)
	engine.SetMaxToolParallel(len(commands))
	allow := "allow"
	engine.SetDangerousConfig(&danger.DangerousConfig{
		DefaultAction: &allow,
		Classes:       map[danger.RiskClass]danger.Action{danger.Destructive: danger.Prompt},
	})
	if note != "" {
		engine.SetApprovalCardNote(note)
	}
	if _, err := engine.Run(context.Background(), "run the tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	return append([]string(nil), approver.cards...)
}

// Every approval surface (Web UI, Telegram, terminal, denial errors) shows
// the batch card through the display sanitizer. The card's own text is
// first-party, so the sanitizer must have nothing to escape in it: an escape
// such as \ufe0f on the card is noise the person has to read past.
func TestRED_BatchCardFirstPartyTextNeedsNoDisplayEscapes(t *testing.T) {
	for name, tc := range map[string]struct {
		note     string
		commands []string
		header   string
	}{
		"one action":      {commands: []string{"rm -rf /etc/test0", "echo hi"}, header: "The following tool action requires approval:"},
		"several actions": {commands: []string{"rm -rf /etc/test0", "rm -rf /etc/test1"}, header: "2 tool actions require approval:"},
		"with a note":     {note: "Sandbox network: none; attached: app_default", commands: []string{"rm -rf /etc/test0", "rm -rf /etc/test1"}, header: "2 tool actions require approval:"},
	} {
		t.Run(name, func(t *testing.T) {
			cards := serverCardsFor(t, tc.note, tc.commands...)
			if len(cards) != 1 {
				t.Fatalf("got %d batch cards, want 1", len(cards))
			}
			card := cards[0]
			if got := danger.SanitizeForDisplay(card); got != card {
				t.Fatalf("display sanitizer changed the batch card:\n got: %q\nwant: %q", got, card)
			}
			for _, line := range strings.Split(strings.TrimSuffix(card, "\n"), "\n") {
				if got := danger.SanitizeInline(line); got != line {
					t.Fatalf("inline sanitizer changed card line %q into %q", line, got)
				}
			}
			if first := strings.SplitN(card, "\n", 2)[0]; first != tc.header {
				t.Fatalf("card header = %q, want %q", first, tc.header)
			}
		})
	}
}

// serverFlakyProvider answers the first model call with HTTP 503 (Retry-After:
// 1 keeps the SDK's backoff short) and every later call with then(w).
func serverFlakyProvider(t *testing.T, then func(w http.ResponseWriter)) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"upstream busy","type":"server_error"}}`)
			return
		}
		then(w)
	}))
	return srv, &calls
}

// While the SDK retries a failed model call, the engine reports each new
// attempt as an llm_retry signal (attempt number, attempt budget and the
// status that failed), so a client can show that the provider is being
// retried instead of an unexplained wait.
func TestRED_ProviderRetryIsSignalled(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			srv, calls := serverFlakyProvider(t, func(w http.ResponseWriter) {
				fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
			})
			defer srv.Close()
			engine := New(testChatClient(t, srv.URL), tool.NewRegistry(nil), 3, "", nil, 0)
			if stream {
				engine.SetStream(true)
				engine.SetDeltaHandler(func(llmclient.Delta) error { return nil })
			}
			var mu sync.Mutex
			var retries []SignalEvent
			engine.SetSignalHandler(func(ev SignalEvent) {
				if ev.Type == "llm_retry" {
					mu.Lock()
					retries = append(retries, ev)
					mu.Unlock()
				}
			})
			if _, err := engine.Run(context.Background(), "hello"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if *calls != 2 {
				t.Fatalf("provider saw %d requests, want 2 (one retry)", *calls)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(retries) != 1 {
				t.Fatalf("llm_retry signals = %+v, want exactly one", retries)
			}
			if got := retries[0]; got.Count != 2 || got.Detail != "HTTP 503, attempt 2 of 8" {
				t.Fatalf("llm_retry = {Count:%d Detail:%q}, want {Count:2 Detail:%q}", got.Count, got.Detail, "HTTP 503, attempt 2 of 8")
			}
		})
	}
}
