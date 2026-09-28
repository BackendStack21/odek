package odek

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/events"
)

func TestRuntimeLoggingSessionAndDistinctTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"private answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "runtime.log")
	a, err := New(Config{Model: "test", BaseURL: server.URL, APIKey: "test-key", MaxIterations: 2, RuntimeLogPath: path, Limits: budget.Limits{InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 4}, InteractionMode: "off", NoProjectFile: true})
	if err != nil {
		t.Fatal(err)
	}
	a.SetToolSessionID("session-123") // binds events as well as tool artifacts
	for i := 0; i < 2; i++ {
		if _, err := a.Run(t.Context(), "private task"); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "test-key") {
		t.Fatal("private content leaked")
	}
	turns := map[string]bool{}
	finished := 0
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		var ev events.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "run_started" {
			if ev.SessionID != "" {
				t.Fatal("fabricated pre-session ID")
			}
			continue
		}
		if ev.SessionID != "session-123" || ev.TurnID == "" {
			t.Fatalf("missing correlation: %+v", ev)
		}
		if ev.Type == "turn_started" {
			turns[ev.TurnID] = true
		}
		if ev.Type == "run_completed" {
			finished++
			if ev.Data["cost_usd"] == nil {
				t.Fatal("missing configured cost")
			}
		}
	}
	if len(turns) != 2 || finished != 2 {
		t.Fatalf("turns=%v finished=%d", turns, finished)
	}
}

func TestRuntimeLoggingProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE response body","type":"authentication_error"}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "runtime.log")
	a, err := New(Config{Model: "test", BaseURL: server.URL, APIKey: "PRIVATE-key", RuntimeLogPath: path, NoProjectFile: true, InteractionMode: "off", EventContext: events.Context{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(t.Context(), "PRIVATE prompt"); err == nil {
		t.Fatal("provider failure swallowed")
	}
	_ = a.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatal("request/provider text leaked")
	}
	found := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "llm_call_failed" || ev.Type == "run_failed" {
			if ev.SessionID != "session" || ev.TurnID == "" || ev.Data["error_class"] != "provider_auth" || ev.Data["http_status"] != float64(401) {
				t.Fatalf("missing failure detail/correlation: %+v", ev)
			}
			found[ev.Type] = true
		}
	}
	if len(found) != 2 {
		t.Fatalf("missing failure lifecycle: %v", found)
	}
}
