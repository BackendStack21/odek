package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Streamable HTTP: when the server returns an Mcp-Session-Id header on the
// initialize response, the client must send it on every subsequent request
// (notifications included); stateful servers answer 400 otherwise.
func TestRED_HTTPTransportEchoesMcpSessionID(t *testing.T) {
	const sid = "sess-7f3a"
	var mu sync.Mutex
	var sawInitializedWithSID bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method == "initialize" && r.Header.Get("Mcp-Session-Id") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != sid {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"Bad Request: No valid session ID provided"}}`)
			return
		}
		if req.ID == nil { // notification
			if req.Method == "notifications/initialized" {
				mu.Lock()
				sawInitializedWithSID = true
				mu.Unlock()
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", sid)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`, *req.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"Echo","inputSchema":{"type":"object"}}]}}`, *req.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, *req.ID)
		}
	}))
	defer srv.Close()

	c, err := New("stateful", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover against a stateful Streamable HTTP server failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	mu.Lock()
	ok := sawInitializedWithSID
	mu.Unlock()
	if !ok {
		t.Error("notifications/initialized was not sent with the session id")
	}
	out, err := c.CallTool(ctx, "echo", `{}`)
	if err != nil || out != "ok" {
		t.Fatalf("CallTool = %q, %v", out, err)
	}
}

// A session id that is not visible ASCII (header-injection shaped or empty)
// is never stored or echoed.
func TestRED_HTTPTransportIgnoresInvalidMcpSessionID(t *testing.T) {
	for _, bad := range []string{"has space", "tab\there", "été"} {
		if validMCPSessionID(bad) {
			t.Errorf("validMCPSessionID(%q) = true", bad)
		}
	}
	if validMCPSessionID("") {
		t.Error("empty session id accepted")
	}
	if !validMCPSessionID("abc-123_~!") {
		t.Error("visible-ASCII session id rejected")
	}
}

// redHuntConfigPipeClient builds a stdio Client wired to in-memory pipes and
// returns the server's read/write ends.
func redHuntConfigPipeClient(t *testing.T, name string, timeout time.Duration) (*Client, *bufio.Scanner, io.Writer) {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	c := &Client{
		name: name, stdin: clientWrite, stdout: bufio.NewReader(clientRead),
		lineCh: make(chan lineResult, 10), done: make(chan struct{}),
		writeCh: make(chan *queuedRequest, 4), writeDone: make(chan struct{}),
		closed: make(chan struct{}), pending: make(map[int]chan callResponse),
		timeout: timeout,
	}
	go c.readLoop()
	go c.writeLoop()
	t.Cleanup(func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
		serverWrite.Close()
		serverRead.Close()
	})
	return c, bufio.NewScanner(serverRead), serverWrite
}

// MCP lifecycle: after the initialize response the client must send the
// notifications/initialized notification before any other request — on the
// stdio transport too.
func TestRED_StdioDiscoverSendsInitializedNotification(t *testing.T) {
	c, sc, serverWrite := redHuntConfigPipeClient(t, "strict", 3*time.Second)
	go func() {
		initialized := false
		for sc.Scan() {
			var req struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			switch {
			case req.Method == "notifications/initialized" && req.ID == nil:
				initialized = true
			case req.ID == nil:
			case req.Method == "initialize":
				fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`+"\n", *req.ID)
			case !initialized:
				fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"Received request before initialization was complete"}}`+"\n", *req.ID)
			default:
				fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","description":"Echo"}]}}`+"\n", *req.ID)
			}
		}
	}()

	tools, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover against a lifecycle-strict stdio server: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
}

// MCP basic utilities: the receiver of a "ping" must respond promptly with an
// empty result. A server that pings before finishing a tools/call must not
// stall the call; any other server request gets method-not-found, and
// notifications get no reply.
func TestRED_StdioClientAnswersServerPing(t *testing.T) {
	c, sc, serverWrite := redHuntConfigPipeClient(t, "pinger", 2*time.Second)
	replies := make(chan string, 8)
	go func() {
		callID := -1
		for sc.Scan() {
			line := sc.Text()
			var msg struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
			}
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			switch {
			case msg.Method == "tools/call" && msg.ID != nil:
				_ = json.Unmarshal(*msg.ID, &callID)
				// A notification (no reply expected), an unsupported request,
				// then a liveness check before doing the work.
				fmt.Fprint(serverWrite, `{"jsonrpc":"2.0","method":"notifications/message","params":{}}`+"\n")
				fmt.Fprint(serverWrite, `{"jsonrpc":"2.0","id":7,"method":"sampling/createMessage","params":{}}`+"\n")
				fmt.Fprint(serverWrite, `{"jsonrpc":"2.0","id":"ping1","method":"ping"}`+"\n")
			case msg.Method == "" && msg.ID != nil:
				replies <- line
				if string(*msg.ID) == `"ping1"` {
					fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"done"}]}}`+"\n", callID)
				}
			}
		}
	}()

	out, err := c.CallTool(context.Background(), "work", `{}`)
	if err != nil {
		t.Fatalf("CallTool against a server that pings first: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	var got []string
	for len(got) < 2 {
		select {
		case r := <-replies:
			got = append(got, r)
		case <-time.After(2 * time.Second):
			t.Fatalf("replies = %v, want two", got)
		}
	}
	var mnf, pong map[string]any
	_ = json.Unmarshal([]byte(got[0]), &mnf)
	_ = json.Unmarshal([]byte(got[1]), &pong)
	if errObj, _ := mnf["error"].(map[string]any); mnf["id"] != float64(7) || errObj == nil || errObj["code"] != float64(-32601) {
		t.Errorf("reply to an unsupported server request = %s, want method-not-found for id 7", got[0])
	}
	if res, ok := pong["result"].(map[string]any); pong["id"] != "ping1" || !ok || len(res) != 0 {
		t.Errorf("reply to ping = %s, want an empty result for id \"ping1\"", got[1])
	}
	select {
	case r := <-replies:
		t.Errorf("unexpected extra reply %s (notifications must not be answered)", r)
	case <-time.After(100 * time.Millisecond):
	}
}

// A server request whose id is neither a string nor a number is malformed and
// gets no reply.
func TestRED_ServerRequestReplyRejectsNonScalarID(t *testing.T) {
	for _, id := range []string{`{"a":1}`, `[1]`, `true`, `null`, ``} {
		if b := serverRequestReply(json.RawMessage(id), "ping"); b != nil {
			t.Errorf("id %s: reply %s, want none", id, b)
		}
	}
	if b := serverRequestReply(json.RawMessage(`12`), "ping"); string(b) != `{"jsonrpc":"2.0","id":12,"result":{}}`+"\n" {
		t.Errorf("ping reply = %q", b)
	}
	// Not valid JSON (cannot come from a decoded frame): no reply.
	if b := serverRequestReply(json.RawMessage(`"unterminated`), "ping"); b != nil {
		t.Errorf("malformed id: reply %s, want none", b)
	}
}

// notify on stdio: a nil ctx and an unset timeout use the defaults; a closed
// connection or a full writer queue past the deadline fail instead of
// blocking.
func TestRED_StdioNotifyBoundedAndClosed(t *testing.T) {
	c := &Client{writeCh: make(chan *queuedRequest, 1), closed: make(chan struct{})}
	//nolint:staticcheck // a nil ctx is the case under test
	if err := c.notify(nil, "notifications/initialized"); err != nil {
		t.Fatalf("notify into an empty queue: %v", err)
	}
	if q := <-c.writeCh; !bytes.HasSuffix(q.data, []byte("\n")) || !bytes.Contains(q.data, []byte(`"method":"notifications/initialized"`)) || bytes.Contains(q.data, []byte(`"id"`)) {
		t.Fatalf("queued notification = %s", q.data)
	}

	c.writeCh <- &queuedRequest{ctx: context.Background()} // queue full
	c.timeout = 50 * time.Millisecond
	if err := c.notify(context.Background(), "x"); err == nil {
		t.Fatal("notify into a full queue past the deadline returned nil")
	}
	close(c.closed)
	if err := c.notify(context.Background(), "x"); err == nil {
		t.Fatal("notify on a closed connection returned nil")
	}
	// A reply to a server request is dropped, never blocking, when the
	// queue is full or the connection is closed.
	done := make(chan struct{})
	go func() { c.sendServerReply([]byte("{}\n")); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sendServerReply blocked on a full queue")
	}
}

// An initialize response without a valid session id leaves the client
// stateless, and clears an id from an earlier initialize.
func TestRED_CaptureSessionIDRejectsInvalid(t *testing.T) {
	c := &Client{sessionID: "old"}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Mcp-Session-Id", "bad id")
	c.captureSessionID(resp)
	if c.sessionID != "" {
		t.Errorf("sessionID = %q after an invalid header, want empty", c.sessionID)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	c.setHTTPHeaders(req)
	if req.Header.Get("Mcp-Session-Id") != "" {
		t.Error("session header sent without a stored id")
	}
}

// Small accessors used by the registry and the loop.
func TestRED_ConfigAccessorsCovered(t *testing.T) {
	off := false
	if !(ServerConfig{Enabled: &off}).IsDisabled() || (ServerConfig{}).IsDisabled() {
		t.Error("IsDisabled")
	}
	a := &ToolAdapter{}
	if s, ok := a.Schema().(map[string]any); !ok || s["type"] != "object" {
		t.Errorf("default schema = %v", a.Schema())
	}
	c := &Client{lineCh: make(chan lineResult, 1)}
	c.lineCh <- lineResult{line: "l"}
	if l, err := c.readLine(context.Background()); l != "l" || err != nil {
		t.Errorf("readLine = %q, %v", l, err)
	}
	close(c.lineCh)
	if _, err := c.readLine(context.Background()); err != io.EOF {
		t.Errorf("readLine after close = %v, want EOF", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Client{lineCh: make(chan lineResult)}).readLine(ctx); err == nil {
		t.Error("readLine with a cancelled ctx returned nil error")
	}
}
