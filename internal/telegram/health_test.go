package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewHealthServer(t *testing.T) {
	hs := NewHealthServer("127.0.0.1:0")
	if hs == nil {
		t.Fatal("NewHealthServer returned nil")
	}
	if hs.addr != "127.0.0.1:0" {
		t.Errorf("addr = %q, want %q", hs.addr, "127.0.0.1:0")
	}
	if hs.startTime.IsZero() {
		t.Error("startTime should not be zero")
	}
	if hs.ready.Load() {
		t.Error("ready should be false initially")
	}
}

func TestHealthServer_SetLogger(t *testing.T) {
	hs := NewHealthServer("")
	if hs.log == nil {
		t.Fatal("default log should not be nil")
	}

	l := NewNopLogger()
	hs.SetLogger(l)
	if hs.log != l {
		t.Error("SetLogger did not update the logger")
	}

	hs.SetLogger(nil)
	if hs.log == nil {
		t.Error("SetLogger(nil) should set a NopLogger, not nil")
	}
}

func TestHealthServer_SetReady(t *testing.T) {
	hs := NewHealthServer("")
	if hs.ready.Load() {
		t.Error("ready should be false initially")
	}

	hs.SetReady()
	if !hs.ready.Load() {
		t.Error("ready should be true after SetReady()")
	}
}

func TestHealthServer_EmptyAddrDoesNothing(t *testing.T) {
	hs := NewHealthServer("")
	err := hs.Start(context.Background())
	if err != nil {
		t.Errorf("Start with empty addr should return nil, got: %v", err)
	}
}

func TestHealthServer_StartAndShutdown(t *testing.T) {
	hs := NewHealthServer("127.0.0.1:0")
	hs.SetReady() // mark ready so health check returns 200
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- hs.Start(ctx)
	}()

	// Wait for server to start
	var resp *http.Response
	var err error
	for i := 0; i < 10; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err = http.Get("http://" + hs.addr + "/health")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Failed to reach health server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), "server closed") {
			t.Errorf("unexpected error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}

func TestHealthServer_ServeHTTP_Healthy(t *testing.T) {
	hs := NewHealthServer("")
	hs.SetReady()

	req, _ := http.NewRequest("GET", "/health", nil)
	rec := &mockResponseWriter{}
	hs.ServeHTTP(rec, req)

	if rec.statusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.statusCode)
	}
	if len(rec.body) == 0 {
		t.Error("expected non-empty response body")
	}
}

// mockResponseWriter implements http.ResponseWriter for testing
type mockResponseWriter struct {
	statusCode int
	body       []byte
	headers    http.Header
}

func (m *mockResponseWriter) Header() http.Header {
	if m.headers == nil {
		m.headers = make(http.Header)
	}
	return m.headers
}

func (m *mockResponseWriter) Write(b []byte) (int, error) {
	m.body = append(m.body, b...)
	return len(b), nil
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}
