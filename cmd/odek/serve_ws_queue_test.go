package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	golangws "golang.org/x/net/websocket"
)

// A stalled client must not block delta producers: deltas are queued and
// coalesced, and the producer returns immediately.
func TestRED_WS_DeltaProducerNeverBlocksOnSlowClient(t *testing.T) {
	old := wsWriteTimeout.Load()
	wsWriteTimeout.Store(int64(30 * time.Second))
	t.Cleanup(func() { wsWriteTimeout.Store(old) })

	pipe := newBlockingWSConn(t)
	conn := pipe.wsConn
	start := time.Now()
	for i := 0; i < 5000; i++ {
		writeWSJSON(conn, map[string]any{"type": "token_delta", "content": "x"})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("delta producer blocked %v behind a stalled client", d)
	}
	if connWriter(conn).dead {
		t.Fatal("connection must not be marked dead while the write timeout has not elapsed")
	}
}

// Delta frames coalesce under backpressure but nothing is lost and order
// relative to non-delta frames is preserved.
func TestWS_QueueOrderingAndCoalescing(t *testing.T) {
	got := make(chan []string, 1)
	srvDone := make(chan struct{})
	srv := &http.Server{Handler: &golangws.Server{
		Handshake: func(*golangws.Config, *http.Request) error { return nil },
		Handler: func(conn *golangws.Conn) {
			defer close(srvDone)
			for i := 0; i < 3000; i++ {
				writeWSJSON(conn, map[string]any{"type": "token_delta", "content": fmt.Sprintf("a%d,", i)})
			}
			writeWSJSON(conn, map[string]any{"type": "tool_start", "name": "x"})
			for i := 0; i < 3000; i++ {
				writeWSJSON(conn, map[string]any{"type": "thinking_delta", "content": fmt.Sprintf("b%d,", i)})
			}
			writeWSJSON(conn, map[string]any{"type": "done"})
		},
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	c, err := golangws.Dial("ws://"+ln.Addr().String(), "", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		var types []string
		var a, b strings.Builder
		for {
			var m map[string]any
			if err := golangws.JSON.Receive(c, &m); err != nil {
				break
			}
			typ, _ := m["type"].(string)
			types = append(types, typ)
			switch typ {
			case "token_delta":
				a.WriteString(m["content"].(string))
			case "thinking_delta":
				b.WriteString(m["content"].(string))
			case "done":
				got <- append(types, a.String(), b.String())
				return
			}
		}
		got <- nil
	}()

	select {
	case res := <-got:
		if res == nil {
			t.Fatal("stream ended before done")
		}
		n := len(res)
		var wantA, wantB strings.Builder
		for i := 0; i < 3000; i++ {
			fmt.Fprintf(&wantA, "a%d,", i)
			fmt.Fprintf(&wantB, "b%d,", i)
		}
		if res[n-2] != wantA.String() || res[n-1] != wantB.String() {
			t.Fatal("delta content lost or reordered")
		}
		// All token_delta frames precede tool_start, all thinking_delta follow it.
		seenTool := false
		for _, ty := range res[:n-2] {
			switch ty {
			case "tool_start":
				seenTool = true
			case "token_delta":
				if seenTool {
					t.Fatal("token_delta after tool_start")
				}
			case "thinking_delta":
				if !seenTool {
					t.Fatal("thinking_delta before tool_start")
				}
			}
		}
		if res[n-3] != "done" {
			t.Fatalf("done not last: %v", res[n-3])
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout")
	}
	<-srvDone
}

func TestWS_DeltaFrameDetection(t *testing.T) {
	if typ, c, ok := wsDeltaFrame(map[string]any{"type": "token_delta", "content": "hi"}); !ok || typ != "token_delta" || c != "hi" {
		t.Fatal("token_delta not detected")
	}
	for _, v := range []any{
		map[string]any{"type": "token_delta", "content": "hi", "extra": 1},
		map[string]any{"type": "token", "content": "hi"},
		map[string]any{"type": "token_delta", "content": 5},
		map[string]string{"type": "token_delta", "content": "hi"},
		nil,
	} {
		if _, _, ok := wsDeltaFrame(v); ok {
			t.Fatalf("%#v wrongly detected as delta", v)
		}
	}
	b, _ := json.Marshal(map[string]any{"type": "token_delta", "content": "hi"})
	if string(b) != `{"content":"hi","type":"token_delta"}` {
		t.Fatalf("unexpected canonical encoding %s", b)
	}
}
