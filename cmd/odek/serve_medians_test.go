package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// uniqueIP returns a loopback address unused by other tests so the shared
// package-level sessionLookupLimiter budget is not polluted.
func uniqueIP(n int) string { return "127.0.0.99:" + itoa(40000+n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestRecoveryRateLimited pins the adversarial finding that the
// /api/sessions/{id}/recovery dispatch returns before the session-lookup
// rate limiter gates rejected lookups, giving an unbounded exists/404
// oracle and unbounded token-guess attempts on this route. Rejected
// recovery lookups must burn the same per-IP limiter budget as the base
// session detail path.
func TestRecoveryRateLimited(t *testing.T) {
	store := newTestSessionStore(t)
	// Drain the limiter from one dedicated IP with unknown-id requests.
	var last int
	for i := 0; i < 70; i++ {
		r := httptest.NewRequest("GET", "/api/sessions/does-not-exist/recovery", nil)
		r.RemoteAddr = "127.0.0.98:41234"
		w := httptest.NewRecorder()
		handleRecovery(store, nil)(w, r)
		last = w.Code
		if last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("unthrottled recovery lookups: after 70 rejected requests last code = %d, want 429", last)
	}
}

// TestClientIPRejectsMalformedProxyHeaders pins the finding that
// clientIP uses X-Real-Ip / X-Forwarded-For values verbatim, letting a
// client behind a trusted proxy rotate arbitrary garbage keys to defeat
// rate-limit buckets. Unparseable header values must fall back to the
// socket peer address.
func TestClientIPRejectsMalformedProxyHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("X-Real-Ip", "not-an-ip")
	if got := clientIP(r, []string{"127.0.0.1"}); got != "127.0.0.1" {
		t.Errorf("clientIP with malformed X-Real-Ip = %q, want socket peer 127.0.0.1", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "127.0.0.1:55556"
	r2.Header.Set("X-Forwarded-For", "garbage-key")
	if got := clientIP(r2, []string{"127.0.0.1"}); got != "127.0.0.1" {
		t.Errorf("clientIP with malformed XFF = %q, want socket peer 127.0.0.1", got)
	}
	// Valid header values are still honored.
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.RemoteAddr = "127.0.0.1:55557"
	r3.Header.Set("X-Real-Ip", "10.1.2.3")
	if got := clientIP(r3, []string{"127.0.0.1"}); got != "10.1.2.3" {
		t.Errorf("clientIP with valid X-Real-Ip = %q, want 10.1.2.3", got)
	}
}

// TestInstanceTokenBootstrapMintOnly pins the finding that the
// instance-token bootstrap on GET /api/sessions/{id} returns the ENTIRE
// session JSON — full transcripts and reasoning — to any caller holding
// only the per-instance token. The bootstrap must mint the session token
// (X-Session-Token header) without echoing the session body; callers then
// re-fetch the detail with the minted per-session token like every other
// client.
func TestInstanceTokenBootstrapMintOnly(t *testing.T) {
	store := newTestSessionStore(t)
	sess, err := store.Create([]session.Message{
		{Role: "user", Content: "secret transcript contents"},
	}, "fixture", "Task")
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	r.Header.Set("X-Odek-Ws-Token", "inst")
	w := httptest.NewRecorder()
	handleSessionByID(store, nil, "inst")(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap: status %d", w.Code)
	}
	if w.Header().Get("X-Session-Token") != sess.AuthToken {
		t.Errorf("bootstrap must mint X-Session-Token, got %q", w.Header().Get("X-Session-Token"))
	}
	if strings.Contains(w.Body.String(), "secret transcript contents") {
		t.Errorf("bootstrap must not echo the session transcript body: %s", w.Body.String())
	}

	// A caller with the minted per-session token still gets the full body.
	r2 := httptest.NewRequest("GET", "/api/sessions/"+sess.ID, nil)
	r2.Header.Set("X-Session-Token", sess.AuthToken)
	w2 := httptest.NewRecorder()
	handleSessionByID(store, nil, "inst")(w2, r2)
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), "secret transcript contents") {
		t.Errorf("per-session-token detail fetch broken: %d %s", w2.Code, w2.Body.String())
	}
}
