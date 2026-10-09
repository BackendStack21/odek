package mcpclient

import (
	"bufio"
	"io"
	"testing"
	"time"
)

// A line without an id (or id:null, e.g. a JSON-RPC parse-error reply) decodes
// to ID 0 and must not be routed to the in-flight call that owns id 0.
func TestRED_ReadLoopIDlessResponseNotRoutedToIDZero(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	c := &Client{
		name: "x", stdout: bufio.NewReader(clientRead),
		pending: make(map[int]chan callResponse),
	}
	ch := make(chan callResponse, 1)
	c.pending[0] = ch
	go c.readLoop()
	defer serverWrite.Close()
	io.WriteString(serverWrite, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"forged"}}`+"\n")
	io.WriteString(serverWrite, `{"jsonrpc":"2.0","result":{"forged":true}}`+"\n")
	select {
	case r := <-ch:
		t.Fatalf("id-less response delivered to call id 0: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestReadLoop_MatchingIDStillRouted(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	c := &Client{name: "x", stdout: bufio.NewReader(clientRead), pending: make(map[int]chan callResponse)}
	ch := make(chan callResponse, 1)
	c.pending[1] = ch
	go c.readLoop()
	defer serverWrite.Close()
	io.WriteString(serverWrite, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`+"\n")
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response with id 1 not delivered")
	}
}
