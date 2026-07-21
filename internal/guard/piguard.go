package guard

import (
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
// sidecar over HTTP or a Unix domain socket.
type piguardClient struct {
	cfg        *Config
	client     *http.Client
	detectURL  string
	longURL    string
	batchURL   string
}

// detectRequest is the body for POST /detect.
type detectRequest struct {
	Text string `json:"text"`
}

// batchRequest is the body for POST /raw.
type batchRequest struct {
	Texts []string `json:"texts"`
}

// longRequest is the body for POST /long.
type longRequest struct {
	Long string `json:"long"`
}

// detectResponse is the common JSON response shape for detect/long/batch items.
type detectResponse struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
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

	timeout := timeout(cfg)
	transport := http.DefaultTransport.(*http.Transport).Clone()

	var client *http.Client
	if cfg.SocketPath != "" {
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", cfg.SocketPath)
		}
		client = &http.Client{Timeout: timeout, Transport: transport}
	} else {
		client = &http.Client{Timeout: timeout}
	}

	return &piguardClient{
		cfg:       cfg,
		client:    client,
		detectURL: endpoint(cfg, "detect"),
		longURL:   endpoint(cfg, "long"),
		batchURL:  endpoint(cfg, "raw"),
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

// Detect calls POST /detect.
func (p *piguardClient) Detect(ctx context.Context, text string) (Result, error) {
	start := time.Now()
	payload := detectRequest{Text: truncateForGuard(text, p.cfg)}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal detect request: %w", err)
	}

	resp, err := p.post(ctx, p.detectURL, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	var dr detectResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return Result{}, fmt.Errorf("decode detect response: %w", err)
	}
	return resultFromResponse(dr, start, threshold(p.cfg)), nil
}

// DetectBatch calls POST /raw.
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

	resp, err := p.post(ctx, p.batchURL, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var br batchResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	results := make([]Result, len(br.Results))
	thr := threshold(p.cfg)
	for i, r := range br.Results {
		results[i] = resultFromResponse(r, start, thr)
	}
	return results, nil
}

// DetectLong calls POST /long.
func (p *piguardClient) DetectLong(ctx context.Context, text string) (Result, error) {
	start := time.Now()
	payload := longRequest{Long: truncateForGuard(text, p.cfg)}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal long request: %w", err)
	}

	resp, err := p.post(ctx, p.longURL, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	var dr detectResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return Result{}, fmt.Errorf("decode long response: %w", err)
	}
	return resultFromResponse(dr, start, threshold(p.cfg)), nil
}

// Close is a no-op for the HTTP client.
func (p *piguardClient) Close() error { return nil }

// post sends a JSON POST request and returns the response.
func (p *piguardClient) post(ctx context.Context, urlStr string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, urlStr)
	}
	return resp, nil
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
