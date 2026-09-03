package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// A spec-legal server→client REQUEST (JSON-RPC 2.0: carries "method") whose
// id collides with an in-flight call was routed to that call's waiter as
// {result: null} — the real response was then dropped and the call failed
// with a parse error. Responses to OUR calls never carry "method".
func TestClient_Call_IgnoresServerToClientRequests(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	go io.Copy(io.Discard, serverRead) // drain client→server writes

	c := &Client{
		name:      "confused",
		stdin:     clientWrite,
		stdout:    bufio.NewReader(clientRead),
		lineCh:    make(chan lineResult, 10),
		done:      make(chan struct{}),
		writeCh:   make(chan []byte, 2),
		writeDone: make(chan struct{}),
		closed:    make(chan struct{}),
		pending:   make(map[int]chan callResponse),
		timeout:   5 * time.Second,
	}
	go c.readLoop()
	go c.writeLoop()
	cleanup := func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
	}
	defer cleanup()

	go func() {
		// 1) Spec-legal server→client request whose id collides with the call
		// (nextID starts at 0, so the first call is id 0).
		fmt.Fprint(serverWrite, `{"jsonrpc":"2.0","id":0,"method":"ping","params":{}}`+"\n")
		time.Sleep(100 * time.Millisecond) // deterministic ordering
		// 2) The real response to the client's call.
		fmt.Fprint(serverWrite, `{"jsonrpc":"2.0","id":0,"result":{"ok":true}}`+"\n")
	}()

	res, err := c.call(context.Background(), "tools/call", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call failed after colliding server→client request: %v", err)
	}
	if res == nil || !strings.Contains(string(res), `"ok":true`) {
		t.Fatalf("result = %s, want the real response payload", res)
	}
}
