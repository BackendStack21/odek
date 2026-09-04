// Package transport provides tuned HTTP transports for odek's API clients.
// All clients (LLM, Telegram, MCP) share the same connection pool to avoid
// redundant TCP+TLS handshakes on every request.
package transport

import (
	"net"
	"net/http"
	"sync"
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

var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

// pooledTransport returns the process-wide shared *http.Transport. Every
// client built by this package reuses it, so the buffered and streaming LLM
// clients (and every other API client) share one connection pool, matching
// the package's documented behavior.
func pooledTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		sharedTransport = &http.Transport{
			// Honor HTTP(S)_PROXY / NO_PROXY: a custom Transport literal with
			// no Proxy field silently drops the stdlib default
			// (ProxyFromEnvironment) — corporate-proxy users got failed or
			// policy-violating direct egress.
			Proxy:               http.ProxyFromEnvironment,
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
		}
	})
	return sharedTransport
}

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
		Timeout:   timeout,
		Transport: pooledTransport(),
	}
}

// NewPooledClientNoDeadline creates an *http.Client with no whole-request
// timeout, sharing the pooled transport. It is for streaming responses,
// where a client-level Timeout would kill the body read mid-stream;
// deadlines are enforced per request via context at the call site
// (hard wall-clock cap + idle watchdog — see internal/llm CallStream).
func NewPooledClientNoDeadline() *http.Client {
	return &http.Client{
		Timeout:   0,
		Transport: pooledTransport(),
	}
}
