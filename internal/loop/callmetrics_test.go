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

func TestCallMetrics_AppendWSFrameOmitsZeros(t *testing.T) {
	frame := map[string]any{"outputTokens": 50}
	CallMetrics{}.AppendWSFrame(frame)
	if len(frame) != 1 {
		t.Errorf("zero metrics must not add keys, got %v", frame)
	}
	CallMetrics{DurationMs: 100, OutputTokens: 20, TokensPerSecond: 200}.AppendWSFrame(frame)
	if frame["outputTokens"] != 50 {
		t.Errorf("cumulative outputTokens overwritten: %v", frame["outputTokens"])
	}
	if frame["callDurationMs"] != int64(100) || frame["callOutputTokens"] != 20 || frame["tokensPerSecond"] != 200.0 {
		t.Errorf("ws frame = %v", frame)
	}
	if _, ok := frame["ttftMs"]; ok {
		t.Error("zero ttftMs must be omitted")
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
	// GenerationMs is 0 when the rest of the stream finishes in under 1ms
	// (single tiny chunk). The rate is omitted rather than invented.
}
