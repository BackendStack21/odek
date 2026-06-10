package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// maxSearXNGBody caps the response body read from SearXNG (defensive — a
// metasearch JSON payload is small; this guards against a misbehaving backend).
const maxSearXNGBody = 4 << 20 // 4 MiB

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
	return &webSearchTool{
		dangerousConfig: dc,
		cfg:             cfg,
		client:          &http.Client{Timeout: time.Duration(timeout) * time.Second},
	}
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
	for _, a := range resp.Answers {
		if s := strings.TrimSpace(string(a)); s != "" && s != "null" {
			out.Answers = append(out.Answers, strings.Trim(s, `"`))
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

	httpResp, err := t.client.Do(req)
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
