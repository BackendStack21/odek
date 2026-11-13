package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// The session detail sent to the Web UI (GET, and the rename/pin POST that
// echoes the session) leaves out the untrusted-span gate state: the overflow
// filter alone is ~175 KB of base64 no client reads. The session file and
// the JSON export (the raw record) keep it.
func TestRED_SessionDetailOmitsTaintState(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{{Role: "user", Content: "hi"}}, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	sess.AuthToken = session.GenerateAuthToken()
	sess.TaintSpans = []session.TaintSpan{{Kind: "host", Value: "evil.example", Source: "web"}}
	sess.TaintOverflow.AddValue("host", "flooded.example")
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
		if strings.Contains(body, "taint_overflow") || strings.Contains(body, "taint_spans") {
			t.Errorf("%s: the session detail carries the untrusted-span state (%d bytes)", tc.method, len(body))
		}
		if !strings.Contains(body, `"messages"`) {
			t.Errorf("%s: the session detail lost its transcript: %.200s", tc.method, body)
		}
	}
	raw, err := os.ReadFile(store.Path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"taint_overflow"`) || !strings.Contains(string(raw), `"taint_spans"`) {
		t.Fatal("the session file lost its untrusted-span state")
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.TaintOverflow.HasValue("host", "flooded.example") || len(loaded.TaintSpans) != 1 {
		t.Fatal("serving the session changed its stored untrusted-span state")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/export?format=json", nil)
	req.Header.Set("X-Session-Token", sess.AuthToken)
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "")(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"taint_overflow"`) {
		t.Fatalf("the JSON export (the raw record) lost the untrusted-span state: status %d", w.Code)
	}
}
