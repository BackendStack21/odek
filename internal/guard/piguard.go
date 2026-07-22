package guard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"time"
)

// piguardClient is a Guard implementation that calls a go-prompt-injection-guard
// sidecar. With url it speaks HTTP JSON (via the docker HTTP gateway); with
// socket_path it speaks the daemon's native newline-delimited JSON protocol
// directly over the Unix socket.
type piguardClient struct {
	cfg        *Config
	client     *http.Client
	socketPath string
	detectURL  string
	longURL    string
	batchURL   string
}

// detectRequest is the body for POST /detect (HTTP) or a {"text":...} daemon
// line (socket).
type detectRequest struct {
	Text string `json:"text"`
}

// batchRequest is the body for POST /raw (HTTP) or a {"texts":[...]} daemon
// line (socket).
type batchRequest struct {
	Texts []string `json:"texts"`
}

// longRequest is the body for POST /long (HTTP) or a {"long":...} daemon
// line (socket).
type longRequest struct {
	Long string `json:"long"`
}

// detectResponse is the common JSON response shape for detect/long/batch items.
type detectResponse struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// errorResponse is the daemon's reply to a malformed request line.
type errorResponse struct {
	Error string `json:"error"`
}

// batchResponse is the response body for POST /raw.
type batchResponse struct {
	Results []detectResponse `json:"results"`
}

// newPiguardClient creates a piguard client from cfg.
func newPiguardClient(cfg *Config) (Guard, error) {
	if cfg == nil {
		return nil, fmt.Errorf("piguard config is nil")
	}
	if cfg.URL == "" && cfg.SocketPath == "" {
		return nil, fmt.Errorf("piguard requires url or socket_path")
	}

	return &piguardClient{
		cfg:        cfg,
		client:     &http.Client{Timeout: timeout(cfg)},
		socketPath: cfg.SocketPath,
		detectURL:  endpoint(cfg, "detect"),
		longURL:    endpoint(cfg, "long"),
		batchURL:   endpoint(cfg, "raw"),
	}, nil
}

// endpoint resolves the URL for a named endpoint, deriving from cfg.URL when
// explicit endpoint URLs are not provided.
func endpoint(cfg *Config, name string) string {
	switch name {
	case "detect":
		if cfg.URL != "" {
			return cfg.URL
		}
	case "long":
		if cfg.LongURL != "" {
			return cfg.LongURL
		}
	case "raw":
		if cfg.BatchURL != "" {
			return cfg.BatchURL
		}
	}

	base := cfg.URL
	if base == "" {
		base = "http://localhost/detect"
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = path.Join(path.Dir(u.Path), name)
	return u.String()
}

// Detect classifies a single text.
func (p *piguardClient) Detect(ctx context.Context, text string) (Result, error) {
	start := time.Now()
	payload := detectRequest{Text: truncateForGuard(text, p.cfg)}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal detect request: %w", err)
	}

	resp, err := p.rpc(ctx, p.detectURL, body)
	if err != nil {
		return Result{}, err
	}

	var dr detectResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		return Result{}, fmt.Errorf("decode detect response: %w", err)
	}
	return resultFromResponse(dr, start, threshold(p.cfg)), nil
}

// DetectBatch classifies many texts in one round-trip.
func (p *piguardClient) DetectBatch(ctx context.Context, texts []string) ([]Result, error) {
	start := time.Now()
	truncated := make([]string, len(texts))
	for i, text := range texts {
		truncated[i] = truncateForGuard(text, p.cfg)
	}
	payload := batchRequest{Texts: truncated}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal batch request: %w", err)
	}

	resp, err := p.rpc(ctx, p.batchURL, body)
	if err != nil {
		return nil, err
	}

	var br batchResponse
	if err := json.Unmarshal(resp, &br); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	results := make([]Result, len(br.Results))
	thr := threshold(p.cfg)
	for i, r := range br.Results {
		results[i] = resultFromResponse(r, start, thr)
	}
	return results, nil
}

// DetectLong scans a document larger than the model's token window in full.
func (p *piguardClient) DetectLong(ctx context.Context, text string) (Result, error) {
	start := time.Now()
	payload := longRequest{Long: truncateForGuard(text, p.cfg)}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal long request: %w", err)
	}

	resp, err := p.rpc(ctx, p.longURL, body)
	if err != nil {
		return Result{}, err
	}

	var dr detectResponse
	if err := json.Unmarshal(resp, &dr); err != nil {
		return Result{}, fmt.Errorf("decode long response: %w", err)
	}
	return resultFromResponse(dr, start, threshold(p.cfg)), nil
}

// Close is a no-op for the HTTP client.
func (p *piguardClient) Close() error { return nil }

// rpc sends one request payload to the sidecar and returns the raw response
// body. In socket mode it speaks the daemon's native newline-delimited JSON
// protocol over the Unix socket; otherwise it POSTs the payload as JSON to
// the given HTTP endpoint.
func (p *piguardClient) rpc(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	var resp []byte
	var err error
	if p.socketPath != "" {
		resp, err = p.rpcSocket(body)
	} else {
		resp, err = p.rpcHTTP(ctx, endpoint, body)
	}
	if err != nil {
		return nil, err
	}
	// The daemon answers malformed lines with {"error": "..."} instead of a
	// classification; surface it rather than decoding empty label/score.
	var er errorResponse
	if jsonErr := json.Unmarshal(resp, &er); jsonErr == nil && er.Error != "" {
		return nil, fmt.Errorf("piguard daemon error: %s", er.Error)
	}
	return resp, nil
}

// rpcSocket forwards the payload as one newline-delimited JSON line to the
// daemon's Unix socket and returns its single-line reply.
func (p *piguardClient) rpcSocket(body []byte) ([]byte, error) {
	conn, err := net.DialTimeout("unix", p.socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial piguard socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout(p.cfg)))

	if _, err := conn.Write(append(bytes.TrimRight(body, "\n"), '\n')); err != nil {
		return nil, fmt.Errorf("write piguard socket: %w", err)
	}
	resp, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(resp) == 0 {
		return nil, fmt.Errorf("read piguard socket: %w", err)
	}
	return bytes.TrimSpace(resp), nil
}

// rpcHTTP sends a JSON POST request and returns the response body.
func (p *piguardClient) rpcHTTP(ctx context.Context, urlStr string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, urlStr)
	}
	return io.ReadAll(resp.Body)
}

// resultFromResponse converts a PIGuard response into a Result, applying the
// configured threshold.
//
// The sidecar's score is the confidence of the predicted label — whichever
// label that is — not the injection probability (a confident BENIGN result
// also scores ~1.0). The threshold therefore only applies to INJECTION
// labels; comparing it against the score of a BENIGN result would reject
// virtually everything, since the model is confident on most inputs.
func resultFromResponse(r detectResponse, start time.Time, threshold float64) Result {
	injected := r.Label == "INJECTION" && r.Score >= threshold
	return Result{
		Label:    r.Label,
		Score:    r.Score,
		Injected: injected,
		Latency:  time.Since(start),
	}
}
