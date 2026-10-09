package mcpclient

import (
	"bufio"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// readLoop must copy each response line once (the retained result), not
// once for the text, once for the byte slice and again per probe decode.
func TestRED_MCPClient_ReadLoopCopiesLineOnce(t *testing.T) {
	const lines = 40
	body := strings.Repeat("r", 60000)
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&sb, `{"jsonrpc":"2.0","id":%d,"result":{"t":%q}}`+"\n", i, body)
	}
	c := &Client{
		name:    "t",
		stdout:  bufio.NewReader(strings.NewReader(sb.String())),
		pending: map[int]chan callResponse{},
	}
	chans := make([]chan callResponse, lines+1)
	for i := 1; i <= lines; i++ {
		chans[i] = make(chan callResponse, 1)
		c.pending[i] = chans[i]
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	c.readLoop()
	runtime.ReadMemStats(&after)

	for i := 1; i <= lines; i++ {
		r, ok := <-chans[i]
		if !ok || r.err != nil || len(r.result) < 60000 {
			t.Fatalf("line %d not delivered intact", i)
		}
	}
	payload := uint64(lines * 60000)
	if got := after.TotalAlloc - before.TotalAlloc; got > payload+payload/3 {
		t.Fatalf("readLoop allocated %d bytes for %d payload bytes", got, payload)
	}
}

func TestReadLoop_RoutingEdgeCases(t *testing.T) {
	in := strings.Join([]string{
		``,
		`not json`,
		`{"jsonrpc":"2.0","method":"notify","id":1}`,
		`{"jsonrpc":"2.0","id":null,"result":{}}`,
		`{"jsonrpc":"2.0","result":{}}`,
		`{"jsonrpc":"2.0","id":"str","result":{}}`,
		`{"jsonrpc":"2.0","id":1.5,"result":{}}`,
		`  {"jsonrpc":"2.0","id":2,"result":{"ok":1}}  `,
		`{"jsonrpc":"2.0","id":3,"error":{"code":-1,"message":"bad"}}`,
	}, "\n") + "\n"
	c := &Client{name: "t", stdout: bufio.NewReader(strings.NewReader(in)), pending: map[int]chan callResponse{}}
	ch1 := make(chan callResponse, 1)
	ch2 := make(chan callResponse, 1)
	ch3 := make(chan callResponse, 1)
	c.pending[1], c.pending[2], c.pending[3] = ch1, ch2, ch3
	c.readLoop()
	if r, ok := <-ch1; ok {
		t.Fatalf("id 1 should not be routed, got %+v", r)
	}
	r2 := <-ch2
	if r2.err != nil || string(r2.result) != `{"ok":1}` {
		t.Fatalf("id 2: %+v", r2)
	}
	r3 := <-ch3
	if r3.err == nil {
		t.Fatalf("id 3 expected rpc error")
	}
}
