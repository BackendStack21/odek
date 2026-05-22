package transport

import (
	"net/http"
	"testing"
	"time"
)

func TestNewPooledClient_ReturnsNonNil(t *testing.T) {
	c := NewPooledClient(30 * time.Second)
	if c == nil {
		t.Fatal("NewPooledClient returned nil")
	}
}

func TestNewPooledClient_TimeoutApplied(t *testing.T) {
	c := NewPooledClient(5 * time.Second)
	if c.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", c.Timeout)
	}
}

func TestNewPooledClient_DefaultTimeout(t *testing.T) {
	c := NewPooledClient(0) // zero means use default
	if c.Timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, DefaultTimeout)
	}
}

func TestNewPooledClient_TransportConfigured(t *testing.T) {
	c := NewPooledClient(30 * time.Second)

	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}

	tests := []struct {
		name string
		got  int
		want int
	}{
		{"MaxIdleConns", tr.MaxIdleConns, DefaultMaxIdleConns},
		{"MaxIdleConnsPerHost", tr.MaxIdleConnsPerHost, DefaultMaxIdlePerHost},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestNewPooledClient_IdleTimeoutConfigured(t *testing.T) {
	c := NewPooledClient(30 * time.Second)
	tr := c.Transport.(*http.Transport)

	if tr.IdleConnTimeout != DefaultIdleTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, DefaultIdleTimeout)
	}
}

func TestNewPooledClient_DisableCompressionEnabled(t *testing.T) {
	c := NewPooledClient(30 * time.Second)
	tr := c.Transport.(*http.Transport)

	if !tr.DisableCompression {
		t.Error("DisableCompression = false, want true")
	}
}

func TestNewPooledClient_HTTP2Enabled(t *testing.T) {
	c := NewPooledClient(30 * time.Second)
	tr := c.Transport.(*http.Transport)

	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want true")
	}
}

// TestNewPooledClient_KeepAlivesNotDisabled verifies that the transport
// DOES NOT disable keep-alives — that would defeat connection pooling entirely.
func TestNewPooledClient_KeepAlivesNotDisabled(t *testing.T) {
	c := NewPooledClient(30 * time.Second)
	tr := c.Transport.(*http.Transport)

	if tr.DisableKeepAlives {
		t.Error("DisableKeepAlives = true, want false — keep-alives must be enabled for pooling")
	}
}

// BenchmarkNewPooledClient measures allocation overhead of client creation.
func BenchmarkNewPooledClient(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		NewPooledClient(0)
	}
}
