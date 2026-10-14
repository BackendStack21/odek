package mcpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// validateName/validateToolName forbid "__" inside a name but allow a
// single leading or trailing underscore, so the "<server>__<tool>" join is
// ambiguous: server "gh_" + tool "issues" and server "gh" + tool "_issues"
// both register "gh___issues". Two configured servers then collide — the
// unfiltered tool list panics tool.NewRegistry ("duplicate name"), and an
// allow-listed one silently resolves the name to whichever server loaded
// last (a project server shadowing the operator's tool).
func TestRED_MCPRegisteredToolNameAmbiguousAcrossServers(t *testing.T) {
	for _, n := range []string{"gh_", "gh"} {
		if err := validateName("server", n); err != nil {
			t.Fatalf("precondition: server name %q rejected: %v", n, err)
		}
	}
	for _, n := range []string{"issues", "_issues"} {
		if err := validateToolName(n); err != nil {
			t.Fatalf("precondition: tool name %q rejected: %v", n, err)
		}
	}
	a := RegisteredToolName("gh_", "issues")
	b := RegisteredToolName("gh", "_issues")
	if a == b {
		t.Errorf("distinct (server, tool) pairs (gh_, issues) and (gh, _issues) register the same model-facing name %q", a)
	}
}

// An HTTPS MCP server (or anything in front of it) that answers with a
// 307/308 redirect to a plain-http URL on the same host name makes the
// client re-POST the request — Bearer token included — in cleartext.
// withSessionRedirectGuard strips the Mcp-Session-Id header when the scheme
// or host changes, but the Authorization header (token_env secret) is left
// to net/http, which only compares host names and forwards it across an
// https -> http downgrade and to a different port.
func TestRED_MCPHTTPBearerTokenSentOverDowngradedRedirect(t *testing.T) {
	var mu sync.Mutex
	var leaked string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`))
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	t.Setenv("MCP_REDHUNT_TOKEN", "s3cr3t-bearer-token")
	c, err := New("remote", ServerConfig{URL: secure.URL + "/mcp", TokenEnv: "MCP_REDHUNT_TOKEN"}, WithHTTPClient(secure.Client()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.call(context.Background(), "initialize", []byte(`{}`))

	mu.Lock()
	defer mu.Unlock()
	if leaked != "" {
		t.Errorf("bearer token forwarded in cleartext to the downgraded redirect target %s: Authorization=%q", plain.URL, leaked)
	}
}
