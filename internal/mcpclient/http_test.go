package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
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

		// Notifications carry no id and get no response body.
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

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

	t.Setenv("MCP_TEST_TOKEN", "sekrit")
	c, err := New("remote-auth", ServerConfig{URL: ts.URL, TokenEnv: "MCP_TEST_TOKEN"})
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
	t.Setenv("MCP_TEST_TOKEN", "")
	c2, err := New("remote-noauth", ServerConfig{URL: ts.URL, TokenEnv: "MCP_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := c2.Discover(ctx); err == nil {
		t.Fatal("expected auth failure without token")
	}
}

func TestTokenEnvNamespaceRestricted(t *testing.T) {
	ts := httptest.NewServer((&httpMCPServer{}).handler())
	defer ts.Close()

	// A project config must not be able to name arbitrary secret vars
	// (e.g. ANTHROPIC_API_KEY) and ship them to an attacker host.
	for _, bad := range []string{"ANTHROPIC_API_KEY", "mcp_token", "MCP-BAD", "MCP_" + strings.Repeat("X", 100)} {
		if _, err := New("evil", ServerConfig{URL: ts.URL, TokenEnv: bad}); err == nil {
			t.Errorf("token_env %q: expected rejection", bad)
		}
	}
	// Uppercase MCP_-prefixed names are accepted.
	t.Setenv("MCP_OK_TOKEN", "x")
	if _, err := New("ok", ServerConfig{URL: ts.URL, TokenEnv: "MCP_OK_TOKEN"}); err != nil {
		t.Errorf("valid token_env rejected: %v", err)
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

func TestWithHTTPClientInjectsTransport(t *testing.T) {
	ts := httptest.NewServer((&httpMCPServer{}).handler())
	defer ts.Close()

	injected := &http.Client{}
	c, err := New("remote", ServerConfig{URL: ts.URL}, WithHTTPClient(injected))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.httpc != injected {
		t.Fatal("WithHTTPClient client not used")
	}
	ctx := context.Background()
	if _, err := c.Discover(ctx); err != nil {
		t.Fatalf("discover via injected client: %v", err)
	}
}

func TestSSEDataVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		// Per the SSE spec, one event's data: lines are joined with \n.
		{"multi-line data joined", ": comment\ndata: {\"a\":\ndata: 1}\n\n", "{\"a\":\n1}"},
		{"first event wins", "data: {\"a\":1}\n\ndata: {\"b\":2}\n\n", `{"a":1}`},
		{"no space after colon", "data:{\"a\":2}\n", `{"a":2}`},
		{"empty data line yields empty joined line", "data:\ndata: {\"a\":3}\n", "\n{\"a\":3}"},
		{"CRLF terminated", "data: {\"a\":4}\r\n\r\n", `{"a":4}`},
		{"no data lines yields no frames", "event: x\nid: 1\n", ""},
	}
	for _, tc := range cases {
		frames := sseFrames([]byte(tc.body))
		got := ""
		if len(frames) > 0 {
			got = frames[0]
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestSSEFramesMultiEvent(t *testing.T) {
	body := ": ping\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"tools\":[]}}\n\n"
	frames := sseFrames([]byte(body))
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d: %q", len(frames), frames)
	}
	if frames[0] != `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` {
		t.Errorf("frame 0: %q", frames[0])
	}
	if frames[1] != `{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}` {
		t.Errorf("frame 1: %q", frames[1])
	}
}

func TestURLTransportResponseIDVerified(t *testing.T) {
	// Server replies with a wrong id: must be an error, not a silent empty result.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":999,"result":{"tools":[]}}`))
	}))
	defer ts.Close()
	c, err := New("wrong-id", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(context.Background(), "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "does not match request id") {
		t.Fatalf("expected id mismatch error, got %v", err)
	}
}

func TestURLTransportSSENoResponseFrame(t *testing.T) {
	// Stream with only notification frames: must error, not return empty.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/x\"}\n\n")
	}))
	defer ts.Close()
	c, err := New("sse-notify-only", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(context.Background(), "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "no JSON-RPC response frame") {
		t.Fatalf("expected no-response-frame error, got %v", err)
	}
}

func TestURLTransportSSEGarbageFrameThenResponse(t *testing.T) {
	// A malformed frame is skipped (retained as parse error) and the valid
	// frame still wins; if nothing valid exists the parse error surfaces.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: not-json\n\n")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", req.ID)
	}))
	defer ts.Close()
	c, err := New("sse-garbage", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.call(context.Background(), "tools/list", nil); err != nil {
		t.Fatalf("valid frame should win after garbage: %v", err)
	}
}

func TestURLTransportSSEOnlyGarbage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: not-json\n\n")
	}))
	defer ts.Close()
	c, err := New("sse-bad", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(context.Background(), "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestURLTransportSSEErrorFrameMatchesID(t *testing.T) {
	// JSON-RPC error with the right id surfaces over SSE; wrong id errors first.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"error\":{\"code\":-1,\"message\":\"boom\"}}\n\n", req.ID)
	}))
	defer ts.Close()
	c, err := New("sse-err", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(context.Background(), "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "MCP error -1") {
		t.Fatalf("expected MCP error via SSE, got %v", err)
	}
}

func TestHTTPNotifyErrorStatus(t *testing.T) {
	// Discover treats a rejected notification as best-effort (ignored), so
	// drive httpNotify directly against a 500 endpoint.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	c, err := New("notify-500", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.httpNotify(context.Background(), "notifications/initialized"); err == nil {
		t.Fatal("expected notify error on http 500")
	}
}

func TestURLTransportSSEUnrelatedResponseBeforeOurs(t *testing.T) {
	// A streaming server interleaves an unrelated response before ours;
	// the scan must keep looking for the matching id.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"other\":true}}\n\n", req.ID+100)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"tools\":[{\"name\":\"echo\",\"description\":\"d\"}]}}\n\n", req.ID)
	}))
	defer ts.Close()
	c, err := New("sse-interleaved", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defs, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", defs)
	}
}

func TestURLTransportJSONErrorFrameWrongIDRejected(t *testing.T) {
	// Plain-JSON path: an error object with a mismatched id must not be
	// accepted as this call's answer.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":42,"error":{"code":-32000,"message":"spoofed"}}`))
	}))
	defer ts.Close()
	c, err := New("json-spoof", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(context.Background(), "tools/list", nil)
	if err == nil {
		t.Fatal("expected id mismatch error")
	}
	if strings.Contains(err.Error(), "spoofed") {
		t.Fatalf("spoofed error text leaked through: %v", err)
	}
	if !strings.Contains(err.Error(), "does not match request id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestURLTransportSSENotificationBeforeResponse(t *testing.T) {
	// SSE stream opens with a server notification (no id), then the actual
	// response — the parser must skip the notification frame.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reqID := req.ID
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"t","version":"1"}}`
		case "tools/list":
			result = `{"tools":[{"name":"echo","description":"d"}]}`
		default:
			result = `{}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/roots/list_changed\"}\n\n")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":%s}\n\n", reqID, result)
	}))
	defer ts.Close()

	c, err := New("sse-multi", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defs, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", defs)
	}
}

func TestURLTransportSendsInitializedNotification(t *testing.T) {
	srv := &httpMCPServer{}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c, err := New("init-note", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	found := false
	for _, m := range srv.requests {
		if m == "notifications/initialized" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notifications/initialized not sent; requests: %v", srv.requests)
	}
}

func TestTokenEnvAllowedVariants(t *testing.T) {
	for _, ok := range []string{"MCP_A", "MCP_1", "MCP_A_B"} {
		if !tokenEnvAllowed(ok) {
			t.Errorf("tokenEnvAllowed(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "MCP", "mcp_a", "MCP_a", "MCP-A", "MCP_A$", strings.Repeat("MCP_", 20)} {
		if tokenEnvAllowed(bad) {
			t.Errorf("tokenEnvAllowed(%q) = true, want false", bad)
		}
	}
}

func TestValidateServerURLErrorBranches(t *testing.T) {
	if _, err := validateServerURL("http://[::1"); err == nil {
		t.Error("expected parse error for malformed URL")
	}
}

func TestHTTPCallErrorBranches(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "tools/list":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(strings.Repeat("boom ", 300))) // body truncated in error
			return
		case "tools/call":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"tool exploded"}}`, req.ID)
			return
		default:
			w.Write([]byte(`not json`))
		}
	}))
	defer ts.Close()

	c, err := New("errs", ServerConfig{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// Non-200: error includes status and a truncated body.
	_, err = c.call(ctx, "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("expected http 500 error, got %v", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("error body not truncated: %d chars", len(err.Error()))
	}

	// JSON-RPC error object surfaces as-is.
	_, err = c.call(ctx, "tools/call", nil)
	if err == nil || !strings.Contains(err.Error(), "MCP error -32000") {
		t.Fatalf("expected MCP error, got %v", err)
	}

	// Malformed JSON body fails as a parse error.
	_, err = c.call(ctx, "initialize", nil)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestHTTPCallContextTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	c, err := New("slow", ServerConfig{URL: ts.URL, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if _, err := c.call(context.Background(), "tools/list", nil); err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 1900*time.Millisecond {
		t.Fatal("per-server timeout not enforced")
	}
}

func TestHTTPClientWarnsOnMissingTokenEnv(t *testing.T) {
	t.Setenv("MCP_MISSING_TOKEN", "")
	c, err := New("warn", ServerConfig{URL: "https://mcp.example.com", TokenEnv: "MCP_MISSING_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	found := false
	for _, w := range c.Warnings() {
		if strings.Contains(w, "MCP_MISSING_TOKEN") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing-token warning, got %v", c.Warnings())
	}
}

func TestURLClientWarningsAndArtifactRoots(t *testing.T) {
	c, err := New("warn", ServerConfig{URL: "https://mcp.example.com", TimeoutSeconds: 99999, ArtifactRoots: []string{"/tmp/roots"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Warnings()) == 0 {
		t.Fatal("expected clamp warning for oversized timeout_seconds")
	}
	if got := c.ArtifactRoots(); len(got) != 1 || got[0] != "/tmp/roots" {
		t.Fatalf("unexpected artifact roots: %v", got)
	}
}
