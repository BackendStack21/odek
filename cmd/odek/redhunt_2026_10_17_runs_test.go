package main

// RED tests for the 2026-10-17 Web UI bug hunt, area "runs" (serve-side
// headless runs, jobs, wake-on-complete, artifacts). Each test encodes the
// CORRECT behavior and fails on the code at v2.34.11.

import (
	"encoding/json"
	"fmt"
	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/session"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// /api/usage prices every token at the server's configured model, so a REST
// run with a per-run model override (documented on POST /api/prompt) is
// priced with the wrong model's model_prices entry.
func TestRED_RunsUsageCostIgnoresPerRunModelPrices(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"priced"}}],"usage":{"prompt_tokens":1000,"completion_tokens":1000}}`))
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, func(rc *config.ResolvedConfig) {
		rc.Limits.InputCostPerMillionUSD = 0
		rc.Limits.OutputCostPerMillionUSD = 0
		rc.Limits.ModelPrices = map[string]budget.ModelPrice{
			"runs-priced-model": {InputCostPerMillionUSD: 100, OutputCostPerMillionUSD: 100},
		}
	})
	resetServeUsageForTest()
	t.Cleanup(resetServeUsageForTest)

	_, resp := startTestRun(t, env, `{"content":"price me","model":"runs-priced-model"}`)
	runID, _ := resp["run_id"].(string)
	snap := waitRunStatus(t, runID, 30*time.Second)
	if snap["status"] != "completed" {
		t.Fatalf("run status = %v (error %v)", snap["status"], snap["error"])
	}
	if atomic.LoadInt64(&serveStats.TokensIn) == 0 {
		t.Fatal("precondition: run tokens were not accumulated")
	}

	w := httptest.NewRecorder()
	handleUsage(env.resolved)(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	var usage map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	in := float64(atomic.LoadInt64(&serveStats.TokensIn))
	out := float64(atomic.LoadInt64(&serveStats.TokensOut))
	want := (in + out) / 1e6 * 100
	cost, _ := usage["estimated_cost_usd"].(float64)
	if usage["prices_configured"] != true || cost < want*0.99 {
		t.Fatalf("usage for a run on runs-priced-model ($100/M): prices_configured=%v estimated_cost_usd=%v, want ~%v (tokens priced at the configured model %q instead of the run's model)",
			usage["prices_configured"], cost, want, env.resolved.Model)
	}
}

// The wake router decides "idle" from WebSocket connections only. A
// headless REST run executing on the same session is invisible to it, so a
// wake turn is queued on the attached connection while the session is busy:
// the connection blocks on the session execution lock until the REST run
// ends (its own iterations already drained the completion notice) and then
// spends a wake turn — and a max_wakes_per_hour slot — on nothing.
func TestRED_RunsWakeRouterIgnoresActiveRESTRunOnSession(t *testing.T) {
	resetServeRuns()
	t.Cleanup(resetServeRuns)

	const sid = "20261017-runs-wake-busy"
	conn := &wsConnInfo{ID: "conn-runs-wake-busy", SessionID: sid, ConnectedAt: time.Now()}
	wsConnRegister(conn)
	t.Cleanup(func() { wsConnUnregister(conn.ID) })

	run := &serveRun{ID: "run-runs-wake-busy", SessionID: sid, Status: "running", StartedAt: time.Now(), pending: map[string]*approvalRequest{}}
	registerRun(run)

	if got := (wsWakeRouter{}).State(sid); got != wakeBusy {
		t.Fatalf("wake state with a running REST run on the session = %v, want wakeBusy (%v); the dispatcher will post a wake turn into a busy session", got, wakeBusy)
	}
}

// Finished REST runs stay in the registry (up to 100) together with their
// cleanup closure, which captures the whole agent — and through the
// engine's per-turn persist callback, the run's full in-memory session.
// Bounding the event tail (an earlier fix) therefore does not bound what a
// finished run pins: every retained run keeps its entire transcript alive.
func TestRED_RunsFinishedRunPinsAgentAndSession(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
	})
	defer llmSrv.Close()
	env := newRestRunEnv(t, llmSrv.URL, nil)
	resetServeRuns()
	t.Cleanup(resetServeRuns)

	// One large prior turn in the session (well under the 32 MiB file cap).
	payload := 6 << 20
	if testing.Short() {
		payload = 64 << 10
	}
	sess, err := env.store.Create([]session.Message{
		{Role: "system", Content: ""},
		{Role: "user", Content: strings.Repeat("a", payload)},
		{Role: "assistant", Content: "noted"},
	}, env.resolved.Model, "big")
	if err != nil {
		t.Fatal(err)
	}

	measure := func() uint64 {
		var ms runtime.MemStats
		for i := 0; i < 3; i++ {
			runtime.GC()
		}
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	base := measure()

	runs := 4
	if testing.Short() {
		runs = 1
	}
	var finished []*serveRun
	for i := 0; i < runs; i++ {
		body := fmt.Sprintf(`{"content":"turn %d","session_id":%q,"auth_token":%q}`, i, sess.ID, sess.AuthToken)
		_, resp := startTestRun(t, env, body)
		runID, _ := resp["run_id"].(string)
		snap := waitRunStatus(t, runID, 60*time.Second)
		if snap["status"] != "completed" {
			t.Fatalf("run %d status = %v (error %v)", i, snap["status"], snap["error"])
		}
		run := lookupRun(runID)
		deadline := time.Now().Add(20 * time.Second)
		for {
			run.mu.Lock()
			active := run.resourcesActive
			run.mu.Unlock()
			if !active {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("run resources never released")
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Structural check (runs in -short mode too): teardown must drop
		// the references that keep the agent and its session reachable.
		run.mu.Lock()
		pinned := run.cleanup != nil || run.cancel != nil || run.approver != nil
		run.mu.Unlock()
		if pinned {
			t.Fatalf("run %d: finished run still references its cleanup/cancel/approver (pins the agent and session)", i)
		}
		finished = append(finished, run)
	}
	if testing.Short() {
		return
	}
	after := measure()
	runtime.KeepAlive(finished)

	var grown uint64
	if after > base {
		grown = after - base
	}
	// Without retention the finished runs hold only their bounded tails;
	// with it each run pins a full parsed copy of the 6 MiB transcript.
	if grown > uint64(payload)*3/2 {
		t.Fatalf("%d finished runs still pin %.1f MiB of heap (session payload %d MiB each)",
			runs, float64(grown)/(1<<20), payload>>20)
	}
}
