# LLM Response Streaming — Implementation-Ready Design

Status: **design spec — ready to implement by milestone** (see §5). This
document supersedes the RFC draft: every open question from review is now a
resolved decision (§4, ADR-style) and the work is decomposed into mergeable
milestones with acceptance criteria (§5) and a pinned test matrix (§6).

Streaming is implemented **once, at the OpenAI-compatible SSE protocol
level**, for **every** provider and model odek supports — OpenAI, Anthropic
(via its OpenAI-compat `/chat/completions` surface), DeepSeek, Z.ai, Kimi,
and self-hosted servers (Ollama, vLLM, LiteLLM, Groq, Together, Fireworks).
Per-provider quirks are absorbed by the parser (§3); anything that rejects
streaming or answers non-SSE takes a learn-once buffered fallback, so
streaming never narrows the set of working providers.

---

## 1. Motivation

odek's LLM calls are fully buffered: nothing reaches the terminal, the Web UI,
or Telegram until the provider returns the complete response. Measured on the
Z.ai coding endpoint during the GLM support work:

| Model | Turn | Wall clock | Terminal output before completion |
|-------|------|-----------|-----------------------------------|
| glm-5.3 | trivial echo | 4.4 s | none |
| glm-5-turbo | trivial echo | 27.5 s | none |
| glm-5.3 | "Say OK" (max_tokens 200) | — | 199 of 200 tokens were `reasoning_tokens` |

This generalizes to any thinking-default model (GLM-5.x, DeepSeek v4 Pro with
thinking enabled, Kimi, OpenAI o-series/gpt-5): most wall clock is spent
before the first content token. Today's `tool_running` heartbeats and the
narrator exist largely to mask that silence. Streaming converts dead wait
time into visible thinking/answer text and enables first-token latency as a
metric. Mid-generation cancellation is a **consequence**, not a goal (ADR-3).

## 2. Current architecture (integration points)

| Component | Today | Streaming touchpoint |
|-----------|-------|---------------------|
| `internal/llm/client.go` — `postChatWithRetry` | `Stream: false`, whole-body `io.ReadAll`, jittered retries (8 attempts), billing-429 fast-fail, `validateCompletionBody` | M1: `postChatStream` sibling; pre-first-delta failures reuse the same retry loop |
| `internal/llm/client.go` — `parseResponse` | one JSON body → `CallResult` | M1: SSE assembler must produce a byte-identical `CallResult` for the same logical response |
| `internal/loop/loop.go:1451` — `e.client.Call(ctx, …)` | **bare `ctx`, no per-call deadline**; only bound is `http.Client.Timeout` from `transport.NewPooledClient` (`internal/transport/client.go:33`) | M0 adds the missing per-call deadline (buffered path too); M2 switches this one call site to `CallStream` |
| `internal/loop/loop.go:1053` (compaction), `:1103` (iteration summary) | own `sideTimeout()` ctx | **unchanged** — `Call` stays buffered (ADR-5) |
| `internal/llm` — `SimpleCall` | buffered, own caller ctx | unchanged |
| `internal/render` | prints complete `Iteration`/`FinalAnswer` blocks; raw-mode `\r\n` handling | M3: replace-block streaming renderer |
| `cmd/odek/serve.go` — `writeWSJSON` | unsynchronized concurrent sends (known audit finding) | M0: send mutex. M4: per-connection coalescing delta channel (ADR-2) |
| `cmd/odek/telegram.go` | completed iterations as messages | M5 (optional): throttled edits |
| `internal/budget` | usage from buffered body, post-call check | unchanged — usage arrives on the final SSE chunk (§3) |
| `internal/events` | `iteration_completed` carries sizes/hashes | M5 (optional): additive `first_token_ms` |
| Sessions / audit / untrusted wrapper | operate on the assembled message | unchanged |

## 3. Wire format — one protocol, per-provider variance

Reference capture (live, Z.ai coding endpoint, `glm-5.3`, `stream: true`,
`stream_options: {"include_usage": true}`):

1. **Reasoning deltas first** (thinking models):
   `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"The"}}]}`
2. **Content deltas** follow:
   `…{"delta":{"role":"assistant","content":"OK"}}`
3. **Tool-call deltas**, keyed by `index` (arguments may split across many
   fragments that concatenate per index; `id`/`name` arrive on the first):
   `{"delta":{"tool_calls":[{"id":"call_…","index":0,"type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Havana\"}"}}]}}`
4. **Final chunk** carries `finish_reason` and the full usage object
   (`prompt_tokens`, `completion_tokens`, `prompt_tokens_details.cached_tokens`,
   `completion_tokens_details.reasoning_tokens`).
5. **Sentinel:** `data: [DONE]`.
6. **Keepalives:** SSE comment lines (`: ping …`) and blank lines are legal
   and carry no payload (ADR-6).

| Backend | Usage in stream | Reasoning deltas | Notes |
|---------|----------------|------------------|-------|
| Z.ai (GLM) | finish chunk, with or without `stream_options` | `delta.reasoning_content` | validated live (traces above) |
| OpenAI (gpt-5/o-series) | only with `stream_options.include_usage`, separate chunk with **empty `choices`** | none via chat/completions | send `stream_options` always (ADR-4) |
| DeepSeek | final chunk, incl. `prompt_cache_hit/miss_tokens` | `delta.reasoning_content` (reasoner) | cache metrics must survive into `CallResult` |
| Anthropic (OpenAI-compat) | final chunk | where exposed | buffered fallback otherwise |
| Kimi / Moonshot | final chunk | on thinking variants | |
| Ollama / vLLM / LiteLLM / Groq / Together / Fireworks | often absent | implementation-dependent | absent usage ⇒ token fields 0 — same as today's buffered behavior for these endpoints |

Parser rules: usage may arrive on the finish chunk or in a usage-only chunk
(empty `choices` is valid, not malformed); `delta.content`/`reasoning_content`
may be `null`; tool arguments concatenate per `index`; reasoning absence is
normal (non-reasoning models), not an error.

## 4. Resolved design decisions (ADRs)

**ADR-1 — Timeouts: hard wall-clock deadline AND idle watchdog.**
The RFC draft replaced the whole-body `http.Client.Timeout` with an idle
watchdog. Rejected: the main call at `loop.go:1451` passes bare `ctx`, so
that client timeout is the *only* wall-clock bound — a trickling stream (one
chunk every 50 s) defeats an idle watchdog yet would run until the runtime
budget (default 3600 s), an hour of billed trickle and a regression today's
code does not have. Resolved design:

- Every streamed call is wrapped `context.WithTimeout(parent, requestTimeout)`
  where `requestTimeout` is the client/profile timeout — the hard cap over
  the entire stream (headers, deltas, usage chunk).
- Idle watchdog: `streamIdleTimeout = 60 s`, reset by **any** SSE event,
  including comment/blank keepalive lines (ADR-6); tripped ⇒ error.
- Pooling: `transport.NewPooledClient(timeout<=0)` today falls back to
  `DefaultTimeout`, so a no-timeout streaming client needs a new constructor
  (`transport.NewPooledClientNoDeadline()`) that sets `http.Client.Timeout =
  0` while **sharing the package-level `http.Transport`** so buffered and
  streaming clients use one connection pool. The buffered client is
  unchanged.
- M0 additionally wraps the *buffered* main call in the same per-call
  deadline, making both paths symmetric and closing the trickle vector even
  without streaming.

**ADR-2 — Fan-out: coalescing channels, never inline writes.**
A mutex makes `writeWSJSON` *safe*, not *fast*: inline `cb → writeWSJSON →
slow browser TCP` back-pressures the engine goroutine reading the stream.
Resolved design: the engine's delta handler must be non-blocking; `serve`
attaches a per-connection **coalescing buffer** — a bounded channel (256
deltas) drained by a flusher goroutine that appends pending text and flushes
every ~100 ms; on overflow it drops intermediate text, marks the block
`truncated`, and the terminal `delta_end` carries the full assembled block so
the UI repaints losslessly. Engine never blocks on a slow consumer. The
REPL writes directly (a local tty is fast; Ctrl-S stalls are the user's
explicit choice, same as today's printing). This mirrors the `internal/events`
drop-on-full precedent, adapted for display via end-repaint.

**ADR-3 — Cancellable callback: `func(Delta) error`.**
A `func(Delta)` signature cannot abort, so the RFC's "groundwork for
cancellation" claim was unsupported. Resolved: the handler returns an error
to abort; the client cancels the request ctx and returns a typed
`*llm.StreamAbortedError`; the loop maps it to "stop this call" without
triggering retry/trim/survival paths. Changing the signature later would
break every consumer — it is decided now.

**ADR-4 — Two learn-once fallbacks, field-level first.**
Conflated in the RFC. Resolved:
- *Field-level* (`dropStreamOptions`): a 400 naming `stream_options` ⇒ retry
  the same streaming request without the field (usage nicety lost, streaming
  kept). Modeled on the existing `forceNoneEffort` pattern.
- *Path-level* (`forceBuffered`): a 400 naming `stream`, or a 200 whose body
  is not SSE (first non-comment line fails `data:` parse / Content-Type is
  not `text/event-stream`) ⇒ fall back once to the buffered path, remember
  per client. Watchdog failures and mid-stream errors are **errors**, not
  fallback triggers.

**ADR-5 — `Call` stays buffered; `CallStream` is a sibling.**
The RFC made `Call` a wrapper over `CallStream`, silently changing timeout
semantics for every auxiliary caller (compaction, iteration summary,
`SimpleCall`). Resolved: invert it. Only the engine's main think step
(`loop.go:1451`) calls `CallStream`, and only when streaming is enabled.
Aux paths keep byte-identical behavior — pinned by test (§6 T12).

**ADR-6 — Keepalives reset the idle watchdog.**
SSE comment lines (`:` prefix) and blank lines are events for liveness
purposes and payload-free. They must reset the idle timer; they must not be
parsed as data or counted as deltas.

## 5. Milestones

Each milestone is independently mergeable and lands behind tests; CI green is
the gate for all of them. `stream` ships **off** by default until M5.

### M0 — prerequisites (no streaming behavior)
Closes two known gaps; valuable regardless of streaming.
- [ ] `writeWSJSON` send mutex in `cmd/odek/serve.go` (audit finding: concurrent
      sends from loop/​subagent/approver goroutines can tear frames)
- [ ] Per-call hard deadline for the buffered main call in
      `internal/loop/loop.go` (`context.WithTimeout(ctx, requestTimeout)`)
- **Acceptance:** race-detector test with parallel WS writers + iteration
  flow; buffered run against a trickle server (chunk per 5 s, deadline 2 s)
  fails at the deadline, not at 3600 s.

### M1 — transport streaming in `internal/llm`
- [ ] `transport.NewPooledClientNoDeadline()` sharing the package Transport
- [ ] `Client.CallStream(ctx, msgs, sys, tools, cb func(Delta) error)
      (*CallResult, error)` — SSE line reader, assembler (content,
      reasoning, per-index tool args), usage capture per §3, hard deadline
      (ADR-1) + idle watchdog (ADR-6), cancellable callback (ADR-3),
      field/path fallbacks (ADR-4), billing-429 fast-fail unchanged
- [ ] `Delta{Kind: DeltaReasoning|DeltaContent|DeltaToolArgs, Text}`; tool-arg
      deltas suppressed behind an opt-in flag (noise control)
- [ ] Config plumbing: `stream` / `ODEK_STREAM` / `--stream` (default off),
      loader + trust-split rules (normal UX knob, project config allowed)
- [ ] Fixture corpus `internal/llm/testdata/sse/`: the captured Z.ai trace
      plus synthesized per-quirk streams (§6)
- **Acceptance:** for every fixture, `CallStream(cb=nil)` returns the same
  `CallResult` the buffered path returns for the equivalent complete body;
  trickle-attack test proves the hard cap (§6 T9); no test touches the
  network.

### M2 — engine wiring
- [ ] `Engine.SetDeltaHandler(func(loop.Delta) error)` following the
      `SetSignalHandler`/`SetToolEventHandler` optional-callback pattern;
      forwards reasoning/content deltas, suppresses tool args by default
- [ ] Main think step uses `CallStream` when streaming is enabled; maps
      `StreamAbortedError` to a clean stop (no retry/trim/survival)
- [ ] `first_token_ms` captured internally for the M5 event field
- **Acceptance:** §6 T12 pins aux paths buffered; abort test (handler error
  ⇒ call stops, session intact, no duplicate output on next iteration).

### M3 — REPL/CLI rendering
- [ ] Replace-block streaming renderer: streamed lines are erased (cursor-up
      + clear, `\r\n`-safe) before the canonical `Iteration` block prints, so
      nothing double-prints; line-count tracking; engaging mode gets a
      reasoning ticker that resolves into the reasoning header
- **Acceptance:** golden-terminal test — stream then completion produces
  exactly the pre-streaming `Iteration` output plus the streamed text that
  was erased; resize-safe best effort documented.

### M4 — serve + Web UI
- [ ] Per-connection coalescing delta channel + flusher per ADR-2 (bounded,
      drop-with-truncation-mark, lossless `delta_end` repaint)
- [ ] WS protocol: `{"type":"delta","kind":"content"|"reasoning","text":…}`
      and `{"type":"delta_end","truncated":bool,"block":…}`; `ui/js` renders
      into the in-flight iteration card
- **Acceptance:** slow-consumer test — a client reading 1 frame/s never
  delays the engine's think loop (measured via iteration latency); overflow
  produces a truncated marker and a correct final repaint.

### M5 — optional surfaces + default flip
- [ ] Telegram throttled (≤1/s) message editing
- [ ] `odek.event/v1` additive `first_token_ms` on `iteration_completed`
      (timing only — preserves the no-secrets posture)
- [ ] `docs/CONFIG.md` stream field; README mention
- [ ] Default flip to streaming **after one minor release of opt-in soak**
      with no fallback-regression reports; buffered path remains the
      automatic fallback

## 6. Test matrix (all `httptest`, no network)

| # | Fixture / scenario | Asserts |
|---|--------------------|---------|
| T1 | Z.ai trace (reasoning → content → usage-on-finish → `[DONE]`) | CallResult equality with buffered equivalent |
| T2 | OpenAI shape: separate empty-`choices` usage chunk (with `stream_options`) | usage captured; empty choices not "malformed" |
| T3 | `null` content / reasoning fields | no parse error, no phantom text |
| T4 | tool args split across N fragments, two interleaved calls (index 0/1) | correct per-index assembly |
| T5 | keepalive comment lines interleaved | watchdog reset, no payload |
| T6 | handler returns error mid-stream | `StreamAbortedError`, request canceled, partial result returned |
| T7 | 400 naming `stream_options` | field-dropped retry, still streaming |
| T8 | 400 naming `stream`; non-SSE 200 body | learn-once buffered fallback |
| T9 | trickle server: chunk per `idle/2`, deadline < total | hard deadline enforced (ADR-1 regression pin) |
| T10 | silent-then-die server | idle watchdog error |
| T11 | billing 429 (code 1113 body) with `stream: true` | fast-fail, exactly one request |
| T12 | engine run with streaming on; compaction + summary triggered | aux calls hit buffered `Call` (spy client pin, ADR-5) |
| T13 | usage absent (local-server shape) | token fields 0, budget unchanged |
| T14 | malformed JSON mid-stream after deltas | terminal error, partial text surfaced |

## 7. Residual risks

- Spec-derived variance: only Z.ai is live-validated; the fixture corpus
  encodes the §3 table, and the fallback is the safety net — but the first
  weeks after the default flip should watch fallback-telemetry (M5 note:
  count learn-once fallbacks per provider in the event stream, type only).
- Mid-stream failures intentionally do not auto-retry (partial text already
  emitted); the error renders next to the partial output.
- Budget enforcement stays post-call (usage arrives last); identical to today.
