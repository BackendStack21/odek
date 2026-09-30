package odek

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func TestSwitchModelRefreshesAutomaticContextWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"custom-small","context_length":4096}]}`)
	}))
	defer server.Close()
	for _, configured := range []int{0, 8192} {
		t.Run(fmt.Sprintf("configured_%d", configured), func(t *testing.T) {
			a, err := New(Config{Provider: "deepseek", BaseURL: server.URL, APIKey: "test-key", Model: "deepseek-v4-flash", ContextWindow: configured, NoProjectFile: true, MemoryDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, tc := range []struct {
				model  string
				window int
			}{
				{"gpt-4o", 128000}, {"custom-small", 4096}, {"unknown-window", 0}, {"deepseek-v4-flash", 1000000},
			} {
				a.SwitchModel(tc.model)
				want := tc.window
				if configured != 0 {
					want = configured
				}
				if got := a.MaxContextTokens(); got != want {
					t.Errorf("switch to %s: context window=%d, want %d", tc.model, got, want)
				}
			}
		})
	}
}

func TestSwitchToSmallerModelTrimsBeforeRequest(t *testing.T) {
	var requestBytes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []session.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		var size int64
		for _, m := range body.Messages {
			size += int64(len(m.Content))
		}
		requestBytes.Store(size)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer server.Close()
	a, err := New(Config{Provider: "deepseek", BaseURL: server.URL, APIKey: "test-key", Model: "deepseek-v4-flash", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.SwitchModel("gpt-4o")
	_, _, err = a.RunWithMessages(t.Context(), []session.Message{
		{Role: "system"}, {Role: "user", Content: "original task"},
		{Role: "assistant", Content: strings.Repeat("x", 600000)},
		{Role: "user", Content: "follow up"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := requestBytes.Load(); got == 0 || got >= 500000 {
		t.Fatalf("smaller model received untrimmed history: %d content bytes", got)
	}
}
