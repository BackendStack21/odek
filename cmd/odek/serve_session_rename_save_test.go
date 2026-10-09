package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

func TestSessionRename_SaveFailureIs5xxAndSuccessPersists(t *testing.T) {
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
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID, strings.NewReader(body))
		req.Header.Set("X-Session-Token", sess.AuthToken)
		w := httptest.NewRecorder()
		handleSessionByID(store, nil, "")(w, req)
		return w
	}

	if w := post(`{"name":"renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.Load(sess.ID); got == nil || got.Task != "renamed" {
		t.Fatalf("rename not persisted: %+v", got)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	w := post(`{"name":"lost"}`)
	os.Chmod(dir, 0o700)
	if w.Code < 500 {
		t.Fatalf("rename with a failing save returned %d, want 5xx", w.Code)
	}
	if got, _ := store.Load(sess.ID); got == nil || got.Task != "renamed" {
		t.Fatalf("failed rename must leave the stored name untouched: %+v", got)
	}
}
