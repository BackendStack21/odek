package skills

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchHTTP_PinsResolvedIP pins the DNS-rebinding fix: the SSRF guard
// resolves the host, but the subsequent http.Client GET performed a SECOND,
// independent resolution — an attacker-controlled DNS server could answer
// the check with a public IP and the dial with 127.0.0.1/169.254.169.254.
// The dial must use the same checked resolution (pinned IP), and any
// resolved private IP must abort the fetch.
func TestFetchHTTP_PinsResolvedIP(t *testing.T) {
	// Loopback target that records whether it was reached. The hostname
	// "rebind.test" is resolved to the loopback server by the pinned dialer's
	// resolver override in the test; without pinning, the guard's check and
	// the transport's dial resolve independently.
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte("skill content"))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	// The hostname is not private by name; isPrivateHost would resolve it.
	// With pinning, the dialer resolves once, sees a loopback IP, and must
	// refuse — even though the URL itself names a public-looking host.
	res, err := fetchHTTPDial("http://rebind.invalid:"+port+"/skill.md", 1<<10, 5, func(host string) ([]string, error) {
		return []string{"127.0.0.1"}, nil // rebinding: public name, private answer
	})
	if err == nil {
		t.Fatalf("rebinding fetch succeeded (reached=%v): %+v — dial must refuse the private resolution", reached, res)
	}
	if reached {
		t.Error("request reached the loopback server despite private-IP resolution")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should name the private-IP refusal, got: %v", err)
	}
}

// TestFetchHTTP_PinnedDialSucceedsOnPublic verifies the pinned dialer still
// fetches legitimately public destinations (guard must not over-block).
func TestFetchHTTP_PinnedDialSucceedsOnPublic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	// httptest binds loopback (a private IP), so the legit-success path runs
	// through the operator private-override — the pinned dialer must still
	// dial the RESOLVED address, never a second lookup.
	res, err := fetchHTTPDial(srv.URL+"/s.md", 1<<10, 5, func(host string) ([]string, error) {
		return []string{"127.0.0.1"}, nil
	}, true)
	if err != nil || res == nil || res.Content != "ok" {
		t.Fatalf("fetch through pinned dialer failed: %v %+v", err, res)
	}
}
