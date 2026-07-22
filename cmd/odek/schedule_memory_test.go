package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// TestRunTaskHeadless_MemoryWired proves that scheduled (headless) jobs get
// the same memory wiring as interactive runs: a pre-seeded extended-memory
// atom must reach the model's context. Regression test for the gap where
// runTaskHeadless built the agent without MemoryDir/MemoryConfig.
func TestRunTaskHeadless_MemoryWired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed an extended-memory atom in the (temp) global memory dir.
	extCfg := extended.DefaultConfig()
	enabled := true
	extCfg.Enabled = &enabled
	em := extended.New(home+"/.odek/memory/extended", nil, extCfg)
	if err := em.AddAtom(context.Background(), extended.MemoryAtom{
		Text:        "The user's favorite editor is Emacs.",
		SourceClass: extended.SourceUserApproved,
		Type:        extended.TypePreference,
	}); err != nil {
		t.Fatalf("seed atom: %v", err)
	}

	// Mock LLM: capture the chat request body, answer with a final response.
	var sawAtom atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "favorite editor is Emacs") {
			sawAtom.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	resolved := config.LoadConfig(config.CLIFlags{})
	resolved.BaseURL = srv.URL + "/v1"
	resolved.APIKey = "test"
	resolved.Model = "mock-model"
	resolved.Memory.Extended = &extCfg

	_, _, err := runTaskHeadless(context.Background(), resolved, "you are a test agent", "what is my favorite editor?", nil)
	if err != nil {
		t.Fatalf("runTaskHeadless: %v", err)
	}
	if !sawAtom.Load() {
		t.Error("seeded memory atom did not reach the model context — memory is not wired into headless runs")
	}
}
