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

// A server returning [envelope item, trailing note] produced a JOINED text
// "envelope\nnote": the envelope probe fails on the trailing data, the
// envelope is treated as plain text, and the RAW envelope JSON — including
// artifact refs that must never reach the model unvalidated — is delivered
// verbatim, bypassing artifact-ref validation entirely.
func TestClient_CallTool_EnvelopeWithTrailingContent(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	c := &Client{
		name:      "multiitem",
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
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		clientWrite.Close()
		clientRead.Close()
		serverWrite.Close()
		serverRead.Close()
	}()

	// Reply only after the client's request is on the wire. Writing the
	// response immediately races readLoop: if the line arrives before
	// call() registers pending[id], it is dropped and CallTool times out
	// (seen under -race in CI).
	go func() {
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"content":[`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"report ready\"}"},`+
			`{"type":"text","text":"(generated 2 artifacts)"}`+
			`]}}`+"\n", id)
	}()

	out, err := c.CallTool(context.Background(), "build_report", `{}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if strings.Contains(out, `"schema":"odek.tool-result/v1"`) {
		t.Fatalf("raw envelope JSON delivered to the model (artifact-ref validation bypassed): %q", out)
	}
	if !strings.Contains(out, "report ready") {
		t.Fatalf("rendered envelope text missing from result: %q", out)
	}
	if !strings.Contains(out, "(generated 2 artifacts)") {
		t.Fatalf("trailing content note lost: %q", out)
	}
}

// readJSONRPCID consumes one JSON-RPC request line from r and returns its id.
func readJSONRPCID(r io.Reader) (int, bool) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return 0, false
	}
	var req struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		return 0, false
	}
	return req.ID, true
}
