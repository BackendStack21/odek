package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// JSON session export must not leak the session-scoped auth token: the
// export file is meant to be shareable.
func TestSessionExportJSON_OmitsAuthToken(t *testing.T) {
	sess := &session.Session{ID: "sess-1234", AuthToken: "supersecret-token-value"}
	sess.AuthToken = "supersecret-token-value"
	w := httptest.NewRecorder()
	handleSessionExport(sess, "json", w)
	body := w.Body.String()
	msg := body
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if strings.Contains(body, "supersecret-token-value") || strings.Contains(body, "auth_token") {
		t.Fatalf("export leaks auth token: %s", msg)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(w.Body.Bytes()), &decoded); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
}
