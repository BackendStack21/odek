package main

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// startServeTest starts the real server and returns a stop function that waits
// for its handlers and session writes to finish. Defer the returned function
// before opening clients, so shutdown precedes environment and temp-dir cleanup.
func startServeTest(t *testing.T, ln net.Listener, mux *http.ServeMux) func() {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- serveOnListener(ln, mux) }()
	return func() {
		t.Helper()
		http.DefaultClient.CloseIdleConnections()
		_ = ln.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("serve shutdown: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("serve shutdown did not finish before test cleanup")
		}
	}
}
