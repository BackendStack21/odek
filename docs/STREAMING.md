# LLM Response Streaming — Design Assessment

Status: **assessment / RFC — not implemented**. This document maps the current
request path, records the OpenAI-compatible SSE wire contract (validated
against the live Z.ai API), and proposes a phased introduction of `stream:
true` support end to end — for **every** provider and model odek supports,
implemented once at the protocol level rather than per provider.

---

## 1. Motivation

odek's LLM calls are fully buffered: nothing reaches the terminal, the Web UI,
or Telegram until the provider returns the complete response. With
thinking-default models this is the dominant UX cost — measured on the Z.ai
coding endpoint during the GLM support work:

| Model | Turn | Wall clock | Terminal output before completion |
|-------|------|-----------|-----------------------------------|
| glm-5.3 | trivial echo | 4.4 s | none |
| glm-5-turbo | trivial echo | 27.5 s | none |
| glm-5.3 | "Say OK" (max_tokens 200) | — | 199 of 200 tokens were `reasoning_tokens` |

The engine already papers over the silence with heartbeats
(`tool_running` fires every 60 s, `internal/loop/signal.go`) and the narrator
(`internal/narrate`) — both are workarounds for the absence of incremental
output. Streaming converts dead wait time into visible thinking/answer text,
enables first-token latency as a metric, and lays groundwork for mid-generation
cancellation.

**Scope: every provider and model odek supports, not a Z.ai feature.** All
odek-supported backends — OpenAI, Anthropic (via its OpenAI-compat
`/chat/completions` surface), DeepSeek, Z.ai, Kimi/Moonshot, and self-hosted
OpenAI-compatible servers (Ollama, vLLM, LiteLLM, Groq, Together, Fireworks) —
speak the same de facto standard: OpenAI-style SSE on `/chat/completions`.
Streaming is therefore implemented **once, at the protocol level**, with
per-provider quirks absorbed by the parser (§3) and a learn-once fallback to
today's buffered path for anything that rejects streaming or emits a
non-SSE body. The GLM measurements above are one instance of a general
property: any thinking-default model (GLM-5.x, DeepSeek v4 Pro with thinking
enabled, Kimi, OpenAI o-series/gpt-5 reasoning models) spends most of its
wall clock before the first content token.

## 2. Current architecture (integration points)

| Component | Today | Streaming touchpoint |
|-----------|-------|---------------------|
| `internal/llm/client.go` — `postChatWithRetry` | POST with `Stream: false`, reads whole body via `io.ReadAll`, retries transient statuses/bodies with jittered backoff (8 attempts), billing-429 fast-fail, `validateCompletionBody` gates 200-with-garbage | Phase 1: SSE line parser, delta assembler, idle watchdog; retry only pre-first-delta failures |
| `internal/llm/client.go` — `parseResponse` | single JSON body → `CallResult{Content, ReasoningContent, ToolCalls, usage, cache metrics}` | Phase 1: assembler must produce the *same* `CallResult` |
| `internal/loop/loop.go:1451` — `e.client.Call(ctx, …)` | engine blocks for the full result, then renders iteration, fires `IterationCallback`, executes tools (parallel) | Phase 2: optional delta handler threaded through, mirroring `SetSignalHandler` (`loop.go:357`) and `SetToolEventHandler` (`loop.go:358`) |
| `internal/render` | compression-oriented; `Iteration`/`FinalAnswer` print complete text | Phase 3: raw mode prints content deltas live; engaging mode shows a reasoning ticker/summary |
| `cmd/odek/serve.go` — WS | pushes `iteration`, `tool_event`, `session`, `busy` messages after completion via `writeWSJSON` | Phase 3: new `delta` message type. **Prerequisite:** `writeWSJSON` is currently called from multiple goroutines without synchronization (known audit finding) — a send mutex must land first or delta fan-out will tear frames |
| `cmd/odek/telegram.go` | sends completed iterations as messages | Phase 3 (optional): throttled message editing (~1/s) |
| `internal/budget` | usage read from the buffered body after the call; per-run checker enforces caps | unchanged — usage arrives on the final SSE chunk (see §3) |
| `internal/events` (`odek.event/v1`) | `iteration_completed` carries sizes/hashes only | optional additive `first_token_ms` timing field — no content, preserving the no-secrets posture |
| Sessions / audit / untrusted wrapper | operate on the assembled assistant message and tool results | unchanged; persistence still snapshots the assembled result |

## 3. Wire format — one protocol, per-provider variance

The parser targets the **OpenAI-compatible SSE contract**, which every
odek-supported backend speaks on `/chat/completions`. Reference capture from
the live Z.ai coding endpoint (`glm-5.3`, `stream: true`,
`stream_options: {"include_usage": true}`):

1. **Reasoning deltas first** (thinking models):
   `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The"}}]}`
2. **Content deltas** follow:
   `…{"delta":{"role":"assistant","content":"OK"}}`
3. **Tool-call deltas** are keyed by `index` — Z.ai emitted the complete call
   in one fragment, but OpenAI-style streams split `function.arguments` across
   many fragments that must be string-concatenated per index:
   `{"delta":{"tool_calls":[{"id":"call_…","index":0,"type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Havana\"}"}}]}}`
4. **Final chunk** carries `finish_reason` **and** the full usage object:
   `{"choices":[{"index":0,"finish_reason":"length","delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":14,"completion_tokens":200,"total_tokens":214,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":199}}}`
5. **Sentinel:** `data: [DONE]`.

Per-provider and per-model variance the parser must absorb:

| Backend | Usage in stream | Reasoning deltas | Notes |
|---------|----------------|------------------|-------|
| Z.ai (GLM) | on the finish chunk, with or without `stream_options` | `delta.reasoning_content` | validated live (traces above) |
| OpenAI (gpt-5/o-series) | only with `stream_options.include_usage`, in a **separate chunk with empty `choices`** | none — chat/completions does not return reasoning text | content-only deltas; send `stream_options` always |
| OpenAI (non-reasoning) | as above | none | |
| DeepSeek | final chunk, incl. `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens` | `delta.reasoning_content` (deepseek-reasoner) | cache metrics must survive into `CallResult` |
| Anthropic (OpenAI-compat endpoint) | final chunk | thinking text surfaces as reasoning deltas where exposed | odek already sends `anthropic-version`; buffered fallback otherwise |
| Kimi / Moonshot | final chunk | `delta.reasoning_content` on thinking variants | |
| Ollama / vLLM / LiteLLM / Groq / Together / Fireworks | often **absent** or partial | implementation-dependent | absent usage ⇒ `CallResult` token fields stay 0 — exactly today's buffered behavior for such endpoints (budget falls back to estimates/flat prices) |

Parser rules that fall out of the table:

- Usage may arrive on the finish chunk or in a usage-only chunk with empty
  `choices`; both are valid, and neither is a "malformed body".
- `delta.content` (and `reasoning_content`) can be `null`, not merely empty,
  on reasoning-only or keepalive chunks.
- Tool arguments concatenate per `index`; `id`/`name` arrive on the first
  fragment for that index.
- Model-level, not just provider-level: reasoning models emit reasoning
  deltas; non-reasoning models emit content only. The parser treats both
  identically — reasoning absence is normal, not an error.

## 4. Proposed design (phased)

### Phase 1 — transport streaming in `internal/llm` (no behavior change)

```go
// DeltaKind discriminates streamed fragments.
type DeltaKind int // DeltaReasoning | DeltaContent | DeltaToolArgs

type Delta struct {
    Kind DeltaKind
    Text string // concatenated fragment
}

// CallStream is Call with incremental delivery. cb is invoked from the
// HTTP reader goroutine; it must be non-blocking (same contract as
// loop.SignalHandler). The returned CallResult is identical to Call's.
func (c *Client) CallStream(ctx context.Context, msgs []Message,
    sys []SystemBlock, tools []ToolDef, cb func(Delta)) (*CallResult, error)

// Call becomes a thin wrapper: CallStream with cb == nil.
```

Key decisions:

- **Timeout semantics change.** `http.Client.Timeout` bounds the *whole* body
  read and would kill long streams. Streaming must switch to a
  `ResponseHeaderTimeout` + idle-read watchdog (reset on every chunk; e.g.
  `min(requestTimeout, 60s)` between chunks). The watchdog error maps to the
  existing retry classification.
- **Retry boundary.** Failures *before the first emitted delta* (connect,
  TLS, status errors, billing fast-fail — all header-phase) retry exactly as
  today, because nothing was consumed. Failures *after* the first delta are
  terminal: the caller has already shown text and a silent full-retry would
  duplicate output. Surface them as errors on the assembled partial result.
- **Assembly.** Content and reasoning accumulate by concatenation; tool calls
  accumulate per `index` (first fragment carries `id`/`name`; `arguments`
  fragments concatenate). The assembler returns the existing `CallResult`, so
  `SimpleCall`, budget accounting, session persistence, and the untrusted
  wrapper are untouched.
- **Config.** `stream: true` / `ODEK_STREAM=1` / `--stream`, default **off**
  in Phase 1. On a provider 400 that names `stream` (or an SSE-less body),
  fall back once to the buffered path and remember, mirroring the existing
  `forceNoneEffort` learn-once pattern in `client.go`.

### Phase 2 — engine fan-out

`Engine.SetDeltaHandler(cb func(loop.Delta))` following the established
optional-callback pattern. The engine forwards reasoning and content deltas
separately (the renderer wants different treatment) and suppresses tool-arg
deltas by default (noise + JSON fragments). `iteration_completed` accounting
is unchanged because usage still arrives on the final chunk.

### Phase 3 — surfaces

- **REPL/CLI (raw mode):** stream `DeltaContent` to stdout as it arrives;
  reasoning collapses to a one-line "thinking…" spinner that resolves into
  the existing reasoning header. Engaging mode streams narrator-style lines.
- **Web UI:** new WS message `{"type":"delta","kind":"content"|"reasoning",
  "text":"…"}` plus a terminal `{"type":"delta_end"}`; `ui/js` appends into
  the in-flight iteration card. **Lands only after the `writeWSJSON` send
  mutex** (concurrent-writer audit finding) — delta fan-out multiplies the
  exact interleaving that finding describes.
- **Telegram (optional):** edit the in-flight message at most once per second.
- **Events:** optional `first_token_ms` on `iteration_completed` — timing
  metadata only, consistent with the hashed-args/no-content event contract.

### Rollout gate

Flip the default to streaming after one release of opt-in soak; keep the
buffered path as the automatic fallback (unsupported provider, learned 400,
watchdog misbehavior). The fakeserver (`internal/mcpclient/testdata` pattern)
gains an SSE mode for E2E.

## 5. Risks and open questions

- **Provider variance** is the main surface area; the §3 parser rules are the
  contract. `null` content fields, usage-chunk placement, and per-index tool
  fragments each need dedicated tests. Live validation so far covers Z.ai
  only — the §3 table for other backends is specification-derived, which is
  exactly why the buffered fallback is the rollout safety net and why the
  fakeserver SSE mode (§4 rollout gate) must encode one hostile/quirky stream
  per column of the table.
- **No-regression for non-streaming backends:** endpoints that never
  advertise SSE, or answer `stream: true` with a buffered body, take the
  fallback path transparently — streaming must never narrow the set of
  working providers.
- **Partial-output retries** are intentionally not attempted (§ Phase 1);
  a mid-stream failure shows an error next to the partial text instead of
  duplicating it.
- **Cost/budget**: usage arrives only at the end, so mid-generation budget
  abort is impossible without provider support; acceptable (same as today).
- **Redaction/session**: deltas are model-authored assistant content, the
  same class as today's buffered content; persistence and `internal/redact`
  continue to operate on the assembled message.
- **DoS posture**: SSE framing is line-bounded (reuse `maxResponseSize` as a
  cumulative cap across the stream, not per-chunk); the idle watchdog bounds
  silent streams.

## 6. Suggested implementation order

1. `writeWSJSON` mutex (small, independently mergeable — already a known gap)
2. Phase 1 client (`CallStream`, assembler, watchdog, fallback) + SSE unit
   tests against `httptest` chunked servers, including malformed-stream cases
3. Phase 2 engine handler + REPL raw-mode rendering
4. Phase 3 serve WS `delta` + UI, then optional Telegram/events additions
5. Flip default after soak
