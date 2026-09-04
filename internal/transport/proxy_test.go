package transport

import (
	"net/http"
	"testing"
	"time"
)

// The pooled transport is shared by every API client (LLM buffered +
// streaming, Telegram, MCP). Omitting Proxy on the custom Transport
// literal silently disabled HTTP(S)_PROXY / NO_PROXY — the stdlib default
// is ProxyFromEnvironment — so corporate-proxy users got failed egress
// (or policy-violating direct egress) with no warning.
func TestPooledTransport_HonorsProxyEnvironment(t *testing.T) {
	tr := NewPooledClient(time.Second).Transport.(*http.Transport)
	if tr.Proxy == nil {
		t.Fatal("pooled transport has nil Proxy — HTTP(S)_PROXY/NO_PROXY are ignored")
	}
}
