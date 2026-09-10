package loop

import (
	"math"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
)

// minRateDurationMs is the floor below which tokens-per-second is omitted.
// Sub-50ms calls (mock servers, cached empty completions) produce noisy rates.
const minRateDurationMs int64 = 50

// CallMetrics is the last main think-step LLM call's timing and derived rates.
// Zero-valued fields mean "not measured": the buffered path has no TTFT,
// and rates stay 0 when the provider reported no output tokens or the
// call was shorter than minRateDurationMs. Side calls (compaction, titles,
// progress summaries) never update these fields.
type CallMetrics struct {
	DurationMs                int64
	TTFTMs                    int64
	GenerationMs              int64
	InputTokens               int
	OutputTokens              int
	TokensPerSecond           float64
	GenerationTokensPerSecond float64
}

// elapsedMs is the wall time from start to end in whole milliseconds.
// A positive interval shorter than 1ms rounds up to 1 so a measured
// instant is not stored as 0 (which Append* treats as "unknown").
func elapsedMs(start, end time.Time) int64 {
	d := end.Sub(start)
	if d <= 0 {
		return 0
	}
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 1
}

// tokensPerSecond is output tokens divided by duration. Returns 0 when the
// rate would be meaningless (no tokens, or a sub-floor duration).
func tokensPerSecond(tokens int, durationMs int64) float64 {
	if tokens <= 0 || durationMs < minRateDurationMs {
		return 0
	}
	v := float64(tokens) / (float64(durationMs) / 1000.0)
	return math.Round(v*10) / 10
}

// AppendEventData writes non-zero call metrics onto an odek.event/v1 data map
// (snake_case). Existing keys are left alone; zeros are omitted so old
// consumers that treat missing as unknown keep working.
func (m CallMetrics) AppendEventData(data map[string]any) {
	if data == nil {
		return
	}
	if m.DurationMs > 0 {
		data["call_duration_ms"] = m.DurationMs
	}
	if m.TTFTMs > 0 {
		data["ttft_ms"] = m.TTFTMs
	}
	if m.GenerationMs > 0 {
		data["generation_ms"] = m.GenerationMs
	}
	if m.InputTokens > 0 {
		data["call_input_tokens"] = m.InputTokens
	}
	if m.OutputTokens > 0 {
		data["call_output_tokens"] = m.OutputTokens
	}
	if m.TokensPerSecond > 0 {
		data["tokens_per_second"] = m.TokensPerSecond
	}
	if m.GenerationTokensPerSecond > 0 {
		data["generation_tokens_per_second"] = m.GenerationTokensPerSecond
	}
}

// AppendWSFrame writes the same non-zero fields in protocol-v2 WebSocket
// camelCase (usage / done frames). outputTokens on those frames stays
// run-cumulative; these keys are this-call only.
func (m CallMetrics) AppendWSFrame(data map[string]any) {
	if data == nil {
		return
	}
	if m.DurationMs > 0 {
		data["callDurationMs"] = m.DurationMs
	}
	if m.TTFTMs > 0 {
		data["ttftMs"] = m.TTFTMs
	}
	if m.GenerationMs > 0 {
		data["generationMs"] = m.GenerationMs
	}
	if m.InputTokens > 0 {
		data["callInputTokens"] = m.InputTokens
	}
	if m.OutputTokens > 0 {
		data["callOutputTokens"] = m.OutputTokens
	}
	if m.TokensPerSecond > 0 {
		data["tokensPerSecond"] = m.TokensPerSecond
	}
	if m.GenerationTokensPerSecond > 0 {
		data["generationTokensPerSecond"] = m.GenerationTokensPerSecond
	}
}

// CallMetricsSnapshot copies this-call fields off an IterationInfo.
func (info IterationInfo) CallMetricsSnapshot() CallMetrics {
	return CallMetrics{
		DurationMs:                info.CallDurationMs,
		TTFTMs:                    info.TTFTMs,
		GenerationMs:              info.GenerationMs,
		InputTokens:               info.CallInputTokens,
		OutputTokens:              info.CallOutputTokens,
		TokensPerSecond:           info.TokensPerSecond,
		GenerationTokensPerSecond: info.GenerationTokensPerSecond,
	}
}

// LastCallMetrics returns a copy of the last main think-step call metrics.
func (e *Engine) LastCallMetrics() CallMetrics {
	if e == nil {
		return CallMetrics{}
	}
	return e.lastCall
}

// TotalLLMDuration is the sum of main think-step call durations this run.
// Side calls are excluded. Reset on each Run / RunWithMessages.
func (e *Engine) TotalLLMDuration() int64 {
	if e == nil {
		return 0
	}
	return e.TotalLLMDurationMs
}

// ThinkTokensPerSecond is run-level end-to-end throughput: think-step
// output tokens over summed think-step LLM duration. 0 when unknown.
func (e *Engine) ThinkTokensPerSecond() float64 {
	if e == nil {
		return 0
	}
	return tokensPerSecond(e.TotalThinkOutputTokens, e.TotalLLMDurationMs)
}

func (e *Engine) recordThinkCall(res *llmclient.CallResult) {
	if e == nil || res == nil {
		return
	}
	m := CallMetrics{
		DurationMs:                res.DurationMs,
		TTFTMs:                    res.TTFTMs,
		GenerationMs:              res.GenerationMs,
		InputTokens:               res.InputTokens,
		OutputTokens:              res.OutputTokens,
		TokensPerSecond:           tokensPerSecond(res.OutputTokens, res.DurationMs),
		GenerationTokensPerSecond: tokensPerSecond(res.OutputTokens, res.GenerationMs),
	}
	e.lastCall = m
	// Run-level rate is think-step tokens over think-step duration. A
	// sub-millisecond call (DurationMs == 0) must not add tokens without
	// duration — that inflates tokens_per_second on run_completed.
	if res.DurationMs > 0 {
		e.TotalLLMDurationMs += res.DurationMs
		e.TotalThinkOutputTokens += res.OutputTokens
	}
}

func (e *Engine) withCallMetrics(info IterationInfo) IterationInfo {
	m := e.lastCall
	info.CallDurationMs = m.DurationMs
	info.TTFTMs = m.TTFTMs
	info.GenerationMs = m.GenerationMs
	info.CallInputTokens = m.InputTokens
	info.CallOutputTokens = m.OutputTokens
	info.TokensPerSecond = m.TokensPerSecond
	info.GenerationTokensPerSecond = m.GenerationTokensPerSecond
	return info
}

func (e *Engine) emitIterationCompleted(iteration, toolsCalled int) {
	data := map[string]any{
		"input_tokens":  e.TotalInputTokens,
		"output_tokens": e.TotalOutputTokens,
		"tools_called":  toolsCalled,
		"budget":        e.BudgetSnapshot(),
	}
	e.lastCall.AppendEventData(data)
	e.emitEvent(events.Event{
		Type:      events.TypeIterationCompleted,
		Iteration: iteration,
		Data:      data,
	})
}

// AppendRunLLMMetrics writes run-level think-step duration and throughput
// onto an event data map (run_completed / run_failed). Zeros are omitted.
func (e *Engine) AppendRunLLMMetrics(data map[string]any) {
	if e == nil || data == nil {
		return
	}
	if ms := e.TotalLLMDuration(); ms > 0 {
		data["llm_duration_ms"] = ms
	}
	if tps := e.ThinkTokensPerSecond(); tps > 0 {
		data["tokens_per_second"] = tps
	}
}
