package main

import (
	"bytes"
	"github.com/BackendStack21/odek/internal/session"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Security review wave C, F4: validateSessionToken auto-mints a token for
// legacy (pre-token-defense) sessions and returns ok regardless of what
// the caller presented — so on MUTATION paths (rename/pin, delete, cancel)
// an instance-cookie-only holder could act on any legacy session. Mutations
// must fail closed: a minted bootstrap token must actually be presented.
func TestSessionMutations_LegacyEmptyTokenFailsClosed(t *testing.T) {
	store := newTestSessionStore(t)
	// Legacy session: no auth token on disk.
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "legacy task")
	if err != nil {
		t.Fatal(err)
	}
	sess.AuthToken = ""
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	// Rename/pin mutation with NO session token presented.
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, bytes.NewReader([]byte(`{"pinned":true}`)))
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("mutation on a legacy session without presenting the minted token: status = %d, want 401 (fail closed)", w.Code)
	}
}
