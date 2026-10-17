package main

import (
	"sync"
	"testing"
	"time"
)

// A WebSocket handler can start while a drain from an earlier shutdown is
// still waiting (tests run several serve instances in one process). With a
// sync.WaitGroup that add-from-zero concurrent with Wait is a data race the
// race detector reports; liveCounter orders both under its mutex.
func TestLiveCounter_AddConcurrentWithWait(t *testing.T) {
	var c liveCounter
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.wait() }()
		go func() { defer wg.Done(); c.add(); c.done() }()
	}
	wg.Wait()
	c.wait() // all handlers left: returns at once
}

func TestLiveCounter_WaitBlocksUntilLastDone(t *testing.T) {
	var c liveCounter
	c.add()
	c.add()
	waited := make(chan struct{})
	go func() { c.wait(); close(waited) }()
	c.done()
	select {
	case <-waited:
		t.Fatal("wait returned with a handler still live")
	case <-time.After(50 * time.Millisecond):
	}
	c.done()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the last handler left")
	}
	// The counter is reusable after it drains.
	c.add()
	c.done()
	c.wait()
}
