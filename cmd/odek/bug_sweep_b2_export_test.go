package main

// Bug-sweep batch 2 — /export route alias regression test.
//
// RED-first: handleSessionByID stripped the /export suffix for ALL methods
// while only GET dispatches to the export handler. DELETE /api/sessions/{id}/export
// therefore deleted the session and POST .../export renamed it — destructive
// aliases through a documented read-only route. (The sibling /plan guard is
// GET-only for exactly this reason.)

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

func TestSessionExportSuffix_NotAliasedForMutatingMethods(t *testing.T) {
	store := newTestSessionStore(t)

	sess, err := store.Create([]llm.Message{
		{Role: "user", Content: "hello"},
	}, "test-model", "greeting task")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	handler := handleSessionByID(store, nil, "")

	// DELETE through the export URL must NOT delete the session.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sess.ID+"/export", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	handler(w, req)
	if w.Code == http.StatusNoContent {
		t.Fatalf("DELETE /export fell through to base-session delete (status 204) — destructive alias")
	}
	if _, err := store.Load(sess.ID); err != nil {
		t.Fatalf("session %s was deleted through the /export URL alias: %v", sess.ID, err)
	}

	// POST through the export URL must NOT rename the session.
	body := strings.NewReader(`{"name":"renamed-via-export"}`)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/export", body)
	req2.Header.Set("X-Session-Token", sess.AuthToken)
	handler(w2, req2)
	if w2.Code == http.StatusOK {
		t.Fatalf("POST /export fell through to session rename (status 200) — destructive alias")
	}
	after, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("session %s missing after POST /export: %v", sess.ID, err)
	}
	if after.Task == "renamed-via-export" {
		t.Fatalf("session renamed through the /export URL alias")
	}
}
