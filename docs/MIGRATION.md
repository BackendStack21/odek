# Migrating to odek v2

## Batch execution tool retirement

The batch execution tools have been removed from all built-in registries,
including MCP and subagents. There are no compatibility aliases or opt-in
legacy implementations. `delegate_tasks` and `bg_*` remain available for
isolated agent work and background process management.

| Removed tool | Replacement |
|---|---|
| `parallel_shell` | Individual `shell` calls |
| `batch_patch` | Individual `patch` calls |
| `batch_read` | Individual `read_file` calls |
| `multi_grep` | Individual `search_files` calls |
| `http_batch` | Individual `http_request` calls |

Emit separate calls for independent operations in one model response. The
loop schedules supported independent calls under `max_tool_parallel`
(default 4), preserves input order for tool-result messages, and orders
conflicting file operations. Stateful browser calls retain response order.
Shell operations remain conservative barriers
because the runtime cannot establish their independence. Models emitting one
call at a time still work, with less overlap.

`batch_patch` previously applied edits sequentially and stopped on the first
failure, preserving edits already applied. Separate `patch` calls are not a
transaction and do not provide batch-wide early-stop behavior. Submit dependent
edits in successive model responses when a failure must stop later work.

Each individual call consumes one `limits.max_tool_calls` unit and gets its own
call ID, approval classification, outcome, and events. Ten reads now consume
ten calls instead of one batched call. Review tight execution budgets without
automatically increasing them.

`checksum` and `head_tail` now accept one file per call:

```json
{"path":"README.md","algorithm":"sha256"}
```

```json
{"path":"README.md","mode":"head","lines":10}
```

Their result envelopes retain `results`, containing one entry. Legacy `files`
inputs without `path` are rejected. These tools have no internal worker pool.

`http_request` accepts one URL, optional method (default GET), and headers:

```json
{"url":"https://example.com","method":"HEAD","headers":{"Accept":"text/html"}}
```

It returns `{url,status,content_length?,error?}` and discards response bodies.
Use `browser` to read page content. The request keeps the 30-second timeout,
SSRF/DNS-rebinding guard, redirect policy checks, proxy refusal, and untrusted
network-error boundary. GET/HEAD/OPTIONS calls can overlap; other methods are
conservative scheduling barriers.

Update `tools.enabled`, `tools.disabled`, CLI `--tool`/`--no-tool`, environment
filters, capability profiles, operator identity prompts, and skills referencing
retired tools. Unknown filter names are ignored; they are not translated to
replacements. Review restrictive filters explicitly so a retired denylist
entry does not leave its replacement enabled unintentionally. The shipped
profile template uses the replacements.

The Web UI no longer has specialized argument previews, result cards, or
renderers for these five tools, including old session transcripts. Generic
raw/JSON rendering remains available. Stored historical tool names retain
their memory-provenance classification to prevent unsafe replay.

## Danger-policy defaults (v2.15.1)

Two default changes, aimed at the out-of-box experience:

1. **`network_egress` now defaults to `allow`** (was `prompt`). Outbound
   commands like `curl`, `wget`, `git push` run unprompted. SSRF/dial-guard,
   redirect re-classification, and the `install` gate are unaffected.
   To restore the old behavior:
   ```json
   "dangerous": { "classes": { "network_egress": "prompt" } }
   ```
2. **`odek init --global` no longer writes `"action": "prompt"`** into
   `~/.odek/config.json`. That global override replaced *every* per-class
   default — including `safe` and `local_write`, which are supposed to run
   unprompted — so an init-produced config prompted on every command and
   downgraded `unknown` from deny to prompt. New configs rely on the built-in
   per-class defaults.

Existing configs are untouched: an explicit `"action"` in your config still
wins over the new defaults. Note one knock-on: scheduled jobs follow the
global egress default too — gate them via `schedules.dangerous.classes` if
unattended egress matters to you. Sub-agent profiles with
`max_risk: "network_egress"` are unaffected (profile caps are class-rank
based, not action based), but operator configs pinning explicit egress
actions keep what they set.

## Removed tools (v2.13+)

The `tr`, `sort`, `count_lines`, and `word_count` tools were removed as
shell-duplicate conveniences (see the token-efficiency change). Replacements:

- `tr` → `patch` for file edits, or in-model transforms for inline text
- `sort` → `shell` (`sort` command)
- `count_lines` / `word_count` → `file_info` (size), `read_file` (total_lines),
  or `shell` (`wc`)

Old sessions that contain calls to these tools still render and replay
correctly; internal taint classification retains the removed names on purpose.

## LLM client (v2.0.0)

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
    "request_timeout_seconds": 300,
    "stream_idle_timeout_seconds": 300,
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
| `ODEK_API_KEY` | Override for the **selected** provider only. |

DeepSeek-only leftover: when `provider` is `deepseek`, `ODEK_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`. That hop does **not** apply to `--provider openai`.

## Deleted model profiles

`KnownProfiles`, `LookupProfile`, and `ModelProfile` are gone. v1 auto-set thinking and timeouts from the model name (`deepseek-v4-pro` → thinking on, 180s). v2 does not:

- Thinking: set `--thinking medium` (or `low`/`high`/`disabled`; `enabled` still works as an alias for `medium`).
- Timeout: default **300s** for every model. Raise with `llm.request_timeout_seconds`.
- Context window: `llm.context_window` → last-resort table for shipped ids (`deepseek-v4*` 1M, `deepseek-` 128K, GLM/Kimi/OpenAI prefixes) → `ListModels` → else 0 (no trim).

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

Cache usage fields come from the SDK (`Usage.Cache*`). Pin **go-llm-sdk v0.3.3+** so GPT-5.6 tools+reasoning stays on (`POST /v1/responses`), cache-token parsing remains honest, and DeepSeek thinking-mode tool loops replay `reasoning_content` instead of dropping it (v0.3.2 and older lose the key on turns where the provider elided reasoning, and the loop 400s from then on).
