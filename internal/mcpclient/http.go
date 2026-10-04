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

// tokenEnvAllowed restricts which environment variables token_env may name.
// A project-level config can set url to an attacker host; an unrestricted
// token_env would then ship any operator secret (provider API keys, cloud
// credentials) to that host as a Bearer token. Names must be uppercase
// MCP_-prefixed identifiers, so operators declare MCP tokens explicitly in
// ~/.odek/secrets.env (e.g. MCP_REMOTE_TOKEN) and no other secret is
// reachable through this field.
func tokenEnvAllowed(name string) bool {
	if !strings.HasPrefix(name, "MCP_") || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// newHTTPClient builds the URL-transport branch of New: no subprocess, no
// read/write goroutines; every call() round-trips one HTTP request.
func newHTTPClient(name string, cfg ServerConfig, opts httpOptions) (*Client, error) {
	if _, err := validateServerURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcpclient %s: %w", name, err)
	}

	token := ""
	if cfg.TokenEnv != "" {
		if !tokenEnvAllowed(cfg.TokenEnv) {
			return nil, fmt.Errorf("mcpclient %s: token_env %q is not allowed; it must be an uppercase MCP_-prefixed variable name (declare the token in ~/.odek/secrets.env)", name, cfg.TokenEnv)
		}
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
		return c.matchSSEFrame(body, id)
	}

	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return c.matchedResult(r, id)
}

// matchSSEFrame parses an SSE body into complete event payloads and returns
// the result of the frame whose JSON-RPC id matches the request. Servers may
// interleave notification frames (no id) with the response; per the SSE spec
// a single event's data is the concatenation of its data: lines.
func (c *Client) matchSSEFrame(body []byte, id int) (json.RawMessage, error) {
	var parseErr error
	for _, frame := range sseFrames(body) {
		var r response
		if err := json.Unmarshal([]byte(frame), &r); err != nil {
			parseErr = err
			continue
		}
		if r.Method != "" {
			continue // server-initiated notification frame
		}
		return c.matchedResult(r, id)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse response: %w", parseErr)
	}
	return nil, fmt.Errorf("sse stream carried no JSON-RPC response frame for request id %d", id)
}

// matchedResult verifies the response carries the caller's request id.
// Without this, a notification or out-of-order frame is silently returned as
// the answer (an empty result) instead of an error.
func (c *Client) matchedResult(r response, id int) (json.RawMessage, error) {
	if r.ID != id {
		return nil, fmt.Errorf("response id %d does not match request id %d", r.ID, id)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

// httpNotify sends a JSON-RPC notification (no id, no response expected) to
// the Streamable HTTP endpoint. 202 Accepted is the expected status; 200 with
// a discarded body is tolerated.
func (c *Client) httpNotify(ctx context.Context, method string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpc.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notify %q: http %d", method, resp.StatusCode)
	}
	return nil
}

// sseFrames parses an SSE body into complete event payloads: within one
// event, data: lines are joined with "\n" (spec behavior); events are
// separated by blank lines; comment/event/id field lines are ignored. A
// trailing unterminated event still yields its accumulated data.
func sseFrames(body []byte) []string {
	var frames []string
	var data []string
	flush := func() {
		if len(data) > 0 {
			frames = append(frames, strings.Join(data, "\n"))
			data = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		d, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = append(data, strings.TrimPrefix(d, " "))
	}
	flush()
	return frames
}

// sseData returns the first SSE event payload; retained for callers that
// only care about a single-frame stream.
func sseData(body []byte) []byte {
	if frames := sseFrames(body); len(frames) > 0 {
		return []byte(frames[0])
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
