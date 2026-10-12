package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// runtimeBListServer is a Streamable HTTP MCP server whose tools/list answer
// is produced by list (given the request params).
func runtimeBListServer(t *testing.T, list func(params json.RawMessage) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}}
		if req.Method == "tools/list" {
			result = list(req.Params)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// MCP tools/list is a paginated method: a server returns nextCursor and the
// client must request the next page with params.cursor. Discover issues a
// single tools/list and ignores nextCursor, so every tool past the first
// page is silently missing from the catalogue.
func TestRED_MCPDiscoverIgnoresToolsListPagination(t *testing.T) {
	srv := runtimeBListServer(t, func(params json.RawMessage) any {
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Cursor == "page2" {
			return map[string]any{"tools": []any{
				map[string]any{"name": "second", "description": "b", "inputSchema": map[string]any{"type": "object"}},
			}}
		}
		return map[string]any{
			"tools": []any{
				map[string]any{"name": "first", "description": "a", "inputSchema": map[string]any{"type": "object"}},
			},
			"nextCursor": "page2",
		}
	})
	c, err := New("paged", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	defs, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if len(defs) != 2 {
		t.Fatalf("Discover returned %v; the second tools/list page (nextCursor) was never fetched", names)
	}
}

// Every registered MCP tool name is "<server>__<tool>". Server and tool are
// each allowed 64 characters, so the registered name can reach 130 — but
// OpenAI-format providers (the default DeepSeek provider included) and
// Anthropic reject a function/tool name over 64 characters with HTTP 400,
// failing EVERY LLM call of the run. A single long tool name from one MCP
// server makes the whole agent unusable instead of being rejected (or
// skipped) at discovery.
func TestRED_MCPRegisteredToolNameExceedsProviderLimit(t *testing.T) {
	long := "list_pull_request_review_comments_for_repository_v2" // 51 chars, valid
	srv := runtimeBListServer(t, func(json.RawMessage) any {
		return map[string]any{"tools": []any{
			map[string]any{"name": long, "description": "a", "inputSchema": map[string]any{"type": "object"}},
		}}
	})
	c, err := New("github-enterprise", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	defs, err := c.Discover(context.Background())
	if err != nil {
		return // rejected at discovery: correct
	}
	for _, d := range defs {
		a := &ToolAdapter{Client: c, ToolName: d.Name}
		if n := len(a.Name()); n > 64 {
			t.Fatalf("registered tool name %q is %d chars; providers reject tool names over 64 chars, failing every LLM call", a.Name(), n)
		}
	}
}

// closeHTTP documents that the closed channel exists "so concurrent callers
// observe the shutdown" — but httpCall never selects on c.closed nor ties
// its request to it. Close on an HTTP MCP client therefore neither aborts
// an in-flight tools/call nor refuses later ones: a call to a slow/hung
// server keeps running for the full per-server timeout (up to 3600s) after
// the owning agent/session shut the client down. The stdio transport
// unblocks the same waiter immediately on Close.
func TestRED_MCPHTTPCloseDoesNotAbortInFlightCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c, err := New("slow", ServerConfig{URL: srv.URL, TimeoutSeconds: 5})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), "x", `{}`)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the request reach the server
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("in-flight call succeeded after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not abort the in-flight HTTP MCP call; it keeps running until the per-server timeout")
	}
	// A call issued after Close must fail fast instead of dialing the server.
	start := time.Now()
	if _, err := c.CallTool(context.Background(), "x", `{}`); err == nil || time.Since(start) > time.Second {
		t.Fatalf("call after Close: err=%v after %v; want a prompt 'closed' error", err, time.Since(start).Round(time.Millisecond))
	}
}

// Shortened registered names stay within the provider limit, are
// deterministic and distinct for distinct tools; short names are unchanged.
func TestRED_MCPRegisteredToolNameShortening(t *testing.T) {
	if got := RegisteredToolName("srv", "tool"); got != "srv__tool" {
		t.Fatalf("short name changed: %q", got)
	}
	a := RegisteredToolName("github-enterprise", "list_pull_request_review_comments_for_repository_v2")
	b := RegisteredToolName("github-enterprise", "list_pull_request_review_comments_for_repository_v3")
	if len(a) > 64 || len(b) > 64 {
		t.Fatalf("names too long: %d %d", len(a), len(b))
	}
	if a == b {
		t.Fatalf("distinct tools collapsed to %q", a)
	}
	if a != RegisteredToolName("github-enterprise", "list_pull_request_review_comments_for_repository_v2") {
		t.Fatal("shortening is not deterministic")
	}
}

// A server that returns endless pages is bounded.
func TestRED_MCPDiscoverPaginationBounded(t *testing.T) {
	page := 0
	srv := runtimeBListServer(t, func(json.RawMessage) any {
		page++
		return map[string]any{
			"tools":      []any{map[string]any{"name": fmt.Sprintf("t%d", page), "inputSchema": map[string]any{"type": "object"}}},
			"nextCursor": fmt.Sprintf("c%d", page),
		}
	})
	c, err := New("endless", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Discover(context.Background())
	if page > maxToolsListPages {
		t.Fatalf("fetched %d pages, cap is %d", page, maxToolsListPages)
	}
}

// Non-200 bodies reaching the error text are flattened and capped.
func TestRED_MCPHTTPErrorBodySanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("line1\nline2\x1b[31m\n" + strings.Repeat("é", 2000)))
	}))
	defer srv.Close()
	c, err := New("err", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.CallTool(context.Background(), "x", `{}`)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\n\x1b") || !utf8.ValidString(msg) || len([]rune(msg)) > 700 {
		t.Fatalf("unsanitized error: %q", msg)
	}
}

// Every shortened name keeps the "<server>__<tool>" shape that the loop's
// approval classification and episode taint key on, even for long server names.
func TestRED_MCPRegisteredToolNameKeepsSeparator(t *testing.T) {
	long := strings.Repeat("s", 64)
	for _, c := range [][2]string{
		{long, strings.Repeat("t", 64)},
		{long, "x"},
		{"srv", strings.Repeat("t", 64)},
		{strings.Repeat("s", 55), strings.Repeat("t", 40)},
		{"enterprise-server", "list_pull_request_review_comments_for_repository_v2"},
	} {
		n := RegisteredToolName(c[0], c[1])
		if len(n) > 64 || !strings.Contains(n, "__") {
			t.Errorf("RegisteredToolName(%d,%d) = %q (len %d): want <=64 chars containing __", len(c[0]), len(c[1]), n, len(n))
		}
	}
}

// A cursor cycle (A, B, A, ...) stops at the first repeat.
func TestRED_MCPDiscoverCursorCycleStops(t *testing.T) {
	calls := 0
	srv := runtimeBListServer(t, func(params json.RawMessage) any {
		calls++
		next := "A"
		if calls%2 == 0 {
			next = "B"
		}
		return map[string]any{
			"tools":      []any{map[string]any{"name": fmt.Sprintf("t%d", calls), "inputSchema": map[string]any{"type": "object"}}},
			"nextCursor": next,
		}
	})
	c, err := New("cyc", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Discover(context.Background())
	if calls > 4 {
		t.Fatalf("cursor cycle fetched %d pages", calls)
	}
}
