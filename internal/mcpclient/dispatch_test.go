package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCancelledQueuedRequestNeverDispatches(t *testing.T) {
	reader, writer := io.Pipe()
	c := &Client{timeout: 25 * time.Millisecond, stdin: writer,
		pending: make(map[int]chan callResponse), writeCh: make(chan *queuedRequest, 3),
		writeDone: make(chan struct{}), closed: make(chan struct{})}
	go c.writeLoop()
	t.Cleanup(func() {
		close(c.closed)
		_ = reader.Close()
		_ = writer.Close()
		<-c.writeDone
	})
	// The first write is already underway but blocked by server backpressure.
	_, err := c.call(context.Background(), "tools/call", json.RawMessage(`{"name":"first"}`))
	var first *RequestError
	if !errors.As(err, &first) || !first.OutcomeUnknown || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("in-flight timeout must report unknown outcome: %v", err)
	}
	_, err = c.call(context.Background(), "tools/call", json.RawMessage(`{"name":"cancelled_write"}`))
	var queued *RequestError
	if !errors.As(err, &queued) || queued.OutcomeUnknown || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued timeout must report no dispatch: %v", err)
	}
	// A sentinel proves the writer drained the queue past the cancelled entry.
	c.writeCh <- &queuedRequest{ctx: context.Background(), data: []byte("sentinel\n")}
	seen := make(chan string, 3)
	go func() {
		s := bufio.NewScanner(reader)
		for s.Scan() {
			seen <- s.Text()
			if s.Text() == "sentinel" {
				return
			}
		}
	}()
	for _, expected := range []string{"first", "sentinel"} {
		select {
		case line := <-seen:
			if !strings.Contains(line, expected) {
				t.Fatalf("received %q, expected %q", line, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("writer did not drain live requests")
		}
	}
}

func TestRequestCancelledBeforeDispatchCannotStart(t *testing.T) {
	r := &queuedRequest{ctx: context.Background()}
	err := r.fail(context.Canceled)
	if r.begin() {
		t.Fatal("cancelled request began dispatch")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation identity: %v", err)
	}
}

func TestCallUnblocksWhenClientCloses(t *testing.T) {
	c := &Client{timeout: time.Second, pending: make(map[int]chan callResponse),
		writeCh: make(chan *queuedRequest, 1), closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := c.call(context.Background(), "tools/call", nil); done <- err }()
	<-c.writeCh
	close(c.closed)
	select {
	case err := <-done:
		var re *RequestError
		if !errors.As(err, &re) || re.OutcomeUnknown {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("closed client left caller waiting for its timeout")
	}
}
