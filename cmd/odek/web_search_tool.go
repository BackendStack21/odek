package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// maxSearXNGBody caps the response body read from SearXNG (defensive — a
// metasearch JSON payload is small; this guards against a misbehaving backend).
const maxSearXNGBody = 4 << 20 // 4 MiB

// Cold-start retry: how many extra attempts (and the delay between them) to
// make when SearXNG refuses the connection — covers the compose startup race
// where the container is up but the app isn't yet listening. Vars (not consts)
// so tests can shrink the delay.
var (
	searxngConnectRetries = 2
	searxngRetryDelay     = time.Second
)

// ═════════════════════════════════════════════════════════════════════════
// web_search Tool (SearXNG JSON API backend)
// ═════════════════════════════════════════════════════════════════════════

type webSearchTool struct {
	dangerousConfig danger.DangerousConfig
	cfg             config.WebSearchConfig
	client          *http.Client
}

func newWebSearchTool(dc danger.DangerousConfig, cfg config.WebSearchConfig) *webSearchTool {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15
	}
	t := &webSearchTool{dangerousConfig: dc, cfg: cfg}
	t.client = &http.Client{
		Timeout:       time.Duration(timeout) * time.Second,
		CheckRedirect: t.checkRedirect,
	}
	return t
}

// checkRedirect re-classifies every redirect hop. The configured base_url is
// trusted, but a compromised, buggy, or misconfigured SearXNG could 3xx the
// client toward an internal/metadata endpoint (SSRF). Re-classifying each hop —
// the same guard browser/http_batch install — closes that. Installing
// CheckRedirect disables Go's implicit 10-hop cap, so we re-impose it.
func (t *webSearchTool) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	target := req.URL.String()
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "web_search", Resource: target, Risk: danger.ClassifyURL(target),
	}, nil); err != nil {
		return fmt.Errorf("redirect to %s blocked: %w", target, err)
	}
	return nil
}

func (t *webSearchTool) Name() string { return "web_search" }

func (t *webSearchTool) Description() string {
	return `Search the web via a self-hosted SearXNG metasearch instance. Returns ranked results (title, url, snippet, engine) plus any direct answers. Use this to find pages, then fetch the most relevant URLs with the browser or http_batch tools. Results come from external search engines and are treated as untrusted content.`
}

type webSearchArgs struct {
	Query      string `json:"query"`
	Category   string `json:"category,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func (t *webSearchTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Optional SearXNG category to restrict the search (e.g. \"general\", \"news\", \"science\", \"it\"). Defaults to the instance configuration.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Optional cap on the number of results returned. Defaults to the configured maximum.",
			},
		},
		"required": []string{"query"},
	}
}

// searxngResponse models the subset of the SearXNG JSON API we surface.
type searxngResponse struct {
	Query   string `json:"query"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Engine  string `json:"engine"`
	} `json:"results"`
	// SearXNG answers are heterogeneous objects (simple {"answer": "..."} plus
	// other shapes like weather/translations). Keep them raw and decode each
	// element tolerantly (see Call) so an unexpected answer shape can never fail
	// the whole response and lose the results.
	Answers     []json.RawMessage `json:"answers"`
	Infoboxes   []json.RawMessage `json:"infoboxes"`
	Suggestions []string          `json:"suggestions"`
}

type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Engine  string `json:"engine,omitempty"`
}

type webSearchOutput struct {
	Query   string            `json:"query"`
	Results []webSearchResult `json:"results"`
	Answers []string          `json:"answers,omitempty"`
	Count   int               `json:"count"`
	Error   string            `json:"error,omitempty"`
}

func (t *webSearchTool) Call(argsJSON string) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("web_search: panic: %v", r)
			result = `{"error":"internal error"}`
		}
	}()

	var args webSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return jsonError("query is required")
	}
	if t.cfg.BaseURL == "" {
		return jsonError("web_search is not configured: set web_search.base_url to a SearXNG instance")
	}

	// Security: a web search ultimately fans out to external search engines and
	// leaks the query terms beyond the trust boundary, so gate it as network
	// egress — consistent with the browser/http_batch tools. The backend URL is
	// fixed config (not agent-controlled), so there is no SSRF surface here.
	if err := t.dangerousConfig.CheckOperation(danger.ToolOperation{
		Name: "web_search", Resource: query, Risk: danger.NetworkEgress,
	}, nil); err != nil {
		return jsonError(err.Error())
	}

	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = t.cfg.MaxResults
	}
	if maxResults <= 0 {
		maxResults = 10
	}

	resp, err := t.query(query, args.Category)
	if err != nil {
		return jsonResult(webSearchOutput{Query: query, Error: err.Error()})
	}

	out := webSearchOutput{Query: query}
	for _, r := range resp.Results {
		if len(out.Results) >= maxResults {
			break
		}
		out.Results = append(out.Results, webSearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
			Engine:  r.Engine,
		})
	}
	out.Count = len(out.Results)
	for _, raw := range resp.Answers {
		// Decode each answer element on its own: a non-conforming shape (e.g. a
		// weather/translation answer with no string "answer" field) is skipped,
		// never failing the whole response.
		var a struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		if s := strings.TrimSpace(a.Answer); s != "" {
			out.Answers = append(out.Answers, s)
		}
	}

	raw, mErr := json.Marshal(out)
	if mErr != nil {
		return jsonError("marshal error: " + mErr.Error())
	}
	// Results are external web content — wrap so the model distinguishes data
	// from instructions (a SERP snippet could carry an injection payload).
	return wrapUntrusted("web_search:"+query, string(raw)), nil
}

// query performs the SearXNG JSON request and decodes the response.
func (t *webSearchTool) query(query, category string) (*searxngResponse, error) {
	endpoint, err := url.Parse(strings.TrimRight(t.cfg.BaseURL, "/") + "/search")
	if err != nil {
		return nil, fmt.Errorf("invalid web_search base_url %q: %v", t.cfg.BaseURL, err)
	}
	q := endpoint.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if cat := strings.TrimSpace(category); cat != "" {
		q.Set("categories", cat)
	} else if t.cfg.Categories != "" {
		q.Set("categories", t.cfg.Categories)
	}
	if t.cfg.Language != "" {
		q.Set("language", t.cfg.Language)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	// Retry only on connection-refused — the precise signal that the SearXNG
	// sidecar is up as a container but not yet accepting connections (the
	// startup race when both come up together under compose). Other errors
	// (timeouts, DNS, genuine "down") fail fast on the first attempt.
	var httpResp *http.Response
	for attempt := 0; ; attempt++ {
		httpResp, err = t.client.Do(req)
		if err == nil || attempt >= searxngConnectRetries || !errors.Is(err, syscall.ECONNREFUSED) {
			break
		}
		time.Sleep(searxngRetryDelay)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot reach SearXNG at %s — is the service running? (%v)", t.cfg.BaseURL, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("SearXNG returned 403 for format=json — enable the JSON API in settings.yml (search.formats must include \"json\")")
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SearXNG returned HTTP %d", httpResp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxSearXNGBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %v", err)
	}

	var resp searxngResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode SearXNG JSON (got %d bytes): %v", len(body), err)
	}
	return &resp, nil
}

// Ensure webSearchTool implements odek.Tool
var _ odek.Tool = (*webSearchTool)(nil)
