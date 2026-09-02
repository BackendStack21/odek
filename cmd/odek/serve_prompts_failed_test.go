package main

// Regression test for batch-3 finding B3-SERVE-1: the prompts_failed usage
// counter had zero increment sites — failed prompts never surfaced in
// /api/usage. A run that ends in the handlePrompt error funnel must bump
// the counter (WS and REST share the funnel).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
)

func TestServe_FailedRunBumpsPromptsFailed(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	resetServeUsageForTest()
	t.Cleanup(resetServeUsageForTest)

	_, resp := startTestRun(t, env, `{"content":"say something"}`)
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run_id in response: %v", resp)
	}
	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "failed" {
		t.Fatalf("precondition: run status = %v, want failed (error: %v)", snap["status"], snap["error"])
	}

	if got := atomic.LoadInt64(&serveStats.PromptsFailed); got < 1 {
		t.Errorf("BUG B3-SERVE-1: prompts_failed = %d after a run with status=failed, want >= 1", got)
	}

	// The operator-facing lifetime aggregate reports it too.
	w := httptest.NewRecorder()
	handleUsage(config.ResolvedConfig{Model: "m"})(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got, _ := body["prompts_failed"].(float64); got < 1 {
		t.Errorf("BUG B3-SERVE-1: /api/usage prompts_failed = %v, want >= 1", body["prompts_failed"])
	}
}
