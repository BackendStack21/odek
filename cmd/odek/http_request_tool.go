package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

// httpRequestTool checks one URL without exposing response bodies to the model.
// Independent requests are scheduled by the loop, under its concurrency limit.
type httpRequestTool struct {
	ctxTool
	dangerousConfig danger.DangerousConfig
	client          *http.Client
}

type httpRequestArgs struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type httpRequestResult struct {
	URL           string `json:"url"`
	Status        int    `json:"status"`
	ContentLength int64  `json:"content_length,omitempty"`
	Error         string `json:"error,omitempty"`
}

func newHTTPRequestTool(dc danger.DangerousConfig) *httpRequestTool {
	t := &httpRequestTool{dangerousConfig: dc}
	t.client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: t.checkRedirect, Transport: ssrfGuardedTransport()}
	return t
}

func (t *httpRequestTool) Name() string { return "http_request" }
func (t *httpRequestTool) Description() string {
	return "Check one HTTP(S) URL and return its status, content length, and error. Response bodies are discarded; use browser for page content. Emit separate calls for independent URL checks. Optional method (default GET) and headers are supported."
}
func (t *httpRequestTool) Schema() any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"url":     map[string]any{"type": "string", "description": "HTTP(S) URL to check."},
		"method":  map[string]any{"type": "string", "description": "HTTP method (default GET)."},
		"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	}, "required": []string{"url"}}
}

func (t *httpRequestTool) Effects(args string) odek.ToolEffects {
	var input httpRequestArgs
	if json.Unmarshal([]byte(args), &input) != nil {
		return odek.ToolEffects{Unknown: true}
	}
	switch strings.ToUpper(input.Method) {
	case "", "GET", "HEAD", "OPTIONS":
		return odek.ToolEffects{}
	default:
		return odek.ToolEffects{Unknown: true}
	}
}

func (t *httpRequestTool) CallContext(ctx context.Context, args string) (string, error) {
	// Bind redirects and output boundaries to this invocation rather than a
	// shared mutable context, while reusing the transport's connection pool.
	client := *t.client
	call := &httpRequestTool{dangerousConfig: t.dangerousConfig, client: &client}
	call.SetContext(ctx)
	client.CheckRedirect = call.checkRedirect
	return call.Call(args)
}

func (t *httpRequestTool) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return t.checkURL(req.URL.String())
}

func (t *httpRequestTool) checkURL(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("url must be an absolute HTTP(S) URL")
	}
	return t.dangerousConfig.CheckOperation(danger.ToolOperation{Name: t.Name(), Resource: target, Risk: danger.ClassifyURL(target)}, nil)
}

func (t *httpRequestTool) Call(argsJSON string) (string, error) {
	var args httpRequestArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}
	if args.URL == "" {
		return jsonError("url is required; use one http_request call per URL")
	}
	result := httpRequestResult{URL: args.URL}
	if err := t.checkURL(args.URL); err != nil {
		result.Error = err.Error()
		return jsonResult(result)
	}
	method := strings.ToUpper(args.Method)
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(t.toolCtx(), method, args.URL, nil)
	if err != nil {
		return jsonError("invalid request: " + err.Error())
	}
	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "odek-http-request/0.1")
	resp, err := t.client.Do(req)
	if err != nil {
		result.Error = wrapUntrusted(t.toolCtx(), args.URL, err.Error())
		return jsonResult(result)
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	result.ContentLength = resp.ContentLength
	if result.ContentLength < 0 && method != http.MethodHead {
		n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		result.ContentLength = n
		if err != nil {
			result.Error = wrapUntrusted(t.toolCtx(), args.URL, err.Error())
		}
	}
	return jsonResult(result)
}
