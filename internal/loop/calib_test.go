package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/tool"
)

// Margin calibration compares provider-reported prompt size with the local
// estimate of the whole request. When the provider reports cached tokens
// separately (exclusive semantics), the calibration input must be the full
// prompt window, not just the uncached remainder.
func TestRED_MarginCalibrationUsesFullPromptWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":19000}}}`)
	}))
	defer server.Close()
	e := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 3, "sys", nil, 100000)
	if _, err := e.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if e.lastPromptTokens <= 0 {
		t.Skipf("provider usage not parsed (window=%d)", e.lastPromptTokens)
	}
	if e.lastReportedInputTokens != e.lastPromptTokens {
		t.Fatalf("calibration input %d != full prompt window %d: cached tokens are excluded so an underestimating tokenizer is never detected", e.lastReportedInputTokens, e.lastPromptTokens)
	}
}
