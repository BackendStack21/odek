package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/session"
)

// Browser limits can only tighten operator caps and never supply model prices.
type serveRunLimits struct {
	Runtime int64   `json:"max_runtime_seconds,omitempty"`
	Tools   int64   `json:"max_tool_calls,omitempty"`
	Input   int64   `json:"max_input_tokens,omitempty"`
	Output  int64   `json:"max_output_tokens,omitempty"`
	Cost    float64 `json:"max_cost_usd,omitempty"`
}

func (l *serveRunLimits) resolve(base budget.Limits) (budget.Limits, error) {
	if l == nil {
		return base, nil
	}
	if l.Runtime < 0 || l.Tools < 0 || l.Input < 0 || l.Output < 0 || l.Cost < 0 || math.IsNaN(l.Cost) || math.IsInf(l.Cost, 0) {
		return base, fmt.Errorf("run limits must be finite and non-negative")
	}
	lower := func(current, requested int64) int64 {
		if requested > 0 && (current == 0 || requested < current) {
			return requested
		}
		return current
	}
	base.MaxRuntimeSeconds = lower(base.MaxRuntimeSeconds, l.Runtime)
	base.MaxToolCalls = lower(base.MaxToolCalls, l.Tools)
	base.MaxInputTokens = lower(base.MaxInputTokens, l.Input)
	base.MaxOutputTokens = lower(base.MaxOutputTokens, l.Output)
	if l.Cost > 0 && (base.MaxCostUSD == 0 || l.Cost < base.MaxCostUSD) {
		base.MaxCostUSD = l.Cost
	}
	return base, nil
}

func handleWorkspace(resolved config.ResolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "workspace unavailable", http.StatusInternalServerError)
			return
		}
		classes := map[string]string{}
		for _, cls := range []danger.RiskClass{danger.Persistence, danger.UnreadExec, danger.Blocked, danger.Safe, danger.LocalWrite, danger.SystemWrite, danger.Destructive, danger.NetworkEgress, danger.CodeExecution, danger.Install, danger.Unknown} {
			classes[string(cls)] = string(resolved.Dangerous.ActionFor(cls))
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"workspace": cwd, "sandbox": resolved.Sandbox, "policy": classes, "limits": resolved.Limits, "model": resolved.Model, "features": map[string]bool{"run_limits": true, "recovery": true, "turn_settled": true, "permissions": true}})
	}
}

type recoveryAction struct {
	ID      string `json:"call_id"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

func recoveryView(sess *session.Session) map[string]any {
	start := 0
	for i, m := range sess.Messages {
		if m.Role == "user" && m.Name != "bg-wake" && m.Name != "bg-notice" {
			start = i
		}
	}
	original := ""
	if len(sess.Messages) > start {
		m := sess.Messages[start]
		if m.Role == "user" {
			if m.PrincipalPrompt != nil {
				original = *m.PrincipalPrompt
			} else if !strings.Contains(m.Content, "<untrusted_content_") {
				// Legacy expanded input cannot safely become fresh principal instructions.
				original = m.Content
			}
		}
	}
	completed, failed, uncertain := []recoveryAction{}, []recoveryAction{}, []recoveryAction{}
	calls := map[string]string{}
	order := []string{}
	flushPending := func() {
		for _, id := range order {
			if name, ok := calls[id]; ok {
				uncertain = append(uncertain, recoveryAction{ID: id, Name: name, Outcome: "unknown"})
				delete(calls, id)
			}
		}
		order = nil
	}
	for _, m := range sess.Messages[start:] {
		// Providers may reuse call IDs in later assistant groups. A later return
		// must not erase an earlier action whose outcome was never recorded.
		if len(m.ToolCalls) > 0 {
			flushPending()
		}
		for _, call := range m.ToolCalls {
			calls[call.ID] = call.Function.Name
			order = append(order, call.ID)
		}
		if m.Role != "tool" {
			continue
		}
		name := calls[m.ToolCallID]
		if name == "" {
			name = m.Name
		}
		item := recoveryAction{ID: m.ToolCallID, Name: name, Outcome: m.ToolOutcome}
		switch m.ToolOutcome {
		case "completed":
			completed = append(completed, item)
		case "failed":
			failed = append(failed, item)
		default:
			uncertain = append(uncertain, item)
		}
		delete(calls, m.ToolCallID)
	}
	flushPending()
	return map[string]any{"session_id": sess.ID, "revision": sess.Revision, "generation": sess.Generation, "completed": completed, "failed": failed, "uncertain": uncertain, "original_prompt": original, "decisions": sess.Decisions, "warning": "A recorded tool return is not proof of task success. Interrupted actions may have side effects without a saved result; inspect the workspace before continuing."}
}

// The execution lock makes the snapshot usable only after an interrupted run
// releases ownership. Continuation checks its revision again under that lock.
func handleRecovery(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/recovery")
		sess, err := store.Load(id)
		if err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if !validateSessionTokenStrict(store, sess, sessionTokenFromRequest(r)) {
			http.Error(w, "invalid session token", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		release, err := store.AcquireExecution(ctx, id)
		if err != nil {
			http.Error(w, "Work is still settling. Refresh recovery shortly.", http.StatusConflict)
			return
		}
		defer release()
		sess, err = store.Load(id)
		if err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if !validateSessionTokenStrict(store, sess, sessionTokenFromRequest(r)) {
			http.Error(w, "invalid session token", http.StatusUnauthorized)
			return
		}
		writeAPIJSON(w, http.StatusOK, recoveryView(sess))
	}
}

func handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	loc, err := time.LoadLocation(r.URL.Query().Get("timezone"))
	if err != nil {
		http.Error(w, "invalid timezone", http.StatusBadRequest)
		return
	}
	cron := r.URL.Query().Get("cron")
	if len(cron) > 256 {
		http.Error(w, "schedule too long", http.StatusBadRequest)
		return
	}
	expr, err := schedule.ParseInLocation(cron, loc)
	if err != nil {
		http.Error(w, "invalid cron schedule", http.StatusBadRequest)
		return
	}
	next := []time.Time{}
	at := time.Now()
	for range 3 {
		at = expr.Next(at)
		next = append(next, at)
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"next": next, "timezone": loc.String()})
}
