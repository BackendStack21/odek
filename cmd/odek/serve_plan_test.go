package main

// Tests for GET /api/sessions/{id}/plan — the read-only structured plan
// view (docs/PLANNING.md Phase 3 surfaces, backend half). Auth treatment
// mirrors handleSessionByID because the handler is reached through it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/loop"
)

// planMessageFor renders a real plan message through the store renderer so
// tests exercise the exact grammar the engine persists.
func planMessageFor(t *testing.T, args string) llm.Message {
	t.Helper()
	rendered, err := loop.NewPlanStore(12, 2000).Execute(args)
	if err != nil {
		t.Fatalf("setup: plan Execute: %v", err)
	}
	return llm.Message{Role: "system", Content: rendered}
}

func TestHandleSessionByID_GET_Plan_Found(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the work"},
		planMessageFor(t, `{"verb":"create","steps":[{"id":"s1","title":"Scaffold"},{"id":"s2","title":"Wire flags","note":"use stdlib flag"}]}`),
	}, "test-model", "planned task")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/plan", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got struct {
		SessionID string `json:"session_id"`
		Version   int    `json:"version"`
		Steps     []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
			Note   string `json:"note"`
		} `json:"steps"`
		Found bool `json:"found"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Found {
		t.Error("found = false, want true")
	}
	if got.SessionID != sess.ID {
		t.Errorf("session_id = %q, want %q", got.SessionID, sess.ID)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].ID != "s1" || got.Steps[0].Title != "Scaffold" || got.Steps[0].Status != "pending" {
		t.Errorf("steps[0] = %+v, want s1/Scaffold/pending", got.Steps[0])
	}
	if got.Steps[1].Note != "use stdlib flag" {
		t.Errorf("steps[1].note = %q, want 'use stdlib flag'", got.Steps[1].Note)
	}
}

func TestHandleSessionByID_GET_Plan_NotFound(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]llm.Message{
		{Role: "user", Content: "no plans here"},
	}, "test-model", "plain task")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/plan", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for absent plan", w.Code)
	}
	var got sessionPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Found {
		t.Error("found = true, want false")
	}
	if got.Version != 0 || len(got.Steps) != 0 {
		t.Errorf("absent plan must carry zero version/steps: %+v", got)
	}
	if got.SessionID != sess.ID {
		t.Errorf("session_id = %q, want %q", got.SessionID, sess.ID)
	}
}

func TestHandleSessionByID_GET_Plan_CollapsedAllDone(t *testing.T) {
	store := newTestSessionStore(t)
	// A completed plan collapses to the single-line render: parseable, but
	// with no step rows. The view reports found=true, version set, steps [].
	ps := loop.NewPlanStore(12, 2000)
	script := []string{
		`{"verb":"create","steps":[{"id":"a","title":"A"},{"id":"b","title":"B"}]}`,
		`{"verb":"complete","step_id":"a"}`,
		`{"verb":"complete","step_id":"b"}`,
	}
	var rendered string
	var err error
	for _, args := range script {
		rendered, err = ps.Execute(args)
		if err != nil {
			t.Fatalf("plan script %s: %v", args, err)
		}
	}
	if !strings.HasPrefix(rendered, "[Current plan:") || strings.Contains(rendered, "\n") {
		t.Fatalf("setup: expected collapsed single-line render, got:\n%s", rendered)
	}
	sess, err := store.Create([]llm.Message{{Role: "system", Content: rendered}}, "test-model", "done task")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/plan", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got sessionPlanResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Found || got.Version == 0 || len(got.Steps) != 0 {
		t.Errorf("collapsed plan view = %+v, want found=true, version set, steps []", got)
	}
}

func TestHandleSessionByID_GET_Plan_UnknownSession(t *testing.T) {
	store := newTestSessionStore(t)
	handler := handleSessionByID(store, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/20260101-aabbcc/plan", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown session id", w.Code)
	}
}

func TestHandleSessionByID_POST_Plan_IsReadOnly(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]llm.Message{
		{Role: "user", Content: "original task"},
	}, "test-model", "before")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store, nil, "")
	body := strings.NewReader(`{"name":"hijacked"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/plan", body)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("POST to the plan URL must not succeed — the surface is read-only")
	}
	// The failed request must not have fallen through to the rename path.
	after, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if after.Task != "before" {
		t.Errorf("session task mutated through plan URL: %q", after.Task)
	}
}
