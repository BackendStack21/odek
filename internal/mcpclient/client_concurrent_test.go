package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClient_ConcurrentCallsNoResponseLoss verifies that concurrent tool calls
// to the same MCP client don't lose responses due to incorrect routing.
// BUG: call() read from a shared lineCh and discarded responses with
// non-matching IDs. With concurrent calls, responses for one goroutine
// were consumed and silently dropped by another.
func TestClient_ConcurrentCallsNoResponseLoss(t *testing.T) {
	// Use io.Pipe for bidirectional communication with a simulated server.
	serverRead, clientWrite := io.Pipe() // client writes to server
	clientRead, serverWrite := io.Pipe() // server writes to client

	client := &Client{
		name:      "test",
		nextID:    0,
		stdin:     clientWrite,
		stdout:    bufio.NewReader(clientRead),
		pending:   make(map[int]chan callResponse),
		done:      make(chan struct{}),
		writeCh:   make(chan []byte, 32),
		writeDone: make(chan struct{}),
		closed:    make(chan struct{}),
	}
	go client.readLoop()
	go client.writeLoop()

	// Simulate a server that reads both requests, then sends responses
	// out of order (pipelined), identifying each response by the
	// "request_id" field in the tool call arguments rather than by
	// arrival order — this avoids a race condition where goroutine
	// scheduling determines which request arrives first.
	type rawRequest struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}
	go func() {
		scanner := bufio.NewScanner(serverRead)
		var reqs []rawRequest

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var req rawRequest
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Errorf("mock server: unmarshal request: %v", err)
				continue
			}
			reqs = append(reqs, req)
			if len(reqs) >= 2 {
				break
			}
		}

		if len(reqs) < 2 {
			t.Errorf("mock server: only got %d requests", len(reqs))
			return
		}

		// Build a map from request ID → caller's tool-call argument "id".
		// This avoids a race condition: we don't know which goroutine's
		// request arrived first, so we identify responses by content
		// rather than by arrival order.
		callerIDs := make(map[int]string, 2)
		for _, req := range reqs {
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if req.Params != nil {
				json.Unmarshal(req.Params, &params)
			}
			callerID := "unknown"
			if idVal, ok := params.Arguments["id"]; ok {
				callerID, _ = idVal.(string)
			}
			callerIDs[req.ID] = callerID
		}

		// Send responses out of order: respond to reqs[1] first.
		for _, idx := range []int{1, 0} {
			req := reqs[idx]
			callerID := callerIDs[req.ID]
			resp, _ := json.Marshal(response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"response-` + callerID + `"}]}`),
			})
			serverWrite.Write(append(resp, '\n'))
			if idx == 1 {
				time.Sleep(10 * time.Millisecond) // small delay between responses
			}
		}
	}()

	// Make two concurrent calls with a timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var mu sync.Mutex
	results := make(map[string]string)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		text, err := client.CallTool(ctx, "test_tool", `{"id":"a"}`)
		mu.Lock()
		if err != nil {
			results["a"] = fmt.Sprintf("err=%v", err)
		} else {
			results["a"] = fmt.Sprintf("ok=%s", text)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		text, err := client.CallTool(ctx, "test_tool", `{"id":"b"}`)
		mu.Lock()
		if err != nil {
			results["b"] = fmt.Sprintf("err=%v", err)
		} else {
			results["b"] = fmt.Sprintf("ok=%s", text)
		}
		mu.Unlock()
	}()

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if strings.HasPrefix(results["a"], "err=") && strings.Contains(results["a"], "context deadline") {
		t.Errorf("call A timed out — response was stolen by call B")
	}
	if strings.HasPrefix(results["b"], "err=") && strings.Contains(results["b"], "context deadline") {
		t.Errorf("call B timed out — response was stolen by call A")
	}
	if results["a"] == "ok=response-a" {
		t.Logf("call A = %s ✓", results["a"])
	} else {
		t.Errorf("call A = %q, want 'ok=response-a'", results["a"])
	}
	if results["b"] == "ok=response-b" {
		t.Logf("call B = %s ✓", results["b"])
	} else {
		t.Errorf("call B = %q, want 'ok=response-b'", results["b"])
	}
}
