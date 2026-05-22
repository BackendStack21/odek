// Package transport provides tuned HTTP transports for odek's API clients.
// All clients (LLM, Telegram, MCP) share the same connection pool to avoid
// redundant TCP/TLS handshakes on every request.
package transport

import (
	"net"
	"net/http"
	"time"
)

// Default values for the pooled HTTP transport.
const (
	DefaultTimeout        = 120 * time.Second
	DefaultMaxIdleConns   = 20
	DefaultMaxIdlePerHost = 10
	DefaultIdleTimeout    = 90 * time.Second
	DefaultKeepAlive      = 30 * time.Second
)

// NewPooledClient creates an *http.Client with a tuned transport that
// reuses TCP/TLS connections across requests. Pass 0 for timeout to use
// the default (120s).
//
// Use this instead of bare &http.Client{Timeout: ...} in all API clients
// to avoid the ~200ms per-request overhead of TCP+TLS handshakes.
func NewPooledClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        DefaultMaxIdleConns,
			MaxIdleConnsPerHost: DefaultMaxIdlePerHost,
			IdleConnTimeout:     DefaultIdleTimeout,
			DisableCompression:  true, // API responses are typically uncompressed
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: DefaultKeepAlive,
				DualStack: true,
			}).DialContext,
			ForceAttemptHTTP2: true,
		},
	}
}
