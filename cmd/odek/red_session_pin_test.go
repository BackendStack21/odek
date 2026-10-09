package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// POST /api/sessions/{id} (rename/pin) must not report success when the
// session could not be persisted. The handler discards store.Save's error and
// echoes the in-memory mutation with 200, so the client believes the pin or
// rename stuck while the on-disk session is unchanged.
func TestRED_SessionPinRename_SaveFailureReported(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "m", "original")
	if err != nil {
		t.Fatal(err)
	}
	sess.AuthToken = session.GenerateAuthToken()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, strings.NewReader(`{"pinned":true}`))
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(w, req)

	os.Chmod(dir, 0o700)
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Pinned && w.Code == http.StatusOK {
		t.Fatalf("handler returned 200 but pin was not persisted (pinned=%v)", reloaded.Pinned)
	}
}
