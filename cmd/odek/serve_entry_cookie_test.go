package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEntryCookieDoesNotFillTokenMeta pins that a token cookie (minted by an
// earlier version, and sent to every port of the host) no longer unlocks the
// token meta tag: only the tokenized URL does.
func TestEntryCookieDoesNotFillTokenMeta(t *testing.T) {
	handler := handleStatic("tok-123")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: wsTokenCookieName, Value: "tok-123"})
	w := httptest.NewRecorder()
	handler(w, r)
	if strings.Contains(w.Body.String(), "tok-123") {
		t.Errorf("a cookie must not fill the token meta tag, body: %.200s", w.Body.String())
	}
}
