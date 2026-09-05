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
		serverWrite.Close()
		serverRead.Close()
	}
	defer cleanup()

	go func() {
		// Wait until the call is in-flight (pending registered before write)
		// so the colliding server→client request actually hits a waiter —
		// the bug this test pins. An immediate write races readLoop under
		// -race and can drop the real response before pending[id] exists.
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"method":"ping","params":{}}`+"\n", id)
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`+"\n", id)
	}()

	res, err := c.call(context.Background(), "tools/call", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call failed after colliding server→client request: %v", err)
	}
	if res == nil || !strings.Contains(string(res), `"ok":true`) {
		t.Fatalf("result = %s, want the real response payload", res)
	}
}
