package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
)

type requestRoundTripper func(*http.Request) (*http.Response, error)

func (f requestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPRequestSingleRequestMetadata(t *testing.T) {
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	tool.client.Transport = requestRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != "HEAD" || req.Header.Get("Accept") != "text/plain" {
			t.Errorf("request lost method or headers: %v", req)
		}
		return &http.Response{StatusCode: 204, ContentLength: 42, Body: io.NopCloser(strings.NewReader("hidden response body")), Header: make(http.Header)}, nil
	})
	output, err := tool.Call(`{"url":"https://example.com","method":"HEAD","headers":{"Accept":"text/plain"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var result httpRequestResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != 204 || result.ContentLength != 42 || strings.Contains(output, "hidden response body") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestHTTPRequestUnsafeMethodsAreOrderingBarriers(t *testing.T) {
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "DELETE", "PATCH"} {
		effects := tool.Effects(fmt.Sprintf(`{"url":"https://example.com","method":%q}`, method))
		want := method == "POST" || method == "DELETE" || method == "PATCH"
		if effects.Unknown != want {
			t.Errorf("%s unknown effects = %v, want %v", method, effects.Unknown, want)
		}
	}
}

// Exercise the public adapter and real native tool, not a context-free fixture.
func TestNativeHTTPRequestCallsShareLoopConcurrencyLimit(t *testing.T) {
	var modelCalls atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelCalls.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"http_request","arguments":"{\"url\":\"https://example.com/a\"}"}},{"id":"b","type":"function","function":{"name":"http_request","arguments":"{\"url\":\"https://example.com/b\"}"}},{"id":"c","type":"function","function":{"name":"http_request","arguments":"{\"url\":\"https://example.com/c\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
		}
	}))
	defer llm.Close()
	tool := newHTTPRequestTool(danger.DangerousConfig{})
	entered, release := make(chan struct{}, 3), make(chan struct{})
	var active, peak, requests atomic.Int32
	tool.client.Transport = requestRoundTripper(func(req *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		requests.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: 200, ContentLength: 0, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	agent, err := odek.New(odek.Config{Provider: "deepseek", BaseURL: llm.URL, APIKey: "test-key", Model: "test-model", SystemMessage: "Check URLs.", NoProjectFile: true, MemoryDir: t.TempDir(), MaxIterations: 3, ContextWindow: 8192, MaxToolParallel: 2, Tools: []odek.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := agent.Run(ctx, "check three URLs"); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			select {
			case err := <-done:
				t.Fatalf("requests did not overlap: requests=%d model_calls=%d run_error=%v", requests.Load(), modelCalls.Load(), err)
			default:
				t.Fatalf("requests did not overlap: requests=%d model_calls=%d", requests.Load(), modelCalls.Load())
			}
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || peak.Load() != 2 {
		t.Fatalf("requests=%d peak=%d, want 3 requests and concurrency 2", requests.Load(), peak.Load())
	}
}

func TestSingleFileUtilitiesRejectBatchInputs(t *testing.T) {
	for _, tool := range []odek.Tool{&checksumTool{}, &headTailTool{}} {
		output, err := tool.Call(`{"files":[{"path":"README.md"}]}`)
		if err == nil || !strings.Contains(output, "path is required") {
			t.Errorf("%s accepted a batch: %s (%v)", tool.Name(), output, err)
		}
	}
}
