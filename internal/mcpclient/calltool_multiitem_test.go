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
		writeCh:   make(chan *queuedRequest, 2),
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

func TestClient_CallTool_MalformedClaimAlongsideEnvelopeFailsClosed(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	c := &Client{name: "multiitem", stdin: clientWrite, stdout: bufio.NewReader(clientRead), lineCh: make(chan lineResult, 10), done: make(chan struct{}), writeCh: make(chan *queuedRequest, 2), writeDone: make(chan struct{}), closed: make(chan struct{}), pending: make(map[int]chan callResponse), timeout: 5 * time.Second}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		_ = clientWrite.Close()
		_ = clientRead.Close()
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go func() {
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"content":[`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"ok\"}"},`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"artifacts\":\"malformed\"}"}`+
			`]}}`+"\n", id)
	}()
	if _, err := c.CallTool(context.Background(), "build_report", `{}`); err == nil {
		t.Fatal("accepted malformed claimed envelope alongside valid envelope")
	}
}

func TestClient_CallTool_TwoValidEnvelopesAreCombined(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	c := &Client{name: "multiitem", stdin: clientWrite, stdout: bufio.NewReader(clientRead), lineCh: make(chan lineResult, 10), done: make(chan struct{}), writeCh: make(chan *queuedRequest, 2), writeDone: make(chan struct{}), closed: make(chan struct{}), pending: make(map[int]chan callResponse), timeout: 5 * time.Second}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		_ = clientWrite.Close()
		_ = clientRead.Close()
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go func() {
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"content":[`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"first\"}"},`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"second\"}"}`+
			`]}}`+"\n", id)
	}()
	out, err := c.CallTool(context.Background(), "build_report", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("combined envelope text = %q", out)
	}
}

func TestClient_CallTool_LaterEnvelopeArtifactIsValidated(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	c := &Client{name: "multiitem", stdin: clientWrite, stdout: bufio.NewReader(clientRead), lineCh: make(chan lineResult, 10), done: make(chan struct{}), writeCh: make(chan *queuedRequest, 2), writeDone: make(chan struct{}), closed: make(chan struct{}), pending: make(map[int]chan callResponse), timeout: 5 * time.Second}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		_ = clientWrite.Close()
		_ = clientRead.Close()
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go func() {
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		fmt.Fprintf(serverWrite, `{"jsonrpc":"2.0","id":%d,"result":{"content":[`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"first\"}"},`+
			`{"type":"text","text":"{\"schema\":\"odek.tool-result/v1\",\"text\":\"second\",\"artifacts\":[{\"schema\":\"odek.artifact-ref/v1\",\"id\":\"x\",\"uri\":\"file:///tmp/x\",\"media_type\":\"text/plain\"}]}"}`+
			`]}}`+"\n", id)
	}()
	if _, err := c.CallTool(context.Background(), "build_report", `{}`); err == nil {
		t.Fatal("accepted later envelope artifact without configured roots")
	}
}

func TestClient_CallTool_AggregateArtifactCapAcrossEnvelopes(t *testing.T) {
	makeEnvelope := func(start, count int) string {
		refs := make([]map[string]any, count)
		for i := range refs {
			refs[i] = map[string]any{"schema": "odek.artifact-ref/v1", "id": fmt.Sprintf("id-%d", start+i), "uri": "file:///tmp/x", "media_type": "text/plain"}
		}
		b, _ := json.Marshal(map[string]any{"schema": "odek.tool-result/v1", "text": "part", "artifacts": refs})
		return string(b)
	}
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	c := &Client{name: "multiitem", stdin: clientWrite, stdout: bufio.NewReader(clientRead), lineCh: make(chan lineResult, 10), done: make(chan struct{}), writeCh: make(chan *queuedRequest, 2), writeDone: make(chan struct{}), closed: make(chan struct{}), pending: make(map[int]chan callResponse), timeout: 5 * time.Second}
	go c.readLoop()
	go c.writeLoop()
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		_ = clientWrite.Close()
		_ = clientRead.Close()
		_ = serverWrite.Close()
		_ = serverRead.Close()
	}()
	go func() {
		id, ok := readJSONRPCID(serverRead)
		if !ok {
			return
		}
		result := map[string]any{"content": []map[string]string{{"type": "text", "text": makeEnvelope(0, 40)}, {"type": "text", "text": makeEnvelope(40, 40)}}}
		resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		b, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(serverWrite, "%s\n", b)
	}()
	_, err := c.CallTool(context.Background(), "build_report", `{}`)
	if err == nil || !strings.Contains(err.Error(), "cap is") {
		t.Fatalf("error = %v, want aggregate artifact cap error", err)
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
