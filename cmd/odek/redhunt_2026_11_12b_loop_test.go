package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// The session detail sent to the Web UI leaves out all of the untrusted-span
// gate state, the background-job fact and the scan coverage included: no
// client reads them, and the runtime reseeds the gate from the session file.
// The session file and the JSON export (the raw record) keep them.
func TestRED_SessionDetailOmitsGateFacts(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	sess.AuthToken = session.GenerateAuthToken()
	sess.TaintBackground = true
	sess.TaintCoverage = session.TaintCoverage{Messages: 1, Digest: strings.Repeat("ab", 32)}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/sessions/" + sess.ID, ""},
		{http.MethodPost, "/api/sessions/" + sess.ID, `{"pinned":true}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Session-Token", sess.AuthToken)
		w := httptest.NewRecorder()
		handleSessionByID(store, nil, "")(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.method, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, "taint_background") || strings.Contains(body, "taint_coverage") {
			t.Errorf("%s: the session detail carries the gate's background fact or scan coverage: %.300s", tc.method, body)
		}
		if !strings.Contains(body, `"messages"`) {
			t.Errorf("%s: the session detail lost its transcript: %.200s", tc.method, body)
		}
	}
	raw, err := os.ReadFile(store.Path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"taint_background"`) || !strings.Contains(string(raw), `"taint_coverage"`) {
		t.Fatal("the session file lost the gate's background fact or scan coverage")
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TaintBackground || loaded.TaintCoverage != sess.TaintCoverage {
		t.Fatal("serving the session changed its stored gate state")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/export?format=json", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"taint_coverage"`) {
		t.Fatalf("the JSON export (the raw record) lost the gate state: status %d", w.Code)
	}
}
