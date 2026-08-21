package llm

// Streaming test matrix (docs/STREAMING.md §6). All cases run against
// httptest SSE servers — no network. T-numbers reference the matrix rows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sseServer serves raw SSE lines (each string is one wire line, sent with
// flushing so the client sees them incrementally).
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, l := range lines {
			fmt.Fprintln(w, l)
			flusher.Flush()
		}
	}))
}

// bufferedServer serves a complete non-streaming completion body.
func bufferedServer(t *testing.T, body string, hits *atomic.Int32, inspect func(streamReq bool)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		if inspect != nil {
			inspect(req.Stream)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
}

func dataLine(payload string) string { return "data: " + payload }

func chunk(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return dataLine(string(b))
}

// T1: the canonical shape — reasoning → content → tool call → usage on the
// finish chunk → [DONE]. CallStream must return the same CallResult the
// buffered path returns for the equivalent complete body.
func TestStream_ResultEqualsBuffered(t *testing.T) {
	usage := `"usage":{"prompt_tokens":14,"completion_tokens":9,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}`
	sseLines := []string{
		`: openai-style keepalive`,
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "reasoning_content": "think "}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "hard"}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Hello"}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": " world"}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": nil}}}}),
		`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":""}}],` + usage + `}`,
		"data: [DONE]",
	}
	ts := sseServer(t, sseLines)
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	var deltas []Delta
	res, err := c.CallStream(context.Background(), nil, nil, nil, func(d Delta) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}

	if res.Content != "Hello world" {
		t.Errorf("Content = %q, want %q", res.Content, "Hello world")
	}
	if res.ReasoningContent != "think hard" {
		t.Errorf("ReasoningContent = %q, want %q", res.ReasoningContent, "think hard")
	}
	if res.InputTokens != 14 || res.OutputTokens != 9 {
		t.Errorf("tokens = %d/%d, want 14/9", res.InputTokens, res.OutputTokens)
	}
	if res.CachedTokens != 4 || !res.CacheReported {
		t.Errorf("cached = %d reported=%v, want 4/true", res.CachedTokens, res.CacheReported)
	}
	if len(res.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want none", res.ToolCalls)
	}
	if len(deltas) != 4 { // 2 reasoning + 2 content; null content emits nothing
		t.Errorf("deltas = %d, want 4: %+v", len(deltas), deltas)
	}
	if deltas[0].Kind != DeltaReasoning || deltas[2].Kind != DeltaContent {
		t.Errorf("delta kinds out of order: %+v", deltas)
	}
}

// T2: OpenAI dialect — usage arrives in a separate chunk with empty choices
// (only sent because stream_options.include_usage was set).
func TestStream_UsageOnlyChunk(t *testing.T) {
	lines := []string{
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "OK"}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "delta": map[string]any{}}}}),
		`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1}}`,
		"data: [DONE]",
	}
	ts := sseServer(t, lines)
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if res.InputTokens != 7 || res.OutputTokens != 1 {
		t.Errorf("tokens = %d/%d, want 7/1", res.InputTokens, res.OutputTokens)
	}
}

// T4: tool arguments split across fragments and two calls interleaved by
// index; ids/names arrive on the first fragment per index.
func TestStream_ToolCallAssembly(t *testing.T) {
	mk := func(idx int, id, name, args string, extra map[string]any) string {
		fn := map[string]any{"arguments": args}
		if name != "" {
			fn["name"] = name
		}
		tc := map[string]any{"index": idx, "type": "function", "function": fn}
		if id != "" {
			tc["id"] = id
		}
		return chunk(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{tc}}}}})
	}
	lines := []string{
		mk(0, "call-a", "get_weather", `{"ci`, nil),
		mk(1, "call-b", "get_time", `{"tz`, nil),
		mk(0, "", "", `ty":`, nil),
		mk(1, "", "", `":"UTC"}`, nil),
		mk(0, "", "", `"Havana"}`, nil),
		`data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`,
		"data: [DONE]",
	}
	ts := sseServer(t, lines)
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %d, want 2", len(res.ToolCalls))
	}
	a, b := res.ToolCalls[0], res.ToolCalls[1]
	if a.ID != "call-a" || a.Function.Name != "get_weather" || a.Function.Arguments != `{"city":"Havana"}` {
		t.Errorf("tool[0] = %+v", a)
	}
	if b.ID != "call-b" || b.Function.Name != "get_time" || b.Function.Arguments != `{"tz":"UTC"}` {
		t.Errorf("tool[1] = %+v", b)
	}
	if res.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", res.OutputTokens)
	}
}

// T6: a handler error mid-stream aborts with StreamAbortedError and returns
// the partial result.
func TestStream_HandlerAbort(t *testing.T) {
	lines := []string{
		chunk(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "part1 "}}}}),
		chunk(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "part2"}}}}),
		"data: [DONE]",
	}
	ts := sseServer(t, lines)
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, func(d Delta) error {
		if d.Text == "part2" {
			return errors.New("user pressed escape")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected abort error")
	}
	var abort *StreamAbortedError
	if !errors.As(err, &abort) {
		t.Fatalf("error = %v, want *StreamAbortedError", err)
	}
	if !strings.Contains(err.Error(), "user pressed escape") {
		t.Errorf("abort reason missing: %v", err)
	}
	if res == nil || res.Content != "part1 part2" {
		t.Errorf("partial Content = %+v, want assembled partial", res)
	}
}

// T7: a 400 naming stream_options triggers the field-drop retry — the
// second attempt streams without the field and succeeds.
func TestStream_StreamOptionsDropped(t *testing.T) {
	var hits atomic.Int32
	var sawOptions atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		body := make([]byte, 4096)
		nr, _ := r.Body.Read(body)
		raw := string(body[:nr])
		if strings.Contains(raw, `"stream_options"`) {
			sawOptions.Store(true)
		}
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'stream_options'."}}`)
			return
		}
		if sawOptions.Load() && !strings.Contains(raw, `"stream_options"`) {
			// second request dropped the field
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n")
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if res.Content != "OK" {
		t.Errorf("Content = %q, want OK", res.Content)
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2 (original + field-dropped retry)", hits.Load())
	}
	if !c.dropStreamOptions.Load() {
		t.Error("dropStreamOptions not learned")
	}
}

// T8: a 400 naming stream (provider cannot stream) falls back to the
// buffered path transparently, and the fallback is learned.
func TestStream_BufferedFallback(t *testing.T) {
	var hits atomic.Int32
	var streamReqs atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 8192)
		nr, _ := r.Body.Read(body)
		raw := string(body[:nr])
		if strings.Contains(raw, `"stream":true`) {
			streamReqs.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Streaming is not supported for this model."}}`)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"buffered ok","reasoning_content":"why"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if res.Content != "buffered ok" || res.ReasoningContent != "why" || res.InputTokens != 3 {
		t.Errorf("fallback result = %+v", res)
	}
	if !c.forceBuffered.Load() {
		t.Error("forceBuffered not learned")
	}

	// A later CallStream goes straight to the buffered path (no new stream
	// request).
	before := streamReqs.Load()
	res, err = c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil || res.Content != "buffered ok" {
		t.Fatalf("second CallStream: %v %+v", err, res)
	}
	if streamReqs.Load() != before {
		t.Errorf("learned fallback re-attempted streaming (%d -> %d)", before, streamReqs.Load())
	}
}

// T8b: a 200 whose body is not SSE also falls back to buffered.
func TestStream_NonSSEBodyFallsBack(t *testing.T) {
	ts := bufferedServer(t, `{"choices":[{"message":{"content":"plain json"}}]}`, nil, func(streamReq bool) {
		if !streamReq {
			t.Errorf("first request should have asked for streaming")
		}
	})
	defer ts.Close()
	// First handler must answer the stream request with a non-SSE body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 8192)
		nr, _ := r.Body.Read(body)
		if strings.Contains(string(body[:nr]), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"plain json"}}]}`) // 200, not SSE
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"plain json"}}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if res.Content != "plain json" {
		t.Errorf("Content = %q, want %q", res.Content, "plain json")
	}
	if !c.forceBuffered.Load() {
		t.Error("forceBuffered not learned for non-SSE body")
	}
}

// T9: a trickling server (chunks forever, faster than the idle watchdog)
// must be stopped by the hard wall-clock deadline, not run unbounded.
func TestStream_TrickleStoppedByDeadline(t *testing.T) {
	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		flusher.Flush()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
				flusher.Flush()
			}
		}
	}))
	defer func() { close(stop); ts.Close() }()

	c := New(ts.URL, "k", "m", "", 0, 400*time.Millisecond)
	start := time.Now()
	_, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected deadline error for trickling stream")
	}
	if elapsed > 3*time.Second {
		t.Errorf("elapsed = %v, want the hard deadline (~400ms), not unbounded", elapsed)
	}
}

// T10: silence after the first delta trips the idle watchdog (not the
// wall-clock deadline).
func TestStream_IdleWatchdog(t *testing.T) {
	orig := streamIdleTimeout
	streamIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = orig })

	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-stop // then silence
	}))
	defer func() { close(stop); ts.Close() }()

	c := New(ts.URL, "k", "m", "", 0, 30*time.Second)
	start := time.Now()
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected idle-watchdog error")
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Errorf("error = %v, want idle-timeout mention", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, want watchdog (~150ms)", elapsed)
	}
	if res == nil || res.Content != "first" {
		t.Errorf("partial result = %+v, want first fragment kept", res)
	}
}

// T11: billing 429s fast-fail on the streaming path too — exactly one
// request, no retries.
func TestStream_Billing429FailsFast(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`)
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "glm-5.3", "high", 0, 5*time.Second)
	_, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected billing error")
	}
	if !strings.Contains(err.Error(), "Insufficient balance") || !strings.Contains(err.Error(), "not retried") {
		t.Errorf("error = %v, want billing fast-fail", err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}
}

// T13: no usage anywhere (local-server shape) — valid result, zero tokens.
func TestStream_NoUsage(t *testing.T) {
	lines := []string{
		chunk(t, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}}}),
		`data: {"choices":[{"finish_reason":"stop","delta":{}}]}`,
		"data: [DONE]",
	}
	ts := sseServer(t, lines)
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CallStream: %v", err)
	}
	if res.Content != "ok" || res.InputTokens != 0 || res.OutputTokens != 0 {
		t.Errorf("result = %+v, want ok/0/0", res)
	}
}

// T14: malformed JSON after deltas have been emitted is terminal (no
// silent retry that would duplicate the partial output); the partial result
// is returned alongside the error.
func TestStream_MalformedMidStreamTerminal(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"good \"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {not json\n\n")
		w.(http.Flusher).Flush()
	}))
	defer ts.Close()

	c := New(ts.URL, "k", "m", "", 0, 5*time.Second)
	res, err := c.CallStream(context.Background(), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected mid-stream parse error")
	}
	if res == nil || res.Content != "good " {
		t.Errorf("partial = %+v, want assembled prefix", res)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 (mid-stream failures must not retry after emitting)", hits.Load())
	}
}
