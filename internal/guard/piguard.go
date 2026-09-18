package guard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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

	dr, err := decodeDetectResponse(resp)
	if err != nil {
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

	var rawBatch struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(resp, &rawBatch); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}
	if len(rawBatch.Results) != len(texts) {
		return nil, fmt.Errorf("decode batch response: got %d results for %d inputs", len(rawBatch.Results), len(texts))
	}

	results := make([]Result, len(rawBatch.Results))
	thr := threshold(p.cfg)
	for i, raw := range rawBatch.Results {
		r, err := decodeDetectResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("decode batch response item %d: %w", i, err)
		}
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

	dr, err := decodeDetectResponse(resp)
	if err != nil {
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
		resp, err = p.rpcSocket(ctx, body)
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
func (p *piguardClient) rpcSocket(ctx context.Context, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", p.socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial piguard socket: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout(p.cfg))
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	if _, err := conn.Write(append(bytes.TrimRight(body, "\n"), '\n')); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("write piguard socket: %w", err)
	}
	resp, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(resp) == 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
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

func decodeDetectResponse(body []byte) (detectResponse, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return detectResponse{}, err
	}
	if err := validateRawDetectResponse(raw); err != nil {
		return detectResponse{}, err
	}
	var dr detectResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return detectResponse{}, err
	}
	if err := validateDetectResponse(dr); err != nil {
		return detectResponse{}, err
	}
	return dr, nil
}

func validateRawDetectResponse(raw map[string]json.RawMessage) error {
	label, ok := raw["label"]
	if !ok {
		return fmt.Errorf("missing label")
	}
	if string(label) == "null" {
		return fmt.Errorf("invalid label")
	}
	score, ok := raw["score"]
	if !ok {
		return fmt.Errorf("missing score")
	}
	if string(score) == "null" {
		return fmt.Errorf("invalid score")
	}
	return nil
}

func validateDetectResponse(r detectResponse) error {
	if r.Label != "BENIGN" && r.Label != "INJECTION" {
		return fmt.Errorf("invalid label %q", r.Label)
	}
	if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) || r.Score < 0 || r.Score > 1 {
		return fmt.Errorf("invalid score %v", r.Score)
	}
	return nil
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
