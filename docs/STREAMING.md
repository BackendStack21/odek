# Response Streaming

odek can stream LLM responses to the terminal as they are generated, instead of waiting for the complete response before printing anything. Streaming is **opt-in and off by default**; with it disabled, behavior is identical to previous releases. It works with every odek-supported provider: streaming is implemented once against the OpenAI-compatible SSE protocol that all backends speak on `/chat/completions`, and any endpoint that rejects streaming or answers with a non-SSE body transparently falls back to the buffered path.

Streaming matters most for thinking-default models (GLM-5.x, DeepSeek v4 Pro, Kimi, OpenAI reasoning models), which spend most of their wall clock reasoning before the first answer token — a trivial turn can take 5–30 s of silent waiting without it.

## Supported Providers

All providers stream; the differences below are absorbed by the client and never require configuration.

| Provider | Usage in stream | Reasoning deltas | Notes |
|---|---|---|---|
| **Z.ai** (GLM) | finish chunk | `reasoning_content` | validated live |
| **OpenAI** | separate usage-only chunk (behind `stream_options`, sent automatically) | none via chat/completions | |
| **DeepSeek** | final chunk, incl. cache hit/miss tokens | `reasoning_content` | cache metrics preserved |
| **Anthropic** (compat endpoint) | final chunk | where exposed | |
| **Kimi / Moonshot** | final chunk | on thinking variants | |
| **Ollama / vLLM / LiteLLM / Groq / Together / Fireworks** | often absent | implementation-dependent | absent usage leaves token accounting at zero, same as the buffered path |

## Enabling

### CLI
```bash
odek run --stream "task"
odek repl --stream
```

The `--stream` flag is available on `odek run` and `odek repl`. `odek serve` accepts it for completeness, but the Web UI does not consume deltas yet (see [Not Yet Streamed](#not-yet-streamed)).

### Config file (`~/.odek/config.json` or `./odek.json`)
```json
{
  "stream": true
}
```

`stream` follows the standard five-layer priority (config → env → CLI) and may be set in project configs, like `prompt_caching`.

### Environment variable
```bash
export ODEK_STREAM=true
```

### Programmatic API
```go
agent, err := odek.New(odek.Config{
    Model:        "glm-5.3",
    Stream:       true,
    DeltaHandler: func(d llm.Delta) error {
        if d.Kind == llm.DeltaContent {
            fmt.Print(d.Text)
        }
        return nil // a non-nil error aborts generation
    },
})
```

The handler receives `llm.DeltaReasoning` and `llm.DeltaContent` fragments (tool-argument fragments are suppressed by the engine — they are partial JSON). It is invoked synchronously and must be non-blocking, like the other loop callbacks.

## Terminal Output

With streaming enabled, `odek run` and `odek repl` print reasoning and the answer as they arrive, then the regular per-iteration statistics:

```
🧠 The user just said "Hi". Simple greeting. I should respond concisely…

Morning, Rolando. Ready when you are — last open threads were the
sharingan extension install and the M3-18 license policy.
═══ Iter 1/90 · GLM 5.3 (Z.ai) ═══  [26189 in · 91 out · 10.4s]

── 26189 in · 91 out · 64 cached
```

The reasoning block is dimmed with a single 🧠 cue, the answer follows after a blank line, and the iteration header always starts on a fresh line. Nothing double-prints: the renderer suppresses the buffered reasoning/answer blocks for text that was already streamed. Statistics headers and the token summary render as usual.

## Behavior

1. **Only the main think step streams.** Auxiliary LLM calls — context compaction, the iteration-budget progress summary, memory extraction, skill assessment — always use the buffered path.
2. **Tool calls arrive complete.** The model's tool invocations are assembled from their streamed fragments before execution; tool-argument fragments are not forwarded to delta consumers.
3. **A handler error aborts generation.** Returning a non-nil error from the delta handler cancels the stream; the loop fails the turn with the wrapped `*llm.StreamAbortedError` instead of retrying.
4. **Sessions, budgets, and the untrusted-content boundary are unchanged.** Streaming assembles the same result the buffered path returns, so token accounting, cost enforcement, session persistence, and audit operate on identical data.

## Reliability

- **Hard deadline + idle watchdog.** Every streamed call is bounded by a wall-clock deadline (the model profile's request timeout) covering the whole stream, plus a 60 s idle watchdog that trips when no SSE event — including provider keepalive comments — arrives. A trickling or stalled stream can never run unbounded.
- **No duplicated partial output.** Transient failures are retried with the same backoff as the buffered path, but only until the first fragment has been delivered; after that, the failure is terminal and the partial text stays as printed.
- **Learn-once fallbacks.** A provider that rejects the `stream_options` field is retried once without it (streaming continues); a provider that rejects `stream` outright, or answers a streamed request with a non-SSE body, switches permanently to the buffered path. Both are learned per client, not configured.
- **Billing errors still fail fast.** A 429 reporting an empty balance or exhausted quota is returned immediately with the provider's message; it is never retried into an opaque timeout.

## Not Yet Streamed

- **Web UI (`odek serve`)** — a per-connection coalescing `delta` message protocol is planned; `writeWSJSON` frame serialization landed in preparation.
- **Telegram** — completed iterations are sent as messages today; throttled in-place editing is a possible follow-up.
- **Default** — streaming stays opt-in until it has soaked for a release; the buffered path remains the automatic fallback.

## Implementation Details

- `llm.Client.CallStream` (`internal/llm/stream.go`) parses the SSE dialect and returns the same `*CallResult` as `Call`; the assembler handles usage on the finish chunk or in a separate empty-choices chunk, `null` content fields, and per-index tool-argument concatenation.
- Streaming requests use a pooled HTTP client without a client-level timeout (`transport.NewPooledClientNoDeadline`) — a whole-request `http.Client.Timeout` would kill long body reads — sharing the connection pool with the buffered client. Deadlines are enforced per request via context.
- The engine wires streaming through `loop.Engine.SetStream` / `SetDeltaHandler`, following the existing optional-callback pattern (`SetSignalHandler`, `SetToolEventHandler`).
- Offline test coverage lives in `internal/llm/stream_test.go` (the provider-variance and failure-mode matrix) and `internal/loop/loop_test.go` (engine dispatch and the buffered default).
