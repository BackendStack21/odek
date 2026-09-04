package main

import (
	"bytes"
	"github.com/BackendStack21/odek/internal/session"
	"net/http"
	"net/http/httptest"
	"testing"
)

// POST /api/sessions/{id} is the ONLY session mutation without a request
// body cap: every other mutation handler wraps r.Body in
// http.MaxBytesReader (1 MiB) or decodeJSONBody (2 MiB). A client (even a
// token-holder) streaming a multi-gigabyte body OOMs the server.
func TestHandleSessionByID_PostBodySizeCapped(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}

	// A 2 MiB value INSIDE the JSON: the decoder must read past the 1 MiB
	// cap to parse it (trailing whitespace after the value is never read,
	// so it cannot trip the cap).
	body := append(append([]byte(`{"name":"`), bytes.Repeat([]byte("x"), 2<<20)...), []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, bytes.NewReader(body))
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()

	handleSessionByID(store, nil, "")(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized (2 MiB+) mutation body: status = %d, want 400 (body cap); today it is decoded and accepted", w.Code)
	}
}
