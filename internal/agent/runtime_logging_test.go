package agent

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
	"github.com/BackendStack21/odek/internal/runtimelog"
)

func TestRuntimeLoggingSessionAndDistinctTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"private answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "runtime.log")
	a, err := New(Config{Model: "test", BaseURL: server.URL, APIKey: "test-key", MaxIterations: 2, RuntimeLogOptions: &LoggingOptions{Path: path, Level: "debug", Surface: "library", MaxFiles: 4}, Limits: budget.Limits{InputCostPerMillionUSD: 2, OutputCostPerMillionUSD: 4}, InteractionMode: "off", NoProjectFile: true})
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
		var ev runtimelog.Record
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.SessionID != "session-123" || ev.TurnID == "" || ev.Surface != "library" {
			t.Fatalf("missing correlation: %+v", ev)
		}
		if ev.Event == "run.started" {
			turns[ev.TurnID] = true
		}
		if ev.Event == "run.finished" {
			finished++
			if ev.Metadata["cost_usd"] == nil {
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
		var ev runtimelog.Record
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Event == "model.call_failed" || ev.Event == "run.finished" {
			if ev.SessionID != "session" || ev.TurnID == "" || ev.Error == nil || ev.Error.Code != "provider.auth" || ev.Error.HTTPStatus != 401 {
				t.Fatalf("missing failure detail/correlation: %+v", ev)
			}
			found[ev.Event] = true
		}
	}
	if len(found) != 2 {
		t.Fatalf("missing failure lifecycle: %v", found)
	}
}
