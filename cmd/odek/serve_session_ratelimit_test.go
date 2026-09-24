// Rate-limit semantics for GET /api/sessions/{id}[/plan]: requests carrying a
// valid per-session token must never be throttled — the WebUI Now panel polls
// these endpoints every 1–3s while a turn runs, and the 60/min per-IP budget
// was being consumed by the legitimate client alone (random 429s). Enumeration
// protection stays intact: requests without a valid session token still count
// against the limiter.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func newRateLimitTestStore(t *testing.T) *session.Store {
	t.Helper()
	return newTestSessionStore(t)
}

// Token-bearing requests survive far beyond the 60/min budget.
func TestHandleSessionByID_GET_ValidTokenBypassesRateLimit(t *testing.T) {
	store := newRateLimitTestStore(t)
	sess, _ := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store, nil, "")
	for i := 0; i < 65; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
		req.Header.Set("X-Session-Token", sess.AuthToken)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (valid token must not be rate limited)", i, w.Code)
		}
	}
}

// The plan sub-path shares the endpoint and must inherit the bypass — the Now
// panel's 1s busy poll hits it directly.
func TestHandleSessionByID_GET_PlanViewBypassesRateLimit(t *testing.T) {
	store := newRateLimitTestStore(t)
	sess, _ := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store, nil, "")
	for i := 0; i < 65; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/plan", nil)
		req.Header.Set("X-Session-Token", sess.AuthToken)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (plan view must not be rate limited)", i, w.Code)
		}
	}
}

// Tokenless enumeration is still throttled — the bypass never weakens the
// brute-force protection on the 128-bit ID space.
func TestHandleSessionByID_GET_TokenlessStillRateLimited(t *testing.T) {
	store := newRateLimitTestStore(t)
	sess, _ := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store, nil, "")
	saw := 0
	for i := 0; i < 65; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code == http.StatusTooManyRequests {
			saw++
		}
	}
	if saw == 0 {
		t.Fatal("tokenless requests were never rate limited — enumeration protection lost")
	}
}

// An INVALID token is a rejected request: it burns limiter budget and hits
// 429 once exhausted — the bypass is reserved for validated callers.
func TestHandleSessionByID_GET_InvalidTokenStillRateLimited(t *testing.T) {
	store := newRateLimitTestStore(t)
	sess, _ := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store, nil, "")
	saw429 := false
	for i := 0; i < 70; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
		req.Header.Set("X-Session-Token", "wrong-token")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: status = %d, want 401", i, w.Code)
		}
	}
	if !saw429 {
		t.Fatal("invalid-token requests were never rate limited")
	}
}

// The X-Odek-Ws-Token bootstrap path is a one-shot convenience, not an
// unlimited probe channel: it pays limiter budget like any other rejected
// initial validation.
func TestHandleSessionByID_GET_WsTokenBootstrapRateLimited(t *testing.T) {
	store := newRateLimitTestStore(t)
	sess, _ := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")

	sessionLookupLimiter.reset()
	defer sessionLookupLimiter.reset()

	handler := handleSessionByID(store, nil, "instance-tok")
	saw429 := false
	for i := 0; i < 70; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
		req.Header.Set("X-Odek-Ws-Token", "instance-tok")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}
	if !saw429 {
		t.Fatal("ws-token bootstrap requests were never rate limited — unlimited probe channel")
	}
}
