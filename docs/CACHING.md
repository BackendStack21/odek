# Prompt Caching

odek supports prompt caching for supported LLM providers. When enabled, the system prompt and first user message are annotated with cache markers, reducing both latency and cost on repeated interactions.

## Supported Providers

| Provider | Mechanism | Cached tokens cost | TTFT improvement |
|---|---|---|---|
| **Anthropic** (Claude) | Explicit `cache_control: ephemeral` markers | ~90% reduction | ~60-80% |
| **DeepSeek** | Automatic prefix caching (no client markers needed) | ~50-80% reduction | ~50% |
| **OpenAI** | Automatic prefix caching (GPT-4o, GPT-4o-mini) | ~50% reduction | — |

When caching is enabled **and** the bound client is Anthropic-format (`Client.IsAnthropic()` — never URL sniffing), odek:

1. Marks the first system block (`SystemBlock.Cache`)
2. Marks the first user message (`Message.Cache`)

System messages are always sent as separate `SystemBlock`s (one per system row) via `internal/llmclient.toSDKMessages`. OpenAI-format providers never receive `cache_control` markers; they still benefit from prefix-stable system blocks. go-llm-sdk owns Anthropic request headers.

## Enabling

### CLI
```bash
odek run --prompt-caching "Does this work with caching?"
```

The `--prompt-caching` flag is available on `odek run`, `odek repl`, and `odek serve`.

### Config file (`~/.odek/config.json` or `./odek.json`)
```json
{
  "prompt_caching": true
}
```

### Environment variable
```bash
export ODEK_PROMPT_CACHING=true
```

### Programmatic API
```go
agent, err := odek.New(odek.Config{
    Model:         "claude-sonnet-4",
    APIKey:        os.Getenv("ODEK_API_KEY"),
    BaseURL:       "https://api.anthropic.com/v1",
    PromptCaching: true,
})
```

## When to Enable

**Enable for:**
- Anthropic models (Claude family) — explicit cache markers provide the largest benefit
- DeepSeek models — automatic prefix caching works best when the conversation prefix is stable; cache markers are never sent to DeepSeek (they are Anthropic-only)
- Any multi-turn session where the system prompt is large (e.g., AGENTS.md files, loaded skills) — the system prompt is cached after the first iteration

**Disable for:**
- One-shot tasks where the agent runs exactly one iteration
- Providers that don't support caching and have unusual request parsing (safety: unknown fields are ignored by all major providers, but caching is always opt-in via `--prompt-caching`)

## Cache Metrics

When caching is active, odek tracks cache metrics and exposes them through the agent API:

```go
agent.TotalCacheCreationTokens()  // Anthropic: tokens written to cache
agent.TotalCacheReadTokens()      // Anthropic: tokens read from cache hit
agent.TotalCachedTokens()         // OpenAI: cached prompt tokens
```

These are accumulated across all iterations of the most recent run. Zero values mean the provider didn't return cache metrics (either caching is not enabled, or the provider doesn't report them).

### UI Display

Cache stats appear automatically in both the **terminal** and **Web UI** — no extra flags needed:

**Terminal** — gray summary line after each final answer:
```
── 5,432 in · 890 out · 320 stored · 2,100 read
```

Only non-zero metrics are shown. Labels:
- `stored` — tokens written to populate the cache (Anthropic, first request)
- `read` — tokens served from cache hit (Anthropic, subsequent requests)
- `cached` — tokens matched by automatic prefix caching (OpenAI/DeepSeek)

**Web UI** — per-message stats at the bottom of each assistant bubble:
```
⚡ 2.4s  ·  5.4k in  ·  890 out  ·  320 stored  ·  2.1k read
```

And session-level totals in the top bar:
```
∑ 8.2k in  ·  1.8k out  ·  3.4k read  ·  650 stored
```

Hover over any stat for a tooltip explanation.

## How It Works

1. **Before each LLM call**, `internal/llmclient` maps the session DTO through `toSDKMessages`. Cache flags are set only when `PromptCache && IsAnthropic()`:
   - First system block: `SystemBlock.Cache = true`
   - First user message: `Message.Cache = true`

2. **The request is sent** with system text in `ChatRequest.System` (one block per system message) on every call. Markers ride on those blocks only for Anthropic-format clients. Other providers never receive markers; some (OpenAI) would 400 if they were sent.

3. **The response is parsed** for cache metrics from both Anthropic (`cache_creation_input_tokens`, `cache_read_input_tokens`) and OpenAI (`prompt_tokens_details.cached_tokens`).

4. **Metrics are accumulated** across iterations and exposed via the agent API.

## Implementation Details

- Cache markers are applied **per iteration** — the first system block and first user message are marked on every Anthropic-format LLM call. This is safe because the markers reference the same content each time, so the cache is populated on the first iteration and read on subsequent ones.
- The `max_tokens` field is included when set via `odek.Config.MaxTokens`. Some providers (Anthropic) tie caching behavior to this field being present.
- System text always travels as `ChatRequest.System` (SDK `SystemBlock`s), not only when caching is on. That is what keeps the prefix stable for automatic caches on OpenAI-format providers.
