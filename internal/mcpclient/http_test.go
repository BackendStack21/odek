package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateServerURL(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"https://mcp.example.com/rpc", true},
		{"http://localhost:8080/mcp", true},
		{"ftp://example.com", false},
		{"example.com/rpc", false},             // no scheme
		{"https://user:pw@example.com", false}, // embedded credentials
		{"https://", false},
		{"", false},
	} {
		_, err := validateServerURL(tc.raw)
		if tc.ok && err != nil {
			t.Errorf("validateServerURL(%q) unexpected error: %v", tc.raw, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("validateServerURL(%q) expected error, got nil", tc.raw)
		}
	}
}

func TestNewURLServerRequiresURLOrCommand(t *testing.T) {
	if _, err := New("bad", ServerConfig{}); err == nil {
		t.Fatal("expected error for config with neither url nor command")
	} else if !strings.Contains(err.Error(), "either url or command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewURLServerRejectsBadURL(t *testing.T) {
	if _, err := New("bad", ServerConfig{URL: "ftp://x"}); err == nil {
		t.Fatal("expected error for non-http url")
	}
}

// httpMCPServer is a minimal MCP server over Streamable HTTP used by the
// transport tests. Responses may be plain JSON or SSE, switchable per test.
type httpMCPServer struct {
	mu       sync.Mutex
	token    string
	sse      bool
	requests []string
	tools    []ToolDef
}

func (s *httpMCPServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+s.token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, req.Method)
		sse := s.sse
		s.mu.Unlock()

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]string{"name": "test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": s.tools}
		case "tools/call":
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "hello from http"}}}
		default:
			result = map[string]any{}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(": ping\n\ndata: " + string(body) + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

func TestURLTransportDiscoverAndCall(t *testing.T) {
	srv := &httpMCPServer{tools: []ToolDef{{Name: "echo", Description: "echo tool"}}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c, err := New("remote", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	defs, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", defs)
	}

	out, err := c.CallTool(ctx, "echo", `{"x":1}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hello from http" {
		t.Fatalf("unexpected result: %q", out)
	}
}

func TestURLTransportSSEResponses(t *testing.T) {
	srv := &httpMCPServer{sse: true}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c, err := New("remote-sse", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Discover(ctx); err != nil {
		t.Fatalf("discover over SSE: %v", err)
	}
}

func TestURLTransportBearerToken(t *testing.T) {
	srv := &httpMCPServer{token: "sekrit"}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	t.Setenv("ODEK_TEST_MCP_TOKEN", "sekrit")
	c, err := New("remote-auth", ServerConfig{URL: ts.URL, TokenEnv: "ODEK_TEST_MCP_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Discover(ctx); err != nil {
		t.Fatalf("discover with token: %v", err)
	}

	// Without the env var set, the server rejects with 401.
	t.Setenv("ODEK_TEST_MCP_TOKEN", "")
	c2, err := New("remote-noauth", ServerConfig{URL: ts.URL, TokenEnv: "ODEK_TEST_MCP_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := c2.Discover(ctx); err == nil {
		t.Fatal("expected auth failure without token")
	}
}

func TestURLTransportResponseSizeCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"padding":"` + strings.Repeat("x", 5000) + `"}}`))
	}))
	defer ts.Close()

	c, err := New("remote-huge", ServerConfig{URL: ts.URL, MaxResponseBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Discover(ctx); err == nil {
		t.Fatal("expected max_response_bytes rejection")
	}
}

func TestURLTransportCloseIdempotent(t *testing.T) {
	ts := httptest.NewServer((&httpMCPServer{}).handler())
	defer ts.Close()
	c, err := New("remote", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
