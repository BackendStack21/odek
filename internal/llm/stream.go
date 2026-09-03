package llm

// Streaming support: CallStream delivers OpenAI-compatible SSE responses
// incrementally while assembling the same CallResult the buffered path
// returns. Design contract (docs/STREAMING.md):
//
//   - ADR-1: hard wall-clock deadline over the whole stream (context) plus
//     an idle watchdog between SSE events; the no-deadline pooled client is
//     used because http.Client.Timeout would kill the body read mid-stream.
//   - ADR-3: the delta callback may return an error to abort the stream;
//     CallStream then returns *StreamAbortedError.
//   - ADR-4: two learn-once fallbacks — drop stream_options (field-level),
//     fall back to the buffered path (path-level) when the provider rejects
//     streaming or answers a non-SSE body.
//   - Retries only happen before the first delta is emitted to the consumer,
//     so partial output is never duplicated by a silent full retry.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DeltaKind discriminates streamed fragments.
type DeltaKind int

const (
	// DeltaReasoning is a reasoning/thinking fragment (reasoning_content),
	// emitted before content on thinking models.
	DeltaReasoning DeltaKind = iota
	// DeltaContent is an assistant text fragment.
	DeltaContent
	// DeltaToolArgs is a tool-call argument fragment (partial JSON). The
	// engine suppresses these by default; they exist for consumers that
	// render live tool invocations.
	DeltaToolArgs
)

// Delta is one streamed fragment. Text is the concatenated fragment for
// this event, not the accumulated text.
type Delta struct {
	Kind DeltaKind
	Text string
}

// StreamAbortedError is returned by CallStream when the delta handler
// aborted generation. It wraps the handler's error and carries the partial
// result assembled so far via the CallStream return values.
type StreamAbortedError struct {
	Reason error
}

func (e *StreamAbortedError) Error() string {
	return fmt.Sprintf("llm: stream aborted by consumer: %v", e.Reason)
}

func (e *StreamAbortedError) Unwrap() error { return e.Reason }

// streamIdleTimeout bounds the silence between SSE events (keepalive
// comments reset it). Package var so tests can shorten it.
// streamIdleTimeout is the SSE idle watchdog: the time between events
// (keepalive comment lines count) before the stream is dropped and retried.
// Thinking models can legitimately spend minutes before their first event,
// so the default is generous (120s) and operator-configurable via
// llm.stream_idle_timeout_seconds / ODEK_STREAM_IDLE_TIMEOUT_SECONDS
// (see SetStreamIdleTimeout).
var streamIdleTimeout = 120 * time.Second

// SetStreamIdleTimeout overrides the SSE idle watchdog. Call at startup,
// before the first request; non-positive values are ignored.
func SetStreamIdleTimeout(d time.Duration) {
	if d > 0 {
		streamIdleTimeout = d
	}
}

// StreamIdleTimeout reports the active idle watchdog (introspection/tests).
func StreamIdleTimeout() time.Duration {
	return streamIdleTimeout
}

// CallStream sends a chat completion request with stream:true and delivers
// fragments to cb as they arrive, returning the fully assembled result —
// identical to what Call returns for the same logical response. cb is
// invoked synchronously from the reader; it must be non-blocking (same
// contract as loop.SignalHandler). Returning a non-nil error from cb aborts
// the stream and yields a *StreamAbortedError. A nil cb is allowed (assemble
// only). See the package comment for the timeout and fallback contract.
func (c *Client) CallStream(ctx context.Context, messages []Message, systemBlocks []SystemBlock, tools []ToolDef, cb func(Delta) error) (*CallResult, error) {
	if cb == nil {
		cb = func(Delta) error { return nil }
	}

	// ADR-1: hard wall-clock cap over the whole stream. Respect a caller
	// deadline if one is already set.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.requestTimeout())
		defer cancel()
	}

	// Path-level learn-once fallback (ADR-4): a provider that already told
	// us it cannot stream goes straight to the buffered path.
	if c.forceBuffered.Load() {
		return c.Call(ctx, messages, systemBlocks, tools)
	}

	body := c.buildCallParams(messages, systemBlocks, tools)
	body.Stream = true
	if !c.dropStreamOptions.Load() {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	reqBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}

	res, emitted, err := c.postChatStream(ctx, reqBytes, cb)

	// Field-level learn-once retry (ADR-4): strict providers 400 on the
	// OpenAI-only stream_options field. Drop it and re-stream once.
	if err != nil && !emitted && !c.dropStreamOptions.Load() && errorNamesParam(err, "stream_options") {
		c.dropStreamOptions.Store(true)
		body.StreamOptions = nil
		if reqBytes, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("llm: marshal request: %w", err)
		}
		res, emitted, err = c.postChatStream(ctx, reqBytes, cb)
	}

	// Field-level learn-once retry (ADR-4, parity with buffered Call):
	// some models reject reasoning_effort combined with function tools.
	// Learn the constraint once (buildCallParams then pins effort to
	// "none" for later tool-bearing streams) and re-stream once
	// (B3-LLM-1).
	if err != nil && !emitted && len(tools) > 0 && !c.forceNoneEffort.Load() && reasoningEffortRejected(err) {
		c.forceNoneEffort.Store(true)
		body.ReasoningEffort = "none"
		if reqBytes, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("llm: marshal request: %w", err)
		}
		res, emitted, err = c.postChatStream(ctx, reqBytes, cb)
	}

	// Path-level learn-once fallback (ADR-4): the provider rejects streaming
	// outright, or answered 200 with a non-SSE body, and nothing was emitted
	// yet — transparently use the buffered path for this and later calls.
	if err != nil && !emitted && (errorNamesParam(err, "stream") || errors.Is(err, errNotSSE)) {
		c.forceBuffered.Store(true)
		return c.Call(ctx, messages, systemBlocks, tools)
	}

	return res, err
}

// errNotSSE marks a 200 response whose body is not an SSE stream (e.g. a
// gateway answering with a plain JSON completion). Triggers the buffered
// fallback when no delta has been emitted yet.
var errNotSSE = errors.New("llm: response is not an SSE stream")

// errorNamesParam reports whether err is a 400 whose body names the given
// request parameter as the offending one. Providers quote the parameter
// differently ('stream_options' at OpenAI, "stream" inside a JSON body), and
// some phrase whole-parameter rejection as "Streaming is not supported" —
// all forms count.
func errorNamesParam(err error, param string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") {
		return false
	}
	return strings.Contains(msg, "'"+param+"'") ||
		strings.Contains(msg, `"`+param+`"`) ||
		strings.Contains(msg, param+"ing is not supported")
}

// postChatStream POSTs the streaming request and reads the SSE response,
// retrying transient failures exactly like the buffered path — but only
// while no delta has been emitted to the consumer (after that, a silent
// full retry would duplicate output). Returns the assembled result (partial
// on mid-stream errors), whether any delta was emitted, and the error.
func (c *Client) postChatStream(ctx context.Context, reqBytes []byte, cb func(Delta) error) (*CallResult, bool, error) {
	url := c.BaseURL + "/chat/completions"

	const maxRetries = 7
	var lastErr error
	var lastStatus int
	var lastBody string
	var wait time.Duration

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := retrySleep(ctx, wait); err != nil {
				return nil, false, err
			}
		}
		wait = time.Duration(1<<attempt) * time.Second
		if wait > maxRetryBackoff {
			wait = maxRetryBackoff
		}
		wait = jitterBackoff(wait)

		// The request context carries both the parent deadline (hard cap)
		// and the idle watchdog's cancel: only a request-bound cancel
		// unblocks the body read when the watchdog fires.
		reqCtx, cancelReq := context.WithCancel(ctx)
		defer cancelReq()

		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(reqBytes))
		if err != nil {
			return nil, false, fmt.Errorf("llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.streamHTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm: %w", err)
			// Clear the stale provider status: a 429-then-outage sequence
			// must not exhaust into RateLimitError ("provider throttled") —
			// same fix as the buffered retry loop.
			lastStatus = 0
			lastBody = ""
			if isRetryableNetworkError(err) {
				continue
			}
			return nil, false, lastErr
		}

		if resp.StatusCode != http.StatusOK {
			// Error bodies are small; buffer for classification. Streaming
			// bodies (200) are never buffered — see readSSE.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			errBodyStr := strings.TrimSpace(string(errBody))
			lastStatus = resp.StatusCode
			lastBody = truncateLLMErrBody(errBodyStr)
			if errBodyStr != "" {
				lastErr = fmt.Errorf("llm: %s (status %d): %s", resp.Status, resp.StatusCode, errBodyStr)
			} else {
				lastErr = fmt.Errorf("llm: %s (status %d)", resp.Status, resp.StatusCode)
			}
			if isBillingError(resp.StatusCode, errBodyStr) {
				return nil, false, fmt.Errorf("%w — billing/quota error, not retried (check your provider balance or plan)", lastErr)
			}
			if isRetryableHTTPStatus(resp.StatusCode) {
				if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
					wait = ra
				}
				continue
			}
			return nil, false, lastErr
		}

		// 200 resets the stale-status window (see buffered Call): a 429
		// earlier in the retry loop must not mask a streaming failure.
		lastStatus = http.StatusOK
		lastBody = ""
		res, emitted, err := readSSE(ctx, reqCtx, cancelReq, resp.Body, cb)
		resp.Body.Close()
		if err != nil {
			if emitted {
				// Partial output was already delivered to the consumer —
				// never retry (would duplicate text). Surface the partial
				// result alongside the error.
				return res, emitted, err
			}
			if errors.Is(err, errNotSSE) {
				return res, emitted, err
			}
			// Pre-first-delta stream failures (idle watchdog, malformed
			// first events, mid-read resets) are transient-shaped: retry
			// within the same budget like the buffered path.
			lastErr = err
			continue
		}
		return res, emitted, nil
	}

	if lastStatus == http.StatusTooManyRequests {
		return nil, false, fmt.Errorf("llm: retry exhausted (%d attempts): %w", maxRetries+1, &RateLimitError{
			StatusCode: lastStatus,
			Attempts:   maxRetries + 1,
			Body:       lastBody,
		})
	}
	return nil, false, fmt.Errorf("llm: retry exhausted (%d attempts): %w", maxRetries+1, lastErr)
}

// readSSE parses an OpenAI-compatible SSE body, feeding the assembler and
// the consumer callback. The idle watchdog (ADR-1/ADR-6) cancels reqCtx —
// the context the HTTP request runs on, so the blocked body read unblocks —
// when no SSE event, including keepalive comment lines, arrives within
// streamIdleTimeout. parent is the caller's context (hard deadline).
func readSSE(parent, reqCtx context.Context, cancelReq context.CancelFunc, body io.Reader, cb func(Delta) error) (*CallResult, bool, error) {
	defer cancelReq()

	// Snapshot the idle timeout on this (caller-joined) goroutine: reading
	// the package var inside the watchdog would race with tests overriding
	// it after the spawning test completed but before the goroutine got
	// scheduled.
	idle := streamIdleTimeout

	// Idle watchdog goroutine: reset on every line read (including
	// keepalives); fires by canceling reqCtx, which unblocks the reader.
	idleReset := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-reqCtx.Done():
				return
			case <-idleReset:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				cancelReq()
				return
			}
		}
	}()

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // per-line cap

	var acc streamAccumulator
	emitted := false
	totalBytes := 0
	sawData := false
	done := false

	// dataLines buffers the data lines of the CURRENT SSE event: the spec
	// joins same-event data lines with "\n" and terminates the event at
	// the next blank line (or EOF). Parsing per-line misreads spec-valid
	// multi-data-line chunks as garbage and burns the retry budget
	// (B3-LLM-2).
	var dataLines []string
	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if payload == "[DONE]" {
			done = true
			return nil
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("llm: parse stream chunk: %w", err)
		}
		return acc.apply(&chunk, cb, &emitted)
	}
	abortOrErr := func(err error) (*CallResult, bool, error) {
		var abort *handlerAbort
		if errors.As(err, &abort) {
			cancelReq() // stop the watchdog
			<-watchdogDone
			return acc.result(), emitted, &StreamAbortedError{Reason: abort.err}
		}
		return acc.result(), emitted, err
	}

	for sc.Scan() {
		line := sc.Text()
		totalBytes += len(line)
		if totalBytes > maxResponseSize {
			return acc.result(), emitted, fmt.Errorf("llm: stream exceeds maximum size (%d bytes)", maxResponseSize)
		}

		// Any line — data, comment, or blank separator — proves liveness.
		select {
		case idleReset <- struct{}{}:
		default:
		}

		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" {
			// Blank line terminates the current event.
			if err := flushEvent(); err != nil {
				return abortOrErr(err)
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			continue // SSE keepalive (ADR-6)
		}
		if !strings.HasPrefix(trimmed, "data:") {
			if !sawData && !emitted {
				return acc.result(), emitted, errNotSSE
			}
			continue // stray line inside an established stream
		}
		sawData = true
		if linePayload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")); linePayload != "" {
			// Tolerate non-canonical streams that never separate events
			// with a blank line: if the buffer already holds a complete
			// JSON value, dispatch it before starting the next event.
			// Spec-canonical multi-line events (incomplete JSON so far)
			// keep accumulating; an event holding two top-level values is
			// malformed per spec anyway and dispatching it as two chunks
			// matches the pre-B3-LLM-2 per-line behavior.
			if len(dataLines) > 0 && json.Valid([]byte(strings.Join(dataLines, "\n"))) {
				if err := flushEvent(); err != nil {
					return abortOrErr(err)
				}
				if done {
					break // [DONE] without a separator — consume nothing after it
				}
			}
			dataLines = append(dataLines, linePayload)
		}
	}

	if err := sc.Err(); err != nil {
		// Distinguish the watchdog firing (reqCtx canceled, parent alive)
		// from a parent cancellation/deadline the caller caused.
		if reqCtx.Err() != nil && parent.Err() == nil {
			return acc.result(), emitted, fmt.Errorf("llm: stream idle for over %v without an event", idle)
		}
		return acc.result(), emitted, fmt.Errorf("llm: read stream: %w", err)
	}

	// EOF flush: a final event without a trailing blank line is still an
	// event ([DONE] frequently arrives last with no separator).
	if !done {
		if err := flushEvent(); err != nil {
			return abortOrErr(err)
		}
	}

	if !done && !acc.finished && !acc.finishedWithUsage() {
		return acc.result(), emitted, errors.New("llm: stream ended without [DONE] or finish_reason")
	}
	return acc.result(), emitted, nil
}

// handlerAbort carries a consumer-callback error out of the assembler.
type handlerAbort struct{ err error }

func (h *handlerAbort) Error() string { return h.err.Error() }
func (h *handlerAbort) Unwrap() error { return h.err }

// streamChunk is one SSE data payload (OpenAI chat.completion.chunk dialect).
// Content/ReasoningContent are pointers so JSON null is distinguishable from
// an absent field, and so empty fragments are not emitted as deltas.
type streamChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
			// ReasoningContent carries thinking text on GLM/DeepSeek/Kimi
			// reasoning models; absent on others.
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"` // OpenAI sends it; default 0 when absent
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageJSON `json:"usage"`
}

// toolCallAccum assembles one tool call from streaming fragments: the first
// fragment for an index carries id/name; argument fragments concatenate.
type toolCallAccum struct {
	id   string
	name string
	args strings.Builder
}

// streamAccumulator assembles a CallResult from SSE chunks.
type streamAccumulator struct {
	content  strings.Builder
	reason   strings.Builder
	tools    map[int]*toolCallAccum
	finished bool
	usage    *usageJSON
}

func (a *streamAccumulator) apply(chunk *streamChunk, cb func(Delta) error, emitted *bool) error {
	for i := range chunk.Choices {
		ch := &chunk.Choices[i]
		d := &ch.Delta

		if d.ReasoningContent != nil && *d.ReasoningContent != "" {
			a.reason.WriteString(*d.ReasoningContent)
			if err := emit(cb, Delta{Kind: DeltaReasoning, Text: *d.ReasoningContent}, emitted); err != nil {
				return err
			}
		}
		if d.Content != nil && *d.Content != "" {
			a.content.WriteString(*d.Content)
			if err := emit(cb, Delta{Kind: DeltaContent, Text: *d.Content}, emitted); err != nil {
				return err
			}
		}
		for _, tc := range d.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			if a.tools == nil {
				a.tools = make(map[int]*toolCallAccum)
			}
			acc, ok := a.tools[idx]
			if !ok {
				acc = &toolCallAccum{}
				a.tools[idx] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.args.WriteString(tc.Function.Arguments)
				if err := emit(cb, Delta{Kind: DeltaToolArgs, Text: tc.Function.Arguments}, emitted); err != nil {
					return err
				}
			}
		}
		if ch.FinishReason != nil {
			a.finished = true
		}
	}
	if chunk.Usage != nil {
		a.usage = chunk.Usage
	}
	return nil
}

func emit(cb func(Delta) error, d Delta, emitted *bool) error {
	*emitted = true
	if err := cb(d); err != nil {
		return &handlerAbort{err: err}
	}
	return nil
}

func (a *streamAccumulator) finishedWithUsage() bool { return a.usage != nil }

func (a *streamAccumulator) result() *CallResult {
	res := &CallResult{
		Content:          a.content.String(),
		ReasoningContent: a.reason.String(),
	}
	applyUsage(a.usage, res)
	if len(a.tools) > 0 {
		idxs := make([]int, 0, len(a.tools))
		for idx := range a.tools {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			acc := a.tools[idx]
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:   acc.id,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      acc.name,
					Arguments: acc.args.String(),
				},
			})
		}
	}
	return res
}
