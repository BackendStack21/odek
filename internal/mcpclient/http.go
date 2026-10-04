// HTTP (Streamable HTTP) transport for MCP servers.
//
// A ServerConfig carrying "url" instead of "command" produces a Client that
// posts JSON-RPC messages to a remote MCP server over HTTP(S), authenticating
// with a Bearer token resolved from the environment variable named by
// "token_env". All per-server limits (timeout_seconds, max_response_bytes,
// max_result_chars) and artifact-ref fail-closed validation apply identically
// to the stdio transport.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// validateServerURL accepts only absolute http(s) URLs with a host. Anything
// else (other schemes, embedded credentials, empty hosts) is rejected before
// a request is ever built.
func validateServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("credentials in the URL are not allowed; use token_env instead")
	}
	return u, nil
}

// Option customizes Client construction.
type Option func(*httpOptions)

type httpOptions struct {
	client *http.Client
}

// WithHTTPClient supplies the *http.Client used for URL-configured MCP
// servers. Callers on the cmd surface pass an SSRF-guarded transport so the
// dial layer refuses internal addresses and pins validated IPs (see
// cmd/odek/ssrf_guard.go). Without this option a default client is used.
func WithHTTPClient(c *http.Client) Option {
	return func(o *httpOptions) { o.client = c }
}

// newHTTPClient builds the URL-transport branch of New: no subprocess, no
// read/write goroutines; every call() round-trips one HTTP request.
func newHTTPClient(name string, cfg ServerConfig, opts httpOptions) (*Client, error) {
	if _, err := validateServerURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcpclient %s: %w", name, err)
	}

	token := ""
	if cfg.TokenEnv != "" {
		token = os.Getenv(cfg.TokenEnv)
	}

	httpc := opts.client
	if httpc == nil {
		httpc = &http.Client{}
	}

	return &Client{
		name:   name,
		url:    cfg.URL,
		token:  token,
		httpc:  httpc,
		closed: make(chan struct{}),
	}, nil
}

// httpCall performs one JSON-RPC request over HTTP and returns the raw result.
// It mirrors the wire format of the stdio transport (same request/response
// structs) so Discover/CallTool work unchanged. Responses may arrive as plain
// JSON or as a single-event SSE stream, per the Streamable HTTP transport.
func (c *Client) httpCall(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	maxResp := c.maxResponseBytes
	if maxResp <= 0 {
		maxResp = maxMCPResponseLine
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResp+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > maxResp {
		return nil, fmt.Errorf("response exceeds max_response_bytes=%d", maxResp)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateForError(body, 512))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body = sseData(body)
	}

	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

// sseData extracts the JSON payload of the first SSE data: line. The space
// after the colon is optional per the SSE spec.
func sseData(body []byte) []byte {
	for _, line := range strings.Split(string(body), "\n") {
		d, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		d = strings.TrimPrefix(d, " ")
		if len(d) > 0 {
			return []byte(d)
		}
	}
	return body
}

func truncateForError(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// closeHTTP is the URL-transport Close: there is no process or pipe, only the
// closed channel so concurrent callers observe the shutdown.
func (c *Client) closeHTTP() {
	c.closeOnce.Do(func() { close(c.closed) })
}
