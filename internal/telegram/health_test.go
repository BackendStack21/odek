package telegram

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHealthServer_Returns200(t *testing.T) {
	hs := NewHealthServer("127.0.0.1:0")
	hs.ready.Store(true)

	ts := &http.Server{Addr: hs.addr, Handler: hs}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go ts.Serve(ln)

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
	if _, ok := body["uptime_seconds"]; !ok {
		t.Error("missing uptime_seconds")
	}
}

func TestHealthServer_NotReady(t *testing.T) {
	hs := NewHealthServer("127.0.0.1:0")
	// ready defaults to false

	ts := &http.Server{Addr: hs.addr, Handler: hs}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go ts.Serve(ln)

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestHealthServer_404OnOtherPaths(t *testing.T) {
	hs := NewHealthServer("127.0.0.1:0")

	ts := &http.Server{Addr: hs.addr, Handler: hs}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go ts.Serve(ln)

	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
