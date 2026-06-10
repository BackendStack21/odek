package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

// allowAll is a danger config that permits network egress without prompting,
// so the tool's gating doesn't block the hermetic test.
func allowAllDanger() danger.DangerousConfig {
	return danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{
		danger.NetworkEgress: danger.Allow,
	}}
}

// mockSearXNG returns a test server that serves a canned JSON SERP and records
// the last query it received.
func mockSearXNG(t *testing.T, results int) (*httptest.Server, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		lastQuery = r.URL.Query().Get("q")
		resp := map[string]any{"query": lastQuery}
		var rs []map[string]string
		for i := 0; i < results; i++ {
			rs = append(rs, map[string]string{
				"title":   "Result " + string(rune('A'+i)),
				"url":     "https://example.com/" + string(rune('a'+i)),
				"content": "snippet text",
				"engine":  "duckduckgo",
			})
		}
		resp["results"] = rs
		// SearXNG answers are objects with an "answer" field, not bare strings.
		resp["answers"] = []map[string]any{
			{"answer": "42", "url": nil, "template": "answer/legacy.html"},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func decodeWebSearch(t *testing.T, raw string) webSearchOutput {
	t.Helper()
	// Strip the untrusted_content wrapper to get at the JSON payload.
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		t.Fatalf("no JSON object found in output: %q", raw)
	}
	var out webSearchOutput
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		t.Fatalf("decode webSearchOutput: %v (raw=%q)", err, raw)
	}
	return out
}

func TestWebSearch_HappyPath(t *testing.T) {
	srv, lastQuery := mockSearXNG(t, 3)
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: srv.URL, MaxResults: 10})

	raw, err := tool.Call(`{"query":"golang generics"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(raw, "<untrusted_content_") {
		t.Errorf("output not wrapped as untrusted: %q", raw)
	}
	out := decodeWebSearch(t, raw)
	if out.Count != 3 || len(out.Results) != 3 {
		t.Fatalf("Count = %d, len = %d, want 3", out.Count, len(out.Results))
	}
	if out.Results[0].URL == "" || out.Results[0].Title == "" {
		t.Errorf("result missing fields: %+v", out.Results[0])
	}
	if len(out.Answers) != 1 || out.Answers[0] != "42" {
		t.Errorf("answers = %v, want [42]", out.Answers)
	}
	if *lastQuery != "golang generics" {
		t.Errorf("SearXNG received query %q, want %q", *lastQuery, "golang generics")
	}
}

func TestWebSearch_MaxResultsTruncation(t *testing.T) {
	srv, _ := mockSearXNG(t, 10)
	// config cap of 2; the request overrides to 4 — request wins.
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: srv.URL, MaxResults: 2})

	raw, err := tool.Call(`{"query":"x","max_results":4}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	out := decodeWebSearch(t, raw)
	if out.Count != 4 {
		t.Errorf("Count = %d, want 4 (request override)", out.Count)
	}

	// No request override → config cap applies.
	raw2, _ := tool.Call(`{"query":"x"}`)
	out2 := decodeWebSearch(t, raw2)
	if out2.Count != 2 {
		t.Errorf("Count = %d, want 2 (config cap)", out2.Count)
	}
}

func TestWebSearch_EmptyQuery(t *testing.T) {
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: "http://unused"})
	raw, err := tool.Call(`{"query":"   "}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(raw, "query is required") {
		t.Errorf("expected 'query is required', got %q", raw)
	}
}

func TestWebSearch_NotConfigured(t *testing.T) {
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{})
	raw, _ := tool.Call(`{"query":"x"}`)
	if !strings.Contains(raw, "not configured") {
		t.Errorf("expected 'not configured' error, got %q", raw)
	}
}

func TestWebSearch_JSONDisabled403(t *testing.T) {
	// Server that always 403s (simulates JSON format not enabled).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: srv.URL})

	raw, _ := tool.Call(`{"query":"x"}`)
	out := decodeWebSearch(t, raw)
	if !strings.Contains(out.Error, "search.formats") {
		t.Errorf("expected JSON-format hint in error, got %q", out.Error)
	}
}

func TestWebSearch_BackendUnreachable(t *testing.T) {
	// Connection-refused triggers the cold-start retry; shrink the delay so the
	// test exercises the retry path without the 1s production backoff.
	orig := searxngRetryDelay
	searxngRetryDelay = time.Millisecond
	t.Cleanup(func() { searxngRetryDelay = orig })

	// Point at a closed port on localhost.
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{BaseURL: "http://127.0.0.1:1"})
	raw, _ := tool.Call(`{"query":"x"}`)
	out := decodeWebSearch(t, raw)
	if !strings.Contains(out.Error, "cannot reach SearXNG") {
		t.Errorf("expected unreachable error, got %q", out.Error)
	}
}

func TestWebSearch_RedirectToInternalBlocked(t *testing.T) {
	// A compromised/misconfigured SearXNG that 302s toward an internal host must
	// be stopped by the CheckRedirect guard, not followed (SSRF defense).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	// Deny network egress so the redirect-hop re-classification blocks the hop.
	dc := danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{
		danger.NetworkEgress: danger.Allow, // initial query allowed
		danger.SystemWrite:   danger.Deny,  // internal/metadata target denied
	}}
	tool := newWebSearchTool(dc, config.WebSearchConfig{BaseURL: srv.URL})

	raw, _ := tool.Call(`{"query":"x"}`)
	out := decodeWebSearch(t, raw)
	if out.Error == "" {
		t.Fatalf("expected redirect to internal host to be blocked, got results: %+v", out)
	}
	if !strings.Contains(out.Error, "blocked") && !strings.Contains(out.Error, "cannot reach") {
		t.Errorf("expected a redirect-blocked error, got %q", out.Error)
	}
}

func TestWebSearch_DeniedByPolicy(t *testing.T) {
	// NetworkEgress denied → the tool returns the denial as an error result.
	dc := danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{
		danger.NetworkEgress: danger.Deny,
	}}
	tool := newWebSearchTool(dc, config.WebSearchConfig{BaseURL: "http://unused"})
	raw, _ := tool.Call(`{"query":"secret terms"}`)
	if !strings.Contains(raw, "denied") {
		t.Errorf("expected denial, got %q", raw)
	}
}

func TestWebSearch_SchemaShape(t *testing.T) {
	tool := newWebSearchTool(allowAllDanger(), config.WebSearchConfig{})
	schema, ok := tool.Schema().(map[string]any)
	if !ok {
		t.Fatal("schema is not a map")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("schema missing 'query' property")
	}
	req, _ := schema["required"].([]string)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("required = %v, want [query]", req)
	}
}
