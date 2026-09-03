package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/tool"
)

// TestRunWithMessages_SurvivalRetryDoesNotConsumeIterationSlot pins the
// semantics of the context-length survival retry: `continue // retry this
// iteration` must retry the SAME iteration. A plain `continue` runs the
// loop's i++ post statement, so the recovery consumes an iteration slot —
// with maxIter=1 (or on the final iteration) a fully recoverable
// context-length error degrades into the "[Iteration budget reached]"
// partial-summary path instead of retrying with the trimmed history.
func TestRunWithMessages_SurvivalRetryDoesNotConsumeIterationSlot(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Provider rejects the request as over the context window.
			// The client surfaces the body, which isContextLengthError matches.
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 8192 tokens."}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"recovered after trim"}}]}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test", "test-model", "", 0, 0)
	registry := tool.NewRegistry(nil)
	engine := New(client, registry, 1, "sys", nil, 0)

	// History long enough for trimToSurvival to drop something
	// (len > 3 with droppable middle turns).
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task one"},
		{Role: "assistant", Content: "did step one"},
		{Role: "user", Content: "task two"},
		{Role: "assistant", Content: "did step two"},
		{Role: "user", Content: "final question"},
	}

	result, _, err := engine.RunWithMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("RunWithMessages error: %v (context-length recovery should retry, not fail)", err)
	}
	if result != "recovered after trim" {
		t.Errorf("result = %q, want the retried final answer %q (no iteration-budget marker)", result, "recovered after trim")
	}
	if strings.Contains(result, "Iteration budget reached") {
		t.Errorf("result must not carry the partial-summary marker: %q", result)
	}
}
