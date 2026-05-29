package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	golangws "golang.org/x/net/websocket"
)

// All three subtests use the production checkLocalOrigin helper so the
// real fix is exercised, not a copy. See IMPROVEMENTS_ROADMAP.md S-M1.
func newOriginTestServer() *httptest.Server {
	srv := &golangws.Server{
		Handshake: checkLocalOrigin,
		Handler:   func(c *golangws.Conn) { c.Close() },
	}
	return httptest.NewServer(srv)
}

// TestServeWS_RejectsForeignOrigin verifies the fix for S-M1: a page
// hosted on a non-local origin must NOT be able to upgrade to the
// WebSocket and drive the agent.
func TestServeWS_RejectsForeignOrigin(t *testing.T) {
	ts := newOriginTestServer()
	defer ts.Close()

	wsURL := "ws://" + strings.TrimPrefix(ts.URL, "http://") + "/"

	cfg, err := golangws.NewConfig(wsURL, "http://attacker.example.com")
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.Origin, _ = url.Parse("http://attacker.example.com")

	conn, err := golangws.DialConfig(cfg)
	if err == nil {
		conn.Close()
		t.Fatalf("WebSocket upgrade accepted foreign Origin — Origin allowlist regression")
	}
}

// TestServeWS_AcceptsLocalhostOrigin guards against an overly-strict fix
// that would break the bundled Web UI.
func TestServeWS_AcceptsLocalhostOrigin(t *testing.T) {
	ts := newOriginTestServer()
	defer ts.Close()

	hostPort := strings.TrimPrefix(ts.URL, "http://")
	wsURL := "ws://" + hostPort + "/"
	cfg, err := golangws.NewConfig(wsURL, ts.URL)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.Origin, _ = url.Parse(ts.URL)

	conn, err := golangws.DialConfig(cfg)
	if err != nil {
		t.Fatalf("localhost Origin should be accepted, got: %v", err)
	}
	conn.Close()
}

// TestCheckLocalOrigin_DirectMatrix exercises the policy function directly
// so we can cover cases the WebSocket dialer cannot reach (notably an
// absent Origin header, which non-browser clients like curl omit and which
// must remain accepted).
func TestCheckLocalOrigin_DirectMatrix(t *testing.T) {
	cases := []struct {
		name    string
		origin  string
		wantErr bool
	}{
		{"empty", "", false},
		{"localhost", "http://localhost:8080", false},
		{"127.0.0.1", "http://127.0.0.1:9999", false},
		{"ipv6_loopback", "http://[::1]:8080", false},
		{"https_localhost", "https://localhost", false},
		{"foreign_domain", "http://attacker.example.com", true},
		{"public_ip", "http://203.0.113.5", true},
		{"unparseable", "::not a url::", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			err := checkLocalOrigin(nil, req)
			if tc.wantErr && err == nil {
				t.Errorf("checkLocalOrigin(%q) = nil, want error", tc.origin)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkLocalOrigin(%q) = %v, want nil", tc.origin, err)
			}
		})
	}
}
