package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// TestRED_SubagentConfigDisablesUserMessageExtraction: an agent built with
// Config.Subagent keeps Extended Memory recall but never extracts atoms from
// its incoming messages — those are tasks written by the parent model, and
// storing them would label model-authored text as user_approved.
func TestRED_SubagentConfigDisablesUserMessageExtraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	mk := func(sub bool) *Agent {
		t.Helper()
		cfg := memory.DefaultMemoryConfig()
		cfg.Extended = &extended.Config{Enabled: memory.BoolPtr(true)}
		a, err := New(Config{
			APIKey:        "sk-test",
			BaseURL:       server.URL,
			Model:         "test-model",
			NoProjectFile: true,
			MemoryDir:     t.TempDir(),
			MemoryConfig:  cfg,
			Subagent:      sub,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}

	if mk(true).Memory().UserMessageExtractionEnabled() {
		t.Fatal("sub-agent: user-message atom extraction must be off")
	}
	if !mk(false).Memory().UserMessageExtractionEnabled() {
		t.Fatal("parent: user-message atom extraction must stay on")
	}
}
