# Migrating to odek v2

v2.0.0 replaces the local OpenAI-compatible HTTP client (`internal/llm`) with
[`github.com/BackendStack21/go-llm-sdk`](https://github.com/BackendStack21/go-llm-sdk).
LLM identity is now **provider id + model**, not a free-floating `base_url`.

Existing `~/.odek/config.json` files keep working: a bare `base_url` / `api_key`
is mapped to a provider (with a once-per-process warning). Rewrite when convenient.

## Config rewrite

v1:

```json
{
  "model": "deepseek-v4-flash",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "${ODEK_API_KEY}"
}
```

v2:

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "providers": {
    "deepseek": { "api_key": "${DEEPSEEK_API_KEY}" }
  },
  "llm": {
    "request_timeout_seconds": 120,
    "stream_idle_timeout_seconds": 120,
    "context_window": 0
  }
}
```

`odek init --global` writes the v2 template.

### Other providers

| Goal | v2 |
|---|---|
| OpenAI | `"provider": "openai"` + `providers.openai.api_key` / `OPENAI_API_KEY` |
| Anthropic | `"provider": "anthropic"` + `ANTHROPIC_API_KEY` |
| Gemini | `"provider": "gemini"` + `GEMINI_API_KEY` / `GOOGLE_API_KEY` |
| Z.ai coding plan | `"provider": "zai"` + `"providers": {"zai": {"api_key": "${ZAI_API_KEY}", "base_url": "https://api.z.ai/api/coding/paas/v4"}}` |
| Ollama / custom OpenAI gateway | `"provider": "local"` + `"providers": {"local": {"format": "openai", "base_url": "http://localhost:11434/v1", "api_key": "local"}}` |

See [PROVIDERS.md](PROVIDERS.md) and the [SDK provider table](https://github.com/BackendStack21/go-llm-sdk#providers).

## CLI / env

| v2 | Notes |
|---|---|
| `--provider` / `ODEK_PROVIDER` | New. Default `deepseek`. |
| `--model` / `ODEK_MODEL` | Unchanged. |
| `--base-url` / `ODEK_BASE_URL` | Override for the **selected** provider only. |
| `--api-key` / `ODEK_API_KEY` | Override for the **selected** provider only. |

DeepSeek-only leftover: when `provider` is `deepseek`, `ODEK_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`. That hop does **not** apply to `--provider openai`.

## Deleted model profiles

`KnownProfiles`, `LookupProfile`, and `ModelProfile` are gone. v1 auto-set thinking and timeouts from the model name (`deepseek-v4-pro` → thinking on, 180s). v2 does not:

- Thinking: set `--thinking enabled` (or config `thinking`) when you want it.
- Timeout: default **120s** for every model. Raise with `llm.request_timeout_seconds`.
- Context window: `llm.context_window` → `ListModels` → last-resort table for shipped ids (`deepseek-v4-flash` 128K, `deepseek-v4-pro` 1M, GLM/Kimi prefixes) → else 0 (no trim).

`ProfileLabel` now returns the model id.

`GET /api/models` is the picker catalog: provider `ListModels` plus the configured model (`current: true`). `GET /api/profiles` is removed.

## DeepSeek default URL

The SDK default is `https://api.deepseek.com` (no `/v1`). Operators who pinned `https://api.deepseek.com/v1` keep it via `providers.deepseek.base_url` or `ODEK_BASE_URL`.

## Sessions

On-disk messages stay the **v1 nested** `tool_calls[].function` shape so existing `~/.odek/sessions` load without a rewrite. `thinking_signature` is additive (`omitempty`). v1 odek can still read a session that never stored a signature.

Unknown roles are kept on disk and dropped **with their assistant+tool group** at the call boundary (not rewritten on Load).

New sessions persist `provider`. `odek continue` reloads config with that id plus the stored model. Pre-v2 files with an empty `provider` keep the operator's current default provider (possible model/provider mismatch until the session is recreated).

## Library embedders

```go
agent, err := odek.New(odek.Config{
    Provider: "deepseek",
    Model:    "deepseek-v4-flash",
    APIKey:   os.Getenv("DEEPSEEK_API_KEY"),
    // BaseURL is an optional selected-provider override.
})
```

`Config.DeltaHandler` takes `llmclient.Delta` (SDK `Delta`). Do not persist SDK `Message` types — they have no JSON tags.

## Project config trust

`./odek.json` still cannot redirect inference. Ignored with a warning: `provider`, `providers`, `base_url`, `api_key`, `llm`, plus the existing operator-only sections.

## Sub-agents

`delegate_tasks` stamps `provider`, `model`, and selected `base_url` into the task envelope. The FD-handed API key applies to **that** provider. A child must not default to DeepSeek with a Z.ai key.

## Cache / cost budgets

Cache usage fields come from the SDK (`Usage.Cache*`). Pin **go-llm-sdk v0.2.1+** so cache-token parsing and cost caps stay honest (v0.2.0 lacked those fields).
