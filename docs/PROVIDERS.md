# Providers & Models

odek v2 talks to LLMs through [`go-llm-sdk`](https://github.com/BackendStack21/go-llm-sdk).
The source of truth for ids, formats, default base URLs, and env keys is the
[SDK provider table](https://github.com/BackendStack21/go-llm-sdk#providers).
This page lists **odek-only** knobs and examples.

Built-in ids: `deepseek` (default), `openai`, `anthropic`, `gemini`, `zai`, `kimi`.

## Quick examples

```bash
# DeepSeek (default)
export DEEPSEEK_API_KEY=sk-...
odek run --model deepseek-v4-flash "task"

# OpenAI
export OPENAI_API_KEY=sk-...
odek run --provider openai --model gpt-4o "task"

# Anthropic
export ANTHROPIC_API_KEY=sk-...
odek run --provider anthropic --model claude-sonnet-4-5 "task"

# Z.ai coding plan
export ZAI_API_KEY=...
odek run --provider zai --model glm-5.3 \
  --base-url https://api.z.ai/api/coding/paas/v4 "task"

# Ollama / any OpenAI-compatible gateway
odek run --provider local --model llama3 \
  --base-url http://localhost:11434/v1
```

Custom ids need `providers.<id>.format` (`openai` / `anthropic` / `gemini`) in
`~/.odek/config.json`. `--base-url` alone on an unknown host registers a
`legacy` OpenAI-format provider (v1 compat, warned). Custom OpenAI-format
gateways (LiteLLM, OpenRouter, vLLM, `legacy`) send `reasoning_effort` when
`--thinking` is set; custom Anthropic-format proxies send the thinking object
plus `anthropic-version`. Gemini-format customs need no extra flags. Built-in
ids keep their SDK registry quirks.

**GPT-5.6 family (`gpt-5.6`, `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-5.6-sol`):**
Chat Completions rejects function tools combined with reasoning. odek (via
go-llm-sdk) sends those turns to `POST /v1/responses` so `thinking=medium`
actually reasons, and a reasoning summary is streamed back. `thinking=disabled`
stays on Chat Completions with `reasoning_effort: none`. The same Responses
route is used for GPT-5.4/5.5 when an explicit thinking level is set.

## odek knobs (not in the SDK)

| Knob | Where |
|---|---|
| `--provider` / `ODEK_PROVIDER` | Select the registry id |
| `--model` / `ODEK_MODEL` | Model id passed to `SDK.Chat` |
| `--base-url` / `ODEK_BASE_URL` | Override **selected** provider URL |
| `--thinking` / `--thinking-budget` | Reasoning depth on `ChatRequest`: `disabled`/`low`/`medium`/`high` (aliases inbound). Budget is Anthropic tokens |
| `prompt_caching` | Anthropic: first system + memory (`CacheControl`) + first-user `Message.Cache` + last-tool `ToolDef.Cache`. OpenAI-format: prefix-stable separate system messages (no `cache_control`) |
| `llm.request_timeout_seconds` | Default 300. No per-model auto-timeout. |
| `llm.stream_idle_timeout_seconds` | SSE idle watchdog (default 300, floor 5) |
| `llm.context_window` | Trim budget override. Else last-resort table for shipped ids, else `ListModels`, else 0 |

v1 `base_url` + `api_key` without `provider` still work: the host is inferred
(`api.deepseek.com` → `deepseek`, …) or registered as `legacy`. See
[MIGRATION.md](MIGRATION.md).

## Context windows (last-resort table)

Used only when `llm.context_window` is unset and `ListModels` did not report a
window. **No auto-thinking and no auto-timeout.**

| Prefix | Tokens |
|---|---|
| `deepseek-v4` (pro, flash, v4.1-*) | 1M |
| `deepseek-` (v3 chat / reasoner) | 128K |
| `glm-5.3` / `glm-5.2` | 1M |
| `glm-5-turbo` | 200K |
| `glm-` | 128K |
| `kimi-` / `k3-256k` | 256K |
| `k3` | 1M |
| `gpt-6` / `gpt-5.6` / `gpt-5.5` / `gpt-5.4` | 1.05M |
| `gpt-5.4-mini` / `gpt-5.4-nano` / `gpt-5` | 400K |
| `gpt-4.1` | 1.05M |
| `gpt-4o` / `gpt-4-turbo` | 128K |
| `o4` / `o3` / `o1` | 200K |

## Temperature polarity

odek `Config.Temperature` / `--temperature`:

| Value | Wire |
|---|---|
| `0` (default) | send explicit 0 (deterministic) |
| `< 0` | omit (provider default) |
| `> 0` | send that value |

The SDK uses the opposite zero: odek maps `0 → -1` at the call boundary.

## Project config

`./odek.json` cannot set `provider`, `providers`, `base_url`, `api_key`, or `llm`.
A cloned repo must not redirect inference. Keys live in `~/.odek/config.json`,
`~/.odek/secrets.env`, env, or CLI.
