package main

import (
	"sync"
	"testing"
)

// Zero-value browserTool lazy-initializes state and client inside Call;
// concurrent first calls race and can build duplicate states.
func TestBrowserTool_LazyInitRace(t *testing.T) {
	tool := &browserTool{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tool.Call(`{"action":"navigate","url":"http://127.0.0.1:1/"}`)
		}()
	}
	wg.Wait()
	if tool.state == nil || tool.client == nil {
		t.Fatal("expected state and client to be initialized")
	}
}
