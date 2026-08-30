// Dead-prompt regression tests: when the provider kills a turn (rate limit,
// auth failure, outage), the session on disk must still contain the user's
// prompt and a coherent turn-ending note — not end on a dangling tool result
// with the prompt vanished.
//
// Background: handlePrompt only persisted session state after a *completed*
// step (SetMessagesPersistCallback). A first-step LLM failure therefore
// returned the session unchanged and the prompt was lost — 7+ observed
// occurrences on 2026-08-29 (8-subagent 429 saturation + two evening news
// runs ending on a raw delegate_tasks tool result).
package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// alwaysUnauthorized spins a mock LLM that rejects every chat call with 401 —
// a non-retryable status, so the run fails within one attempt.
func alwaysUnauthorized(t *testing.T) *mockLLMServer {
	t.Helper()
	return mockLLM(t, func(w http.ResponseWriter, callCount int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	})
}

// TestServeRun_LLMFailure_PersistsUserPrompt pins the dead-prompt fix: even
// when RunWithMessages fails on the very first LLM call, the session file
// must contain the user's prompt.
func TestServeRun_LLMFailure_PersistsUserPrompt(t *testing.T) {
	llmSrv := alwaysUnauthorized(t)
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	_, resp := startTestRun(t, env, `{"content":"remember the word CRANBERRY","approval_timeout_seconds":10}`)
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("missing run_id in prompt response")
	}

	snap := waitRunStatus(t, runID, 30*time.Second)
	if s, _ := snap["status"].(string); s != "failed" {
		t.Fatalf("run status = %q, want %q (snapshot: %v)", s, "failed", snap)
	}

	sid, _ := snap["session_id"].(string)
	if sid == "" {
		t.Fatal("run snapshot missing session_id")
	}
	sess, err := env.store.Load(sid)
	if err != nil {
		t.Fatalf("load session %s: %v", sid, err)
	}

	for _, m := range sess.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "CRANBERRY") {
			return // prompt survived
		}
	}
	t.Fatalf("user prompt not persisted after LLM failure (dead prompt); session has %d messages", len(sess.Messages))
}

// TestServeRun_LLMFailure_PersistsGracefulTurnNote pins the soft-fail fix: a
// failed turn ends with an assistant-visible note explaining the abort, so
// the transcript never ends on a dangling tool result and a later turn has
// coherent context for the LLM.
func TestServeRun_LLMFailure_PersistsGracefulTurnNote(t *testing.T) {
	llmSrv := alwaysUnauthorized(t)
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	_, resp := startTestRun(t, env, `{"content":"recall STRINGBEAN","approval_timeout_seconds":10}`)
	runID, _ := resp["run_id"].(string)

	snap := waitRunStatus(t, runID, 30*time.Second)
	sid, _ := snap["session_id"].(string)
	if sid == "" {
		t.Fatal("run snapshot missing session_id")
	}
	sess, err := env.store.Load(sid)
	if err != nil {
		t.Fatalf("load session %s: %v", sid, err)
	}
	if len(sess.Messages) < 2 {
		t.Fatalf("session has %d messages, want prompt + turn note", len(sess.Messages))
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last message role = %q, want assistant turn-abort note", last.Role)
	}
	if !strings.Contains(last.Content, "aborted") {
		t.Fatalf("last message does not mark the turn as aborted: %q", last.Content)
	}
}

// TestServeRun_TransientThrottle_RetriesAndCompletes pins the resilience half:
// a single 429 mid-turn is absorbed by the client's retry/backoff and the run
// still completes.
func TestServeRun_TransientThrottle_RetriesAndCompletes(t *testing.T) {
	llmSrv := mockLLM(t, func(w http.ResponseWriter, callCount int) {
		if callCount == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":"1302"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"throttle survived"}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`))
	})
	defer llmSrv.Close()

	env := newRestRunEnv(t, llmSrv.URL, nil)
	_, resp := startTestRun(t, env, `{"content":"say hi","approval_timeout_seconds":10}`)
	runID, _ := resp["run_id"].(string)

	snap := waitRunStatus(t, runID, 30*time.Second)
	if s, _ := snap["status"].(string); s != "completed" {
		t.Fatalf("run status = %q, want completed after transient 429 (err: %v)", s, snap["error"])
	}
	if res, _ := snap["result"].(string); !strings.Contains(res, "throttle survived") {
		t.Fatalf("run result = %q, want the mock answer", res)
	}
}
