package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/artifact"
)

// After a response line exceeds max_response_bytes, readLoop exits (the
// package doc promises the line is dropped "and the connection is closed").
// But nothing marks the client dead: the subprocess keeps running, the
// writer keeps writing, and every later call registers a pending waiter that
// no reader will ever route — so each subsequent tool call to that server
// stalls for the full per-server timeout (default 30s, up to 3600s) for the
// rest of a long-lived serve/telegram process instead of failing fast.
func TestRED_MCPCallAfterReadLoopExitFailsFast(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	c := &Client{
		name:             "oversized",
		stdin:            clientWrite,
		stdout:           bufio.NewReader(clientRead),
		lineCh:           make(chan lineResult, 10),
		done:             make(chan struct{}),
		writeCh:          make(chan *queuedRequest, 4),
		replyCh:          make(chan []byte, serverReplyQueue),
		writeDone:        make(chan struct{}),
		closed:           make(chan struct{}),
		pending:          make(map[int]chan callResponse),
		timeout:          3 * time.Second,
		maxResponseBytes: 64 << 10,
	}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
		serverWrite.Close()
		serverRead.Close()
	}()

	// Server: answer the first request with an oversized line, then answer
	// every later request normally (a live, responsive server).
	go func() {
		sc := bufio.NewScanner(serverRead)
		first := true
		for sc.Scan() {
			var req struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			if first {
				first = false
				fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"pad":"%s"}}`+"\n", req.ID, strings.Repeat("x", 128<<10))
				continue
			}
			fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`+"\n", req.ID)
		}
	}()

	if _, err := c.call(context.Background(), "tools/call", json.RawMessage(`{}`)); err == nil {
		t.Fatal("oversized response should fail the first call")
	}

	start := time.Now()
	_, err := c.call(context.Background(), "tools/call", json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("second call succeeded on a client whose reader is gone")
	}
	if elapsed > time.Second {
		t.Fatalf("call on a dead connection hung %v until the per-server timeout (err=%v); it must fail fast", elapsed.Round(time.Millisecond), err)
	}
}

// max_response_bytes is documented (docs/EXTENSIONS.md, ServerConfig) as the
// cap on a single JSON-RPC response line. readLoop installs it with
// scanner.Buffer(make([]byte, 0, 64*1024), max) — but bufio.Scanner's limit
// is the LARGER of max and cap(buf), so any configured cap below 64 KiB is
// silently raised to 64 KiB on the stdio transport (the HTTP transport
// enforces it exactly).
func TestRED_MCPStdioMaxResponseBytesBelow64KiBNotEnforced(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	c := &Client{
		name:             "smallcap",
		stdin:            clientWrite,
		stdout:           bufio.NewReader(clientRead),
		lineCh:           make(chan lineResult, 10),
		done:             make(chan struct{}),
		writeCh:          make(chan *queuedRequest, 4),
		replyCh:          make(chan []byte, serverReplyQueue),
		writeDone:        make(chan struct{}),
		closed:           make(chan struct{}),
		pending:          make(map[int]chan callResponse),
		timeout:          3 * time.Second,
		maxResponseBytes: 1024,
	}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
		serverWrite.Close()
		serverRead.Close()
	}()
	go func() {
		sc := bufio.NewScanner(serverRead)
		for sc.Scan() {
			var req struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			// ~32 KiB line: 32x the configured 1024-byte cap.
			fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"pad":"%s"}}`+"\n", req.ID, strings.Repeat("x", 32<<10))
		}
	}()

	res, err := c.call(context.Background(), "tools/call", json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("a %d-byte response was accepted although max_response_bytes=1024", len(res))
	}
}

// docs/EXTENSIONS.md: for odek.tool-result/v1 envelopes "artifact refs are
// validated first and rendered as metadata lines regardless" of truncation.
// The multi-item path (envelope item + trailing plain-text item) renders the
// capped envelope, appends the trailing text, and then runs applyResultLimit
// over the WHOLE string — which cuts from the end and drops the artifact
// metadata line (and the loop's artifact_count reads 0) whenever the
// envelope sits near the cap and the trailing note is long.
func TestRED_MCPMultiItemTruncationDropsArtifactMetadata(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(file, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	const name = "multi"
	limit := ResultCharsFloor(name) + 200
	envText := strings.Repeat("e", limit-120)
	env := map[string]any{
		"schema": artifact.SchemaToolResult,
		"text":   envText,
		"artifacts": []any{map[string]any{
			"schema": artifact.SchemaArtifactRef, "id": "rep-1",
			"uri": artifact.FileURI(file), "media_type": "text/plain",
		}},
	}
	envJSON, _ := json.Marshal(env)
	result := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": string(envJSON)},
		map[string]any{"type": "text", "text": strings.Repeat("n", 5000)},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "" || req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	c, err := New(name, ServerConfig{URL: srv.URL, MaxResultChars: limit, ArtifactRoots: []string{dir}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	out, err := c.CallTool(context.Background(), "t", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(out, "result truncated") {
		t.Fatalf("expected a truncation notice, got %d runes", len([]rune(out)))
	}
	if artifact.CountRendered(out) != 1 || !strings.Contains(out, `"rep-1"`) {
		t.Fatalf("validated artifact ref was cut from the truncated multi-item result (artifact lines = %d)", artifact.CountRendered(out))
	}
}

// tools/list names are validated one by one but never for uniqueness. Two
// tools with the same name become two adapters named "<server>__<name>",
// and tool.NewRegistry panics on a duplicate name ("tool: duplicate name")
// when odek.New builds the agent — so one MCP server response crashes the
// CLI/REPL/telegram/serve agent construction instead of being rejected.
func TestRED_MCPDiscoverAcceptsDuplicateToolNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []any{
				map[string]any{"name": "fetch", "description": "a", "inputSchema": map[string]any{"type": "object"}},
				map[string]any{"name": "fetch", "description": "b", "inputSchema": map[string]any{"type": "object"}},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	c, err := New("dup", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	defs, err := c.Discover(context.Background())
	if err == nil {
		t.Fatalf("Discover accepted %d tools with a duplicated name; registering them panics tool.NewRegistry", len(defs))
	}
}
