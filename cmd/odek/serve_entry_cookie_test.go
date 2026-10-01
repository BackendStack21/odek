package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEntryCookieFillsTokenMeta pins the token-in-URL hardening: a browser
// holding the minted HttpOnly cookie gets the populated token meta tag on a
// clean GET / (no query parameter), so the tokenized URL — which persists in
// browser history — is needed only once per browser.
func TestEntryCookieFillsTokenMeta(t *testing.T) {
	handler := handleStatic("tok-123")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: wsTokenCookieName, Value: "tok-123"})
	w := httptest.NewRecorder()
	handler(w, r)
	if !strings.Contains(w.Body.String(), "tok-123") {
		t.Errorf("valid cookie must fill the token meta tag, body: %.200s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("cookie-authenticated entry must be no-store")
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.AddCookie(&http.Cookie{Name: wsTokenCookieName, Value: "wrong"})
	w2 := httptest.NewRecorder()
	handler(w2, r2)
	if strings.Contains(w2.Body.String(), "tok-123") {
		t.Error("invalid cookie must not fill the token meta tag")
	}
}
