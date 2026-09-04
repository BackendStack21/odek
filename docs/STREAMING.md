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

The `--stream` flag is available on `odek run`, `odek repl`, and `odek serve` — the Web UI consumes the stream live as `thinking_delta` / `token_delta` fragments when streaming is on (see [WEBUI.md](WEBUI.md)).

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
🧠 Simple question — the relevant context is already loaded. Answer directly…

Morning! The build is green and yesterday's fixes are merged. What's next?
═══ Iter 1/90 · GLM 5.3 (Z.ai) ═══  [18432 in · 78 out · 8.1s]

── 18432 in · 78 out · 51 cached
```

The reasoning block is dimmed with a single 🧠 cue, the answer follows after a blank line, and the iteration header always starts on a fresh line. Nothing double-prints: the renderer suppresses the buffered reasoning/answer blocks for text that was already streamed. Statistics headers and the token summary render as usual.

## Behavior

1. **Only the main think step streams.** Auxiliary LLM calls — context compaction, the iteration-budget progress summary, memory extraction, skill assessment — always use the buffered path.
2. **Tool calls arrive complete.** The model's tool invocations are assembled from their streamed fragments before execution; tool-argument fragments are not forwarded to delta consumers.
3. **A handler error aborts generation.** Returning a non-nil error from the delta handler cancels the stream; the loop fails the turn with the wrapped `*llmclient.StreamAbortedError` instead of retrying.
4. **Sessions, budgets, and the untrusted-content boundary are unchanged.** Streaming assembles the same result the buffered path returns, so token accounting, cost enforcement, session persistence, and audit operate on identical data.

## Reliability

- **Hard deadline + idle watchdog.** Every streamed call is bounded by a wall-clock deadline (`llm.request_timeout_seconds`, default 300s) covering the whole stream, plus a 300s idle watchdog (`llm.stream_idle_timeout_seconds`, floor 5s) that trips when no SSE event — including provider keepalive comments — arrives. A trickling or stalled stream can never run unbounded.
- **No duplicated partial output.** Transient failures are retried with the same backoff as the buffered path, but only until the first fragment has been delivered; after that, the failure is terminal and the partial text stays as printed.
- **Learn-once fallbacks.** A provider that rejects the `stream_options` field is retried once without it (streaming continues); a provider that rejects `stream` outright, or answers a streamed request with a non-SSE body, switches permanently to the buffered path. Both are learned per client, not configured.
- **Billing errors still fail fast.** A 429 reporting an empty balance or exhausted quota is returned immediately with the provider's message; it is never retried into an opaque timeout.

## Not Yet Streamed

- **Telegram** — completed iterations are sent as messages today; throttled in-place editing is a possible follow-up.
- **Default** — streaming stays opt-in until it has soaked for a release; the buffered path remains the automatic fallback.

## Implementation Details

- Streaming is owned by [`go-llm-sdk`](https://github.com/BackendStack21/go-llm-sdk). odek's `internal/llmclient` forwards `CallStream` and maps deltas.
- Streaming requests use a pooled HTTP client without a client-level timeout (`transport.NewPooledClientNoDeadline`) — a whole-request `http.Client.Timeout` would kill long body reads — sharing the connection pool with the buffered client. Deadlines are enforced per request via context.
- The engine wires streaming through `loop.Engine.SetStream` / `SetDeltaHandler`, following the existing optional-callback pattern (`SetSignalHandler`, `SetToolEventHandler`).
- Offline test coverage lives in the SDK and `internal/loop/loop_test.go` (engine dispatch and the buffered default).

## Idle watchdog

A stream that produces no SSE events — keepalive comment lines count — for `llm.stream_idle_timeout_seconds` (default **300s**, env `ODEK_STREAM_IDLE_TIMEOUT_SECONDS`) is dropped and retried like any transient failure, as long as nothing was emitted yet. Once deltas have been delivered, an idle abort is never retried (that would duplicate text); the partial result is surfaced with the error. Eight attempts with jittered exponential backoff are shared with the buffered client.
