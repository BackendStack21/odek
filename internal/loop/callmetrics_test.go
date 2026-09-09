package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/tool"
)

func TestTokensPerSecond_OmitsNoisyRates(t *testing.T) {
	if got := tokensPerSecond(100, 0); got != 0 {
		t.Errorf("zero duration = %v, want 0", got)
	}
	if got := tokensPerSecond(0, 1000); got != 0 {
		t.Errorf("zero tokens = %v, want 0", got)
	}
	if got := tokensPerSecond(10, minRateDurationMs-1); got != 0 {
		t.Errorf("sub-floor duration = %v, want 0", got)
	}
}

func TestTokensPerSecond_RoundsOneDecimal(t *testing.T) {
	// 78 tokens / 8100ms = 9.629... → 9.6
	if got := tokensPerSecond(78, 8100); got != 9.6 {
		t.Errorf("78/8.1s = %v, want 9.6", got)
	}
	if got := tokensPerSecond(100, 1000); got != 100 {
		t.Errorf("100/1s = %v, want 100", got)
	}
}

func TestTokensPerSecond_FloorInclusive(t *testing.T) {
	if got := tokensPerSecond(5, minRateDurationMs); got != 100 {
		t.Errorf("exactly 50ms, 5 tokens = %v, want 100", got)
	}
}

func TestCallMetrics_AppendSnakeOmitsZeros(t *testing.T) {
	data := map[string]any{"output_tokens": 50}
	CallMetrics{}.AppendEventData(data)
	if len(data) != 1 {
		t.Errorf("zero metrics must not add keys, got %v", data)
	}

	CallMetrics{
		DurationMs:                8100,
		TTFTMs:                    5000,
		GenerationMs:              3100,
		InputTokens:               18432,
		OutputTokens:              78,
		TokensPerSecond:           9.6,
		GenerationTokensPerSecond: 25.2,
	}.AppendEventData(data)

	want := map[string]any{
		"output_tokens":                50,
		"call_duration_ms":             int64(8100),
		"ttft_ms":                      int64(5000),
		"generation_ms":                int64(3100),
		"call_input_tokens":            18432,
		"call_output_tokens":           78,
		"tokens_per_second":            9.6,
		"generation_tokens_per_second": 25.2,
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("data[%q] = %#v, want %#v", k, data[k], v)
		}
	}
}

func TestCallMetrics_AppendNilMaps(t *testing.T) {
	CallMetrics{DurationMs: 100, OutputTokens: 4}.AppendEventData(nil)
	CallMetrics{DurationMs: 100, OutputTokens: 4}.AppendWSFrame(nil)
}

func TestCallMetrics_AppendWSFrameOmitsZeros(t *testing.T) {
	frame := map[string]any{"outputTokens": 50}
	CallMetrics{}.AppendWSFrame(frame)
	if len(frame) != 1 {
		t.Errorf("zero metrics must not add keys, got %v", frame)
	}
	CallMetrics{
		DurationMs:                8100,
		TTFTMs:                    5000,
		GenerationMs:              3100,
		InputTokens:               18432,
		OutputTokens:              78,
		TokensPerSecond:           9.6,
		GenerationTokensPerSecond: 25.2,
	}.AppendWSFrame(frame)
	if frame["outputTokens"] != 50 {
		t.Errorf("cumulative outputTokens overwritten: %v", frame["outputTokens"])
	}
	want := map[string]any{
		"outputTokens":              50,
		"callDurationMs":            int64(8100),
		"ttftMs":                    int64(5000),
		"generationMs":              int64(3100),
		"callInputTokens":           18432,
		"callOutputTokens":          78,
		"tokensPerSecond":           9.6,
		"generationTokensPerSecond": 25.2,
	}
	for k, v := range want {
		if frame[k] != v {
			t.Errorf("frame[%q] = %#v, want %#v", k, frame[k], v)
		}
	}
}

func TestCallMetricsSnapshot_RoundTrip(t *testing.T) {
	info := IterationInfo{
		CallDurationMs:            8100,
		TTFTMs:                    5000,
		GenerationMs:              3100,
		CallInputTokens:           18432,
		CallOutputTokens:          78,
		TokensPerSecond:           9.6,
		GenerationTokensPerSecond: 25.2,
		OutputTokens:              500, // cumulative; must not leak into snapshot
		WindowTokens:              99,
	}
	got := info.CallMetricsSnapshot()
	want := CallMetrics{
		DurationMs:                8100,
		TTFTMs:                    5000,
		GenerationMs:              3100,
		InputTokens:               18432,
		OutputTokens:              78,
		TokensPerSecond:           9.6,
		GenerationTokensPerSecond: 25.2,
	}
	if got != want {
		t.Errorf("snapshot = %+v, want %+v", got, want)
	}
}

func TestElapsedMs_SubMillisecondRoundsUp(t *testing.T) {
	start := time.Now()
	if got := elapsedMs(start, start); got != 0 {
		t.Errorf("zero interval = %d, want 0", got)
	}
	if got := elapsedMs(start.Add(time.Millisecond), start); got != 0 {
		t.Errorf("inverted interval = %d, want 0", got)
	}
	if got := elapsedMs(start, start.Add(500*time.Microsecond)); got != 1 {
		t.Errorf("500µs = %d, want 1 (measured, not unknown)", got)
	}
	if got := elapsedMs(start, start.Add(5*time.Millisecond)); got != 5 {
		t.Errorf("5ms = %d, want 5", got)
	}
}

func TestEngine_LastCallMetrics_NilSafe(t *testing.T) {
	var e *Engine
	if got := e.LastCallMetrics(); got != (CallMetrics{}) {
		t.Errorf("nil engine LastCallMetrics = %+v", got)
	}
	if got := e.TotalLLMDuration(); got != 0 {
		t.Errorf("nil engine TotalLLMDuration = %d", got)
	}
	if got := e.ThinkTokensPerSecond(); got != 0 {
		t.Errorf("nil engine ThinkTokensPerSecond = %v", got)
	}
}

func TestEngine_RecordThinkCall_AccumulatesRunTotals(t *testing.T) {
	e := &Engine{}
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 80, OutputTokens: 16, InputTokens: 4})
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 120, OutputTokens: 24, InputTokens: 8})
	if e.TotalLLMDurationMs != 200 {
		t.Errorf("TotalLLMDurationMs = %d, want 200", e.TotalLLMDurationMs)
	}
	if e.TotalThinkOutputTokens != 40 {
		t.Errorf("TotalThinkOutputTokens = %d, want 40", e.TotalThinkOutputTokens)
	}
	if e.lastCall.InputTokens != 8 || e.lastCall.OutputTokens != 24 {
		t.Errorf("lastCall tokens = %d/%d, want 8/24", e.lastCall.InputTokens, e.lastCall.OutputTokens)
	}
	if e.ThinkTokensPerSecond() != 200 {
		t.Errorf("ThinkTokensPerSecond = %v, want 200 (40 tokens / 0.2s)", e.ThinkTokensPerSecond())
	}
}

func TestEngine_RecordThinkCall_NilSafe(t *testing.T) {
	var e *Engine
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 100, OutputTokens: 10})
	e.AppendRunLLMMetrics(map[string]any{})
	(&Engine{}).recordThinkCall(nil)
	(&Engine{}).AppendRunLLMMetrics(nil)
}

func TestEngine_RecordThinkCall_ZeroDurationDoesNotInflateRunRate(t *testing.T) {
	e := &Engine{}
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 0, OutputTokens: 1000, InputTokens: 4})
	if e.TotalLLMDurationMs != 0 {
		t.Errorf("TotalLLMDurationMs = %d, want 0", e.TotalLLMDurationMs)
	}
	if e.TotalThinkOutputTokens != 0 {
		t.Errorf("TotalThinkOutputTokens = %d, want 0 (tokens without duration must not enter the run rate)", e.TotalThinkOutputTokens)
	}
	if e.lastCall.OutputTokens != 1000 {
		t.Errorf("lastCall.OutputTokens = %d, want 1000 (this-call snapshot still records the call)", e.lastCall.OutputTokens)
	}
	if e.ThinkTokensPerSecond() != 0 {
		t.Errorf("ThinkTokensPerSecond = %v, want 0", e.ThinkTokensPerSecond())
	}
	data := map[string]any{}
	e.AppendRunLLMMetrics(data)
	if _, ok := data["llm_duration_ms"]; ok {
		t.Errorf("run metrics must omit llm_duration_ms when nothing was timed: %v", data)
	}
	if _, ok := data["tokens_per_second"]; ok {
		t.Errorf("run metrics must omit tokens_per_second when duration is unknown: %v", data)
	}
}

func TestEngine_AppendRunLLMMetrics(t *testing.T) {
	e := &Engine{}
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 80, OutputTokens: 16})
	e.recordThinkCall(&llmclient.CallResult{DurationMs: 120, OutputTokens: 24})
	data := map[string]any{"error_class": "tool_error"}
	e.AppendRunLLMMetrics(data)
	if data["llm_duration_ms"] != int64(200) {
		t.Errorf("llm_duration_ms = %v, want 200", data["llm_duration_ms"])
	}
	if data["tokens_per_second"] != 200.0 {
		t.Errorf("tokens_per_second = %v, want 200", data["tokens_per_second"])
	}
	if data["error_class"] != "tool_error" {
		t.Errorf("existing keys overwritten: %v", data)
	}
}

func TestEngine_WithCallMetrics_CopiesSnapshot(t *testing.T) {
	e := &Engine{lastCall: CallMetrics{DurationMs: 90, OutputTokens: 9, TokensPerSecond: 100}}
	got := e.withCallMetrics(IterationInfo{Turn: 2, OutputTokens: 50})
	if got.Turn != 2 || got.OutputTokens != 50 {
		t.Errorf("non-call fields clobbered: %+v", got)
	}
	if got.CallDurationMs != 90 || got.CallOutputTokens != 9 || got.TokensPerSecond != 100 {
		t.Errorf("call metrics not copied: %+v", got)
	}
	snap := got.CallMetricsSnapshot()
	if snap.DurationMs != 90 || snap.OutputTokens != 9 {
		t.Errorf("snapshot after withCallMetrics = %+v", snap)
	}
}

func delayedJSONServer(t *testing.T, delay time.Duration, promptTokens, completionTokens int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
			promptTokens, completionTokens)
	}))
}

func TestEngine_CallMetrics_BufferedThinkStep(t *testing.T) {
	server := delayedJSONServer(t, 60*time.Millisecond, 12, 40)
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry(nil), 5, "", nil, 0)

	var info IterationInfo
	engine.SetIterationCallback(func(i IterationInfo) { info = i })
	col := &eventCollector{}
	engine.SetEventHandler(col.handle)

	if _, err := engine.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if info.CallInputTokens != 12 || info.CallOutputTokens != 40 {
		t.Errorf("call tokens in=%d out=%d, want 12/40", info.CallInputTokens, info.CallOutputTokens)
	}
	if info.InputTokens != 12 || info.OutputTokens != 40 {
		t.Errorf("cumulative tokens in=%d out=%d, want 12/40", info.InputTokens, info.OutputTokens)
	}
	if info.CallDurationMs < minRateDurationMs {
		t.Errorf("CallDurationMs = %d, want >= %d", info.CallDurationMs, minRateDurationMs)
	}
	if info.TokensPerSecond <= 0 {
		t.Errorf("TokensPerSecond = %v, want > 0 on a 60ms call with output tokens", info.TokensPerSecond)
	}
	if info.TTFTMs != 0 || info.GenerationMs != 0 || info.GenerationTokensPerSecond != 0 {
		t.Errorf("buffered path must omit TTFT/generation: ttft=%d gen=%d gen_tps=%v",
			info.TTFTMs, info.GenerationMs, info.GenerationTokensPerSecond)
	}

	var iter events.Event
	found := false
	for _, ev := range col.all() {
		if ev.Type == events.TypeIterationCompleted {
			iter = ev
			found = true
		}
	}
	if !found {
		t.Fatal("missing iteration_completed")
	}
	if iter.Data["call_output_tokens"] != 40 {
		t.Errorf("event call_output_tokens = %v, want 40", iter.Data["call_output_tokens"])
	}
	if iter.Data["output_tokens"] != 40 {
		t.Errorf("event output_tokens (cumulative) = %v, want 40", iter.Data["output_tokens"])
	}
	if _, ok := iter.Data["tokens_per_second"]; !ok {
		t.Error("iteration_completed missing tokens_per_second")
	}
	if _, ok := iter.Data["ttft_ms"]; ok {
		t.Error("buffered iteration_completed must omit ttft_ms")
	}
}

func TestEngine_CallMetrics_StreamingTTFT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry(nil), 5, "", nil, 0)
	engine.SetStream(true)
	engine.SetDeltaHandler(func(llmclient.Delta) error { return nil })

	var info IterationInfo
	engine.SetIterationCallback(func(i IterationInfo) { info = i })

	if _, err := engine.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if info.TTFTMs < minRateDurationMs {
		t.Errorf("TTFTMs = %d, want >= %d (first delta delayed 60ms)", info.TTFTMs, minRateDurationMs)
	}
	if info.CallDurationMs < info.TTFTMs {
		t.Errorf("CallDurationMs %d < TTFTMs %d", info.CallDurationMs, info.TTFTMs)
	}
	if info.CallOutputTokens != 20 {
		t.Errorf("CallOutputTokens = %d, want 20", info.CallOutputTokens)
	}
	if info.TokensPerSecond <= 0 {
		t.Errorf("TokensPerSecond = %v, want > 0", info.TokensPerSecond)
	}
	// A single tiny chunk after TTFT may round generation to 1ms; the
	// rate stays omitted under the 50ms floor rather than invented.
}

func TestEngine_CallMetrics_StreamingGenerationRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry(nil), 5, "", nil, 0)
	engine.SetStream(true)
	engine.SetDeltaHandler(func(llmclient.Delta) error { return nil })

	var info IterationInfo
	engine.SetIterationCallback(func(i IterationInfo) { info = i })

	if _, err := engine.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if info.GenerationMs < minRateDurationMs {
		t.Errorf("GenerationMs = %d, want >= %d (second chunk delayed 80ms)", info.GenerationMs, minRateDurationMs)
	}
	if info.GenerationTokensPerSecond <= 0 {
		t.Errorf("GenerationTokensPerSecond = %v, want > 0", info.GenerationTokensPerSecond)
	}
}

func TestEngine_CallMetrics_ToolArgsDoNotStartTTFT(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		if call == 1 {
			time.Sleep(80 * time.Millisecond)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]}}]}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":8}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := testChatClient(t, server.URL)
	engine := New(client, tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "echo", description: "echo", output: "ok"},
	}), 5, "", nil, 0)
	engine.SetStream(true)
	engine.SetDeltaHandler(func(llmclient.Delta) error { return nil })

	var first IterationInfo
	engine.SetIterationCallback(func(i IterationInfo) {
		if first.CallDurationMs == 0 && i.CallDurationMs > 0 {
			first = i
		}
	})

	if _, err := engine.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if first.TTFTMs != 0 || first.GenerationMs != 0 {
		t.Errorf("tool-args-only think step must omit TTFT/generation: ttft=%d gen=%d", first.TTFTMs, first.GenerationMs)
	}
	if first.CallDurationMs < minRateDurationMs {
		t.Errorf("CallDurationMs = %d, want >= %d (tool-args delta delayed 80ms)", first.CallDurationMs, minRateDurationMs)
	}
}

func TestEngine_Run_ResetsCallMetrics(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		time.Sleep(60 * time.Millisecond)
		if call == 1 {
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"one"}}],"usage":{"prompt_tokens":12,"completion_tokens":40}}`)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"two"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	defer server.Close()

	engine := New(testChatClient(t, server.URL), tool.NewRegistry(nil), 5, "", nil, 0)
	if _, err := engine.Run(context.Background(), "first"); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	firstDur := engine.TotalLLMDurationMs
	if engine.LastCallMetrics().OutputTokens != 40 {
		t.Fatalf("run 1 lastCall output = %d, want 40", engine.LastCallMetrics().OutputTokens)
	}
	if firstDur < minRateDurationMs {
		t.Fatalf("run 1 duration = %d, want >= %d", firstDur, minRateDurationMs)
	}

	if _, err := engine.Run(context.Background(), "second"); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	got := engine.LastCallMetrics()
	if got.OutputTokens != 5 || got.InputTokens != 3 {
		t.Errorf("run 2 lastCall tokens = %d/%d, want 3/5", got.InputTokens, got.OutputTokens)
	}
	if engine.TotalThinkOutputTokens != 5 {
		t.Errorf("TotalThinkOutputTokens after run 2 = %d, want 5 (not accumulated across runs)", engine.TotalThinkOutputTokens)
	}
	if engine.TotalLLMDurationMs >= firstDur+minRateDurationMs {
		t.Errorf("TotalLLMDurationMs after run 2 = %d; must reset rather than accumulate onto run 1 (%d)", engine.TotalLLMDurationMs, firstDur)
	}
	if engine.TotalLLMDurationMs < minRateDurationMs {
		t.Errorf("TotalLLMDurationMs after run 2 = %d, want >= %d", engine.TotalLLMDurationMs, minRateDurationMs)
	}
}
