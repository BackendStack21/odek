# Configuration

`odek uses a **layered configuration system** with convention over configuration — opt-in files and environment variables, no mandatory setup.

## Priority chain

Each layer overrides the one below it. Unset fields inherit from the layer below:

```
0.  ~/.odek/secrets.env     ← Auto-loaded into process environment on startup
1.  ~/.odek/config.json     ← Global defaults (shared across projects)
2.  ./odek.json             ← Project-specific overrides
3.  ODEK_* env vars         ← Runtime/environment overrides
4.  CLI flags               ← Explicit invocation (highest priority)
```

Layer 0 is unique: it does not hold config fields directly. Instead it injects
`KEY=VALUE` pairs into the process environment so they're available for:

- **Layer 1–2** `${VAR}` substitution in config files
- **Layer 3** `ODEK_*` env var lookups (e.g. `ODEK_PROVIDER`, `ODEK_MODEL`)
- **Provider key env vars** (`DEEPSEEK_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `ZAI_API_KEY`, …)

## Config files

### Global defaults (`~/.odek/config.json`)

Shared across all projects:

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
  },
  "thinking": "",
  "max_iterations": 90,
  "sandbox": true,
  "interaction_mode": "engaging",
  "no_color": false,
  "no_agents": false,
  "max_tool_parallel": 4,
  "max_concurrency": 3,
  "trusted_proxies": [],
  "tool_progress": "all",
  "tool_progress_cleanup": true,
  "system": ""
}
```

> **Sandbox default (changed):** when no layer sets `sandbox`, `odek run` /
> `odek repl` / `odek serve` default it **on**, degrading loudly to
> unsandboxed only when Docker is unavailable or a project
> `Dockerfile.odek`/sandbox knob lacks approval (`serve` hard-fails instead).
> Opt out explicitly with `--no-sandbox`, `ODEK_NO_SANDBOX=1`, or `"sandbox": false`
> in trusted config. `odek continue` pins the session's stored sandbox bit and
> does not accept `--no-sandbox`. Override the pin with `ODEK_SANDBOX=false` or
> trusted `"sandbox": false` (`ODEK_NO_SANDBOX=1` does not). Make any fallback fatal with
> `ODEK_REQUIRE_SANDBOX=1`. An explicit `--sandbox` keeps the hard-fail-on-error
> behavior.
>
> Sandbox resource keys (`sandbox_image`, `sandbox_network`, `sandbox_readonly`,
> `sandbox_memory`, `sandbox_cpus`, `sandbox_user`) follow the standard
> `ODEK_*` env / CLI-flag patterns. Two keys are **config-file-only**:
> `sandbox_env` (object of extra environment variables injected into the
> container; supports `${VAR}` interpolation against host env) and
> `sandbox_volumes` (array of extra `host:container` mounts; host paths must
> resolve inside the working directory — outside paths, `..`, and symlinks are
> rejected). Both are sandbox overrides: a project-level `./odek.json`
> that sets them triggers explicit operator approval. See
> [SANDBOXING.md](SANDBOXING.md) for the full table and security notes.
>
> `ODEK_SUPPRESS_SANDBOX_WARNING=1` silences the one-time "sandbox disabled"
> stderr notice on unsandboxed runs (CI pipelines already suppress it when
> stderr is not a TTY). **Not recommended**: the warning is the only signal
> that the agent has full host access — silence it only for scripted runs
> where stderr noise breaks output parsing.

### Project overrides (`./odek.json`)

Same schema as global. Only set the fields you want to override:

```json
{
  "model": "gpt-4o",
  "max_iterations": 30
}
```

> **Security note:** The following fields cannot be set in `./odek.json` because a malicious repository could use them to steal secrets, poison the system prompt, disable safety policy, or redirect data to attacker-controlled backends:
>
> - `provider` / `providers` — use `~/.odek/config.json`, `ODEK_PROVIDER`, or `--provider`
> - `llm` — request timeout, stream idle timeout, and context window are operator-only
> - `base_url` — v1 alias; prefer `providers.<id>.base_url` or `ODEK_BASE_URL` / `--base-url` (selected provider only)
> - `api_key` — v1 alias; prefer `providers.<id>.api_key` or the provider env key in `~/.odek/secrets.env` (`DEEPSEEK_API_KEY`, `ZAI_API_KEY`, …). `ODEK_API_KEY` is a selected-provider override only
> - `system` — use `~/.odek/config.json`, `ODEK_SYSTEM`, or `--system`
> - `dangerous` — use `~/.odek/config.json`
> - `embedding` / `memory` / `sessions` / `skills.dirs` / `skills.embedding` / `web_search` / `transcription` / `vision` — use `~/.odek/config.json`
> - `telegram` — use `~/.odek/config.json` or `ODEK_TELEGRAM_*` env vars
> - `guard` — use `~/.odek/config.json` or `ODEK_GUARD_*` env vars
> - `trusted_proxies` — use `~/.odek/config.json` or `ODEK_TRUSTED_PROXIES`
> - `subagent` / `profiles` / `maintenance` — use `~/.odek/config.json`
>
> If any of these appear in `./odek.json`, odek ignores them and prints a warning.
>
> The `limits` section is neither rejected nor freely settable in `./odek.json`: a project may only *lower* globally-set execution budgets, never raise or disable them. See [Execution budgets](#execution-budgets-limits).

Both files are optional. Missing files are silently ignored. String values support `${VAR}` environment variable substitution — useful for API keys without plaintext storage.

## Secrets file (`~/.odek/secrets.env`)

Auto-loaded on every `odek` invocation before any config file or env var is read.
Each `KEY=VALUE` line is injected into the process environment via `os.Setenv`.

```
DEEPSEEK_API_KEY=sk-...
GITHUB_TOKEN=ghp_...
```

Rules:
- **File format:** `KEY=VALUE` — one per line, no `export` keyword needed
- **Blank lines and `#` comments** are skipped
- **Existing env vars are NOT overwritten** — if `DEEPSEEK_API_KEY` is already in the environment, the file is ignored for that key
- **Missing/unreadable file** is silently ignored (not an error)
- **Permissions:** keep `0600` (`chmod 600 ~/.odek/secrets.env`)

This lets you keep secrets out of config files entirely:

```json
// ~/.odek/config.json — no plaintext secrets
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "providers": {
    "deepseek": { "api_key": "${DEEPSEEK_API_KEY}" }
  }
}
```

## Environment variables

Most config knobs have a `ODEK_*` counterpart:

| Variable | Maps to | Type |
|----------|---------|------|
| `ODEK_PROVIDER` | `--provider` | string |
| `ODEK_MODEL` | `--model` | string |
| `ODEK_BASE_URL` | `--base-url` | string |
| `ODEK_API_KEY` | selected-provider key override (v1 alias) | string |
| `ODEK_THINKING` | `--thinking` | string |
| `ODEK_MAX_ITER` | `--max-iter` | int |
| `ODEK_SANDBOX` | `--sandbox` | bool |
| `ODEK_INTERACTION_MODE` | `--interaction-mode` | string |
| `ODEK_NO_COLOR` | `--no-color` | bool |
| `ODEK_NO_AGENTS` | `--no-agents` | bool |
| `ODEK_SYSTEM` | `--system` | string |
| `ODEK_PROMPT_CACHING` | `prompt_caching` | bool |
| `ODEK_ANNOUNCE_BUDGET` | `announce_budget` | bool |
| `ODEK_STREAM` | `stream` | bool |
| `ODEK_COMPACTION` | `compaction` | bool |
| `ODEK_STREAM_IDLE_TIMEOUT_SECONDS` | `llm.stream_idle_timeout_seconds` | int |
| `ODEK_SANDBOX_IMAGE` | `--sandbox-image` | string |
| `ODEK_SANDBOX_NETWORK` | `--sandbox-network` | string |
| `ODEK_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
| `ODEK_SANDBOX_MEMORY` | `--sandbox-memory` | string |
| `ODEK_SANDBOX_CPUS` | `--sandbox-cpus` | string |
| `ODEK_SANDBOX_USER` | `--sandbox-user` | string |
| `ODEK_APPROVE_PROJECT_SANDBOX` | — | bool | approve project-level `./odek.json` sandbox config and implicit `Dockerfile.odek` builds without prompting |
| `ODEK_SANDBOX_BUILD_NETWORK` | — | bool | allow networked `Dockerfile.odek` builds (default: builds run with `--network=none`) |
| `ODEK_MAX_CONCURRENCY` | `max_concurrency` | int |
| `ODEK_TRUSTED_PROXIES` | `trusted_proxies` | string (comma-separated IPs/CIDRs) |
| `ODEK_MEMORY_EXTENDED_ENABLED` | `--memory-extended-enabled` | bool |
| `ODEK_MEMORY_EXTENDED_MAX_SIZE_MB` | `--memory-extended-max-size-mb` | int |
| `ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS` | `--memory-extended-atom-max-chars` | int |
| `ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS` | `--memory-extended-memory-budget-chars` | int |
| `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED` | — | bool |
| `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE` | — | float |
| `ODEK_MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED` | — | bool |
| `ODEK_MEMORY_EXTENDED_NUDGE_MAX_PER_DAY` | — | int |
| `ODEK_MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS` | — | int |
| `ODEK_MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS` | — | int |
| `ODEK_GUARD_PROVIDER` | `--guard-provider` | string |
| `ODEK_GUARD_URL` | `--guard-url` | string |
| `ODEK_GUARD_BATCH_URL` | `--guard-batch-url` | string |
| `ODEK_GUARD_LONG_URL` | `--guard-long-url` | string |
| `ODEK_GUARD_SOCKET_PATH` | `--guard-socket-path` | string |
| `ODEK_GUARD_THRESHOLD` | `--guard-threshold` | float |
| `ODEK_GUARD_TIMEOUT_SECONDS` | `--guard-timeout` | int |
| `ODEK_GUARD_FALLBACK_TO_LOCAL` | `--guard-fallback` / `--guard-no-fallback` | bool |
| `ODEK_GUARD_SCAN_MEMORY` | `--guard-scan-memory` / `--guard-no-scan-memory` | bool |
| `ODEK_GUARD_SCAN_SYSTEM_PROMPT` | `--guard-scan-system-prompt` / `--guard-no-scan-system-prompt` | bool |
| `ODEK_GUARD_SCAN_MCP_DESCRIPTIONS` | `--guard-scan-mcp` / `--guard-no-scan-mcp` | bool |
| `ODEK_GUARD_SCAN_SKILLS` | `--guard-scan-skills` / `--guard-no-scan-skills` | bool |
| `ODEK_GUARD_SCAN_TOOL_OUTPUTS` | `--guard-scan-tool-outputs` / `--guard-no-scan-tool-outputs` | bool |
| `ODEK_GUARD_SCAN_TELEGRAM` | `--guard-scan-telegram` / `--guard-no-scan-telegram` | bool |

## API key fallback order

Selected provider, then leftovers. After resolution, provider key env vars are **unset** from the process environment (the SDK keeps the key in memory; `printenv` from tools does not see it). The scrubbed values are remembered in-process (bound to the `HOME` they were captured under), so a second `LoadConfig` in the same process, such as the logging pre-load that runs before the command's own load, still resolves a key supplied only through the real environment.

1. Explicit `api_key` / `providers.<id>.api_key` (after `${VAR}` expansion)
2. `ODEK_API_KEY`
3. The selected provider's env key (`DEEPSEEK_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`/`GOOGLE_API_KEY`, `ZAI_API_KEY`, `KIMI_API_KEY`/`MOONSHOT_API_KEY`)
4. DeepSeek-only leftover: `OPENAI_API_KEY` when `provider` is `deepseek`
5. `legacy` (v1 unknown `base_url`): `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`

## Prompt-injection guard

Odek ships a pluggable prompt-injection guard subsystem that can be applied to high-trust surfaces. The guard is **defense-in-depth**: the fast, local rule-based scan (`danger.ScanInjection`) always runs first, and an optional external sidecar (`go-prompt-injection-guard`) can provide a second opinion when configured.

The guard is **off by default** in the sense that no sidecar is needed; the local scan always runs. To enable the optional sidecar, set `provider: "piguard"` and point `url` at the sidecar endpoint.

### Configuration

```json
{
  "guard": {
    "provider": "local",
    "url": "http://127.0.0.1:8080/detect",
    "batch_url": "",
    "long_url": "",
    "socket_path": "",
    "threshold": 0.9,
    "timeout_seconds": 5,
    "fallback_to_local": true,
    "max_text_length": 0,
    "scan": {
      "memory": true,
      "system_prompt": true,
      "mcp_descriptions": true,
      "skills": true,
      "tool_outputs": false,
      "telegram": false
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"local"` | `"local"` uses the built-in rule scan; `"piguard"` uses an external sidecar |
| `url` | `""` | Single-text detection endpoint (e.g. `http://127.0.0.1:8080/detect`) |
| `batch_url` | `""` | Batch detection endpoint; if unset, derived from `url` by substituting the endpoint path |
| `long_url` | `""` | Long-text detection endpoint; if unset, derived from `url` by substituting the endpoint path |
| `socket_path` | `""` | Unix socket of the piguard daemon (alternative to `url`); speaks the daemon's native newline-delimited JSON protocol directly, no HTTP gateway needed |
| `threshold` | `0.9` | Confidence above which an `INJECTION` verdict is treated as injected. The sidecar score is the confidence of the predicted label, so the threshold never applies to `BENIGN` results |
| `timeout_seconds` | `5` | Per-request timeout |
| `fallback_to_local` | `true` | If the sidecar fails, fall back to the local rule scan |
| `max_text_length` | `0` | Truncate text sent to the sidecar; `0` means no limit. The local scan still sees the full text |

### Scan scopes

| Scope | Default | Surfaces covered |
|-------|---------|------------------|
| `memory` | `true` | `memory` add/replace/consolidate, legacy facts, auto-extracted facts, session buffer, and Extended Memory atom extraction/addition/recall/user-model inference |
| `system_prompt` | `true` | `~/.odek/IDENTITY.md`, explicit `--system` / `ODEK_SYSTEM`, and project-level `AGENTS.md` |
| `mcp_descriptions` | `true` | MCP server tool descriptions supplied via `tools/list` |
| `skills` | `true` | Skill bodies at load time and import |
| `tool_outputs` | `false` | External tool outputs wrapped as `<untrusted_content_*>` (warning-only scan) |
| `telegram` | `false` | Telegram photo captions and voice transcripts before injection |

When a scope is not explicitly set, the core surfaces (`memory`, `system_prompt`, `mcp_descriptions`, `skills`) default to `true`; the optional expansion surfaces default to `false`. Regardless of scope, the fast local rule scan always runs on every guarded surface — the scope only toggles the sidecar second opinion.

### Examples

```bash
# Run with a local piguard sidecar
odek run --guard-provider piguard --guard-url http://127.0.0.1:8080/detect "task"

# Enable the optional skill and Telegram guards via environment
ODEK_GUARD_PROVIDER=piguard \
ODEK_GUARD_URL=http://127.0.0.1:8080/detect \
ODEK_GUARD_SCAN_SKILLS=true \
ODEK_GUARD_SCAN_TELEGRAM=true \
odek run "task"
```

> **Security note:** The entire `guard` section is rejected from project-level `./odek.json`. A malicious repository cannot disable the local scan or redirect memory/system-prompt content to an attacker-controlled endpoint.

## Parallel tool execution

When a model emits multiple tool calls in one response (`tool_calls` array with N entries), odek executes them **concurrently** in goroutines bounded by a semaphore.

| Field | Default | Env var | Description |
|-------|---------|---------|-------------|
| `max_tool_parallel` | `4` | — | Max concurrent tool calls per iteration. 0 = default 4. Set to 1 for sequential execution. |

I/O-bound tools (read_file, search_files, shell) benefit most — latency drops from `sum(latencies)` to `max(latency)`.

**Approval gate:** When an approver is configured and the LLM returns multiple tool calls, a single batch approval prompt is shown before any tool executes. If approved, all tools run in parallel. If denied, no tools run.

## Core run toggles

Top-level execution knobs. Every one also exists as a CLI flag and an `ODEK_*` env var; the table below is the config-file form.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `deepseek` | go-llm-sdk registry id (`deepseek`, `openai`, `anthropic`, `gemini`, `zai`, `kimi`, or a custom id). See [Providers](PROVIDERS.md) |
| `providers` | `{}` | Per-id `api_key` / `base_url` / `format` overrides. `${VAR}` expands. Operator-only. `include_reasoning` (boolean, default `true` on OpenAI-format custom providers) opts a strict gateway out of the `include_reasoning` wire flag — the SDK has no automatic fallback for it |
| `model` | `deepseek-v4-flash` | LLM model ID. No auto-thinking or auto-timeout from the name |
| `base_url` | SDK default for `provider` | Selected-provider URL override (v1 alias). DeepSeek default is `https://api.deepseek.com` (no `/v1`) |
| `thinking` | `""` (omit) | Reasoning depth: `disabled`, `low`, `medium`, `high`. Aliases: `enabled`/`on` → `medium`, `off` → `disabled`, `mid` → `medium`, `max` → `high`. Set explicitly — not inferred from the model name |
| `max_iterations` | `90` | Max think→act cycles per run |
| `stream` | `true` | Stream reasoning and answer text to the terminal / Web UI as it arrives (`ODEK_STREAM`, `--stream` / `--no-stream`). Telegram does not print incrementally (no DeltaHandler). See [STREAMING.md](STREAMING.md) |
| `prompt_caching` | `true` | Provider prompt-caching markers — Anthropic endpoints get explicit markers; other providers are unaffected (see [CACHING.md](CACHING.md)). Disable with `ODEK_PROMPT_CACHING=false` / `--no-prompt-caching` |
| `announce_budget` | `true` | Parent-run 50/75/90% budget-awareness hints (iteration, wall-clock, tool-calls, tokens, cost). Distinct from [`subagent.announce_budget`](#sub-agent-configuration). Disable with `ODEK_ANNOUNCE_BUDGET=false` / `--no-announce-budget`. Wording stays “stay on plan / consolidate / deliver” — never skip approvals |
| `interaction_mode` | `"engaging"` | Tool-call presentation: `"engaging"` (emoji narration) · `"enhance"` (per-tool narrated messages) · `"verbose"` (raw tool names, args, results) · `"off"` (no progress output, clean answer only) |
| `no_color` | `false` | Disable colored terminal output |
| `no_agents` | `false` | Skip loading project `AGENTS.md` |
| `system` | built-in | Override the system-prompt identity layer — name/mission/persona (operator-only; rejected from project configs). The invariant security pillar is always composed on top and cannot be overridden. |

With `provider: "deepseek"`, the model picker includes `deepseek-flash` and
`deepseek-pro`, each with a 1,000,000-token context fallback. These IDs also work
with `--model` and runtime model switching. Flash is sent directly; the Pro
shortcut sends `deepseek-v4-pro` to DeepSeek. Existing versioned IDs remain valid.
Custom providers receive model IDs unchanged. See
[DeepSeek's model documentation](https://api-docs.deepseek.com/quick_start/pricing/)
for current provider routing and availability.

## LLM client (`llm`)

Tunes the shared LLM client (streaming and buffered calls share one retry policy):

```json
{
  "llm": {
    "request_timeout_seconds": 300,
    "stream_idle_timeout_seconds": 300,
    "context_window": 0
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `request_timeout_seconds` | `300` | Per-request wall-clock budget for every model. No per-model auto-timeout. `0` keeps the default. Thinking/reasoning models are slow to first byte — 300s avoids the old 120s "provider timeout" on WebSocket clients. |
| `stream_idle_timeout_seconds` | `300` | Time between SSE events (keepalives count) before the stream is dropped and retried. Thinking models can spend minutes before their first event — raise it further if long-thinking models still hit `stream idle` errors. Floor 5s; `0` keeps the default. Eight retry attempts with jittered exponential backoff (and `Retry-After` honor) are shared with the buffered client; billing/quota errors fail fast. |
| `context_window` | `0` | Trim-budget override. `0` means discover via `ListModels`, then the last-resort table for shipped ids, else no trim. |

## Dangerous-operations policy (`dangerous`)

The `dangerous` section is the operator's safety policy for tool calls. Every shell command, file write, and network operation is classified into a risk class, and the class maps to an action. Project-level `./odek.json` cannot set this section.

| Field | Default | Description |
|-------|---------|-------------|
| `classes` | see below | Map of risk class → action. Only non-default overrides need to be set |
| `allowlist` | `[]` | Command strings that are **always allowed** regardless of classification. **Exact match** of the whole command line (after trimming), so `go test ./...` does not allow `go test ./... && make deploy`; takes priority over `denylist`. It cannot authorize a `blocked` operation, and a command over 64 KiB is denied before the list is consulted |
| `denylist` | `[]` | Command strings that are **always denied** regardless of classification. **Token-prefix match** at every command position the line would run — see [Denylist matching](#denylist-matching) below. Entry tokens must equal the leading tokens of the command, so `git push` matches `git push origin` but not `git push-notes`; it is no longer a raw string prefix |
| `action` | *(per-class defaults)* | Global default action for **all** classes — `"allow"` (everything runs unprompted) or `"deny"` (lockdown: nothing runs unless explicitly allowed). Per-class `classes` entries still win |
| `non_interactive` | `"read_only"` | What happens to prompt-class operations when no TTY is available (CI, headless, piped input): `"read_only"` (inspection proceeds; writes/exec/egress denied), `"deny"` (block all prompted operations), `"allow"` (run everything — not recommended). Under `read_only` a shell command proceeds only when it classifies `safe`; a native read tool (`read_file`, `search_files`, `glob`, `file_info`, `tree`, `diff`, `json_query`, `checksum`, `head_tail`, `base64`, `session_search`, …) proceeds when its target ranks below `system_write`. The carve-out is keyed on the native tool name only — never on the free-text description the model supplies for a shell command. An invalid explicit value fails closed to `deny` |
| `strip_secrets_env_children` | `false` | Remove `secrets.env` names from the environment of **host-mode** child processes spawned by `shell` and background jobs. Default `false`: children inherit, so workflows that legitimately need credentials in shell children (`gh`, `curl`) keep working. Sub-agent and MCP stdio spawns strip unconditionally regardless of this knob; sandbox-mode containers never see host secrets |
| `rest_approval_friction` | `false` | Server-side friction for the headless **REST approval bridge** (`POST /api/runs/{id}/approvals/{aid}`): `approve` and `trust` decisions must repeat the action in a typed `confirm` field, mirroring the TTY friction. Default `false`: auto-approving clients keep the single-field contract. `deny` stays single-field — friction guards accidental approvals, not denials |

Risk classes and their built-in default actions. In severity order, lowest to highest: `safe` < `local_write` < `install` < `network_egress` < `network_upload` < `code_execution` < `system_write` = `unread_exec` < `persistence` < `unknown` < `destructive` < `blocked`. A command carries every class it triggers and the strictest action wins (deny > prompt > allow), so an allowed class cannot hide a denied one. The ordering is what a sub-agent `max_risk` cap uses.

| Class | Default | Covers |
|-------|---------|--------|
| `safe` | `allow` | Read-only inspection (`ls`, `cat`, `tree`, …) |
| `local_write` | `allow` | Writes inside the working directory |
| `system_write` | `prompt` | Writes outside the workspace: shell rc files, `~/.ssh`, `~/.odek`, system paths; git data-loss verbs (`reset --hard`, `clean -fdx`, force pushes, `push --mirror`/`--delete`); environment dumps (`env`, `printenv`, and bare `export`/`declare`/`typeset`); `export` of exec-controlling variables (`PATH`, `LD_PRELOAD`, `GIT_CONFIG_*`, `JAVA_TOOL_OPTIONS`, …); and **secret reads** — a reference to a secret-shaped environment variable (`$API_TOKEN`, `${DB_PASSWORD}`, indirect `${!v}`, `printenv NAME`, `os.environ[...]`, `process.env.X`) or a read or write of a credential file (`.env` other than examples, `credentials.json`, `*.pem`, `*.key`, `id_*`, `.netrc`, `.npmrc`, kubeconfig, `terraform.tfstate`, anything under `secrets/`, `credentials/`, `.aws/`, `.ssh/`). The current user's own home is not a system path: ordinary files below it (including `/root` when odek runs as root) are `local_write`, while rc files, credential directories and `~/.odek` trust anchors still escalate |
| `persistence` | `prompt` | Deferred-execution writes: shell profiles, git hooks, CI workflows, cron, systemd/launchd, package lifecycle scripts |
| `unread_exec` | `prompt` | Executing a repo-supplied script whose contents were not read in this session — directly, through an interpreter, by `source`, or fed in through a pipe, substitution, `find -exec` or a program-file option (`awk -f`, `make -f`, …). A full-file `read_file`, or authoring the file yourself, satisfies it; a partial or failed read does not. Never offered a session-trust shortcut |
| `destructive` | `deny` | Irreversible operations: recursive deletes of broad targets, `find -delete`, raw-device writes (`dd of=/dev/…`), filesystem creation and wipe verbs |
| `network_egress` | `allow` | Outbound network operations (`curl`, `wget`, package fetches). Allowed by default for a friction-free start; set `"prompt"` to gate every egress |
| `network_upload` | `prompt` | Network operations that send local content out or let a remote party in: request bodies read from a file, stdin or a runtime substitution (`curl -d @f`, `-T`, `-F f=@x`, `cat x \| nc`), credentials or client certificates on the command line (`curl -u`/`-n`/`--cert`, `wget --http-password`), mutating methods (`curl -X POST`), local-to-remote transfers (`scp f host:`, `rsync src/ host:dst`, `rclone copy`, `aws s3 cp f s3://`, `gsutil cp`, `gh gist create`), listeners and tunnels (`nc -l`, `ssh -L`/`-R`/`-D`), and DNS lookups whose name is built at run time. Also carries `network_egress`, so denying either class denies the command. Inline literal bodies (`curl -d '{"a":1}' URL`), downloads, and running a remote command (`ssh host ls`) stay plain egress |
| `code_execution` | `prompt` | Arbitrary code execution paths: `bash -c`, `eval`, `source`, pipe-to-shell, interpreter one-liners, `go run`, `nc -e`/`socat EXEC:`, `man -P`, tool options that name a program to run. Ordinary git verbs (`status`, `add`, `commit`, `merge`, `checkout`, `rebase`, `stash`, …) only count when the targeted repository is armed — an executable hook, `core.hooksPath`, `core.fsmonitor`, a filter/diff/merge driver, textconv or an editor config; an unresolvable repository fails closed |
| `install` | `prompt` | Package/tool installation |
| `blocked` | `deny` | Hard-coded malicious patterns |
| `unknown` | `deny` | Unrecognizable commands fail closed: unknown program names, MCP tools, unterminated quotes or constructs, run-time-built program operands, and any command over 64 KiB (`danger.MaxCommandBytes`) |

Valid actions: `allow` (run without prompting) · `prompt` (ask the approver) · `deny` (refuse). `read_only` is a `non_interactive`-only action.

A few `classes` examples:

```json
{ "dangerous": { "classes": { "network_egress": "prompt" } } }
```
Gate every shell-level fetch (the default allows `network_egress`).

```json
{ "dangerous": { "classes": { "network_upload": "deny" } } }
```
Refuse every upload, listener and tunnel outright. Because an upload also carries `network_egress`, denying either class denies the command.

```json
{ "dangerous": { "classes": { "unknown": "prompt", "install": "deny" } } }
```
Soften the fail-closed catch-all to a prompt and forbid installs. `blocked` can never be changed from `deny`; a contradictory or unknown class name is rejected at load time.

Operators who already override classes individually should decide an action for `network_upload`: it is new, defaults to `prompt`, and is not covered by a `network_egress` override.

### Denylist matching

A `denylist` entry is a token sequence, not a string. It matches when its tokens equal the leading tokens of a command the line would run, tried at every command position:

- each `;`/`&&`/`||`/`&` segment and pipe stage, and commands inside loops, conditionals, groups and function bodies;
- the command left after leading `VAR=value` assignments and wrappers (`env`, `command`, `nohup`, `timeout`, `sudo`, `xargs`, `env -S '…'`, `watch '…'`, …), with the program compared by basename (`/usr/bin/git` matches `git`);
- shell `-c` payloads, `eval` operands, `find -exec`/`fd -x` commands, and `$(…)`, backtick and process-substitution bodies;
- the tool's global options stripped before the subcommand for `git`, `docker`/`podman`/`nerdctl`, `kubectl`, `helm`, `gh`, `npm`, `cargo` and `terraform`/`tofu`, so `git -C dir push` matches `git push` and `docker -H host push` matches `docker push`;
- shell variables with a statically known value resolved (`g=git; $g push` matches `git push`); a value built at run time (command output, `read`) is not known and is not matched.

Consequences for writing entries: `rm -rf /` no longer matches `rm -rf /tmp` (the old raw string prefix did); flag spellings are distinct entries, so `rm -fr /` and `rm -r -f /` need entries of their own if you want them blocked; and an entry cannot match across a command separator. The denylist is a backstop for commands you never want run, not a substitute for the risk classes.

```json
{
  "dangerous": {
    "classes": {
      "network_egress": "allow",
      "install": "deny"
    },
    "denylist": ["rm -rf /", "curl attacker.example"],
    "allowlist": ["go test ./..."]
  }
}
```

The schedule-specific override of this policy is documented in [Schedule-specific dangerous policy](#schedule-specific-dangerous-policy); the approval UX (friction, trust shortcuts, batch cards) in [SECURITY.md](SECURITY.md).

## Rolling compaction (`compaction`)

When context trimming drops old conversation turns to stay within the model's context window, those turns are normally lost. With `compaction` enabled (the default), the dropped turns are sketched extractively into a rolling digest immediately (so the next think step is not blocked), then a thinking-off side call replaces that sketch with a model digest on a later iteration if it succeeds.

Compaction and iteration-budget progress summaries are **auxiliary** LLM calls: thinking is disabled, no tools are offered, and output is capped independently of the main think step. The digest uses a fixed skeleton (Task / Done / Decisions / Files/symbols / Errors still open / Next) and re-enters history as untrusted derived context. The extractive placeholder uses the same wrapper and audit ingest.

| Field | Default | Env var | CLI flag | Description |
|-------|---------|---------|----------|-------------|
| `compaction` | `true` | `ODEK_COMPACTION` | `--compaction` / `--no-compaction` | Enable LLM-based rolling compaction of trimmed context. Each compaction costs one extra LLM call per trim. Set to `false` (or pass `--no-compaction`) to disable. |

## Verification pass (`verify`)

Before the loop returns a final answer, an optional verification stage checks it against the original task with a bounded, tool-less, thinking-off side call (the same auxiliary-call machinery as compaction digests). The verifier sees the task (the latest user prompt — the turn the answer replies to — plus a bounded, untrusted-wrapped excerpt of the resources attached to it on surfaces that record the typed prompt separately), the tool calls of that turn paired with bounded excerpts of their results (head + tail of ~1.5 KB per result, ~14 KB for the whole trace, explicit `[… N bytes omitted]` markers where evidence was cut, a note when earlier calls of the turn were trimmed from context, secrets redacted before cutting, each excerpt wrapped as untrusted content), and the candidate answer, and must return a JSON verdict: `{"verdict":"pass|fail","reasons":[...],"missing":[...]}`. Earlier turns of a session never enter the trace; they reach the verifier only as a bounded block of user prompts and final assistant answers (~1 KB per message, ~4 KB total, most recent kept, wrapped as untrusted) so a follow-up answer that restates what an earlier turn established can be judged against it. The side-call prompt therefore stays bounded regardless of session length. The verifier is told that excerpts may be truncated: a claim consistent with the visible excerpt is supported, while contradictions, phantom tool runs and unanswered parts of the task fail.

- **hint** mode (default): a `fail` verdict injects a corrective system hint and the loop re-answers once (`max_cycles`, ceiling 3). Verifier prose is wrapped as untrusted content. If the cycle budget is exhausted, the answer still ships, prefixed with `[Verification failed — answer returned unverified]`.
- **strict** mode: no corrective re-try; a failing answer is returned immediately with the same fixed marker prefix. Verifier prose is never concatenated into the answer.
- Verification calls are budget-accounted like any LLM call; when the remaining budget cannot cover one, the stage is skipped (`skipped: budget`) and the run proceeds normally. A failed verifier call never fails the run — the answer ships unverified.
- Events: `verification_started` / `verification_completed` (data: `verdict`, `cycles_used`, `skipped_reason?`) on the `odek.event/v1` stream.
- WebUI / WebSocket clients get the same events as `runtime_event` frames, an `answer_superseded` frame before a hint-mode retry, and `verified` on the `done` frame (see docs/WEBUI.md, "Superseded drafts and verification").

Disabled by default — an absent `verify` section changes nothing. **Operator-only**: the section is ignored (with a warning) in project-level `./odek.json`.

| Field | Default | Description |
|-------|---------|-------------|
| `verify.enabled` | `false` | Master switch for the final-answer verification stage |
| `verify.mode` | `hint` | `hint` (one bounded corrective re-try), `strict` (marker only), or `off` (stage disabled even when enabled) |
| `verify.model` | *(agent model)* | Optional cheaper model for verification side calls (same provider/credentials as the main agent) |
| `verify.max_cycles` | `1` | Corrective re-try budget; hard ceiling `3` |
| `verify.max_tokens` | *(SDK side-call cap)* | Output cap for the verifier completion |

Sub-agents never verify by default and `delegate_tasks` never enables it implicitly. Explicit opt-in only, under `subagent.verify` with the same fields.

## Planning (`planning`)

Gives the agent a protected plan tool and a plan message that survives context trimming. On by default; accepted by `run`, `repl`, and `serve`.

| Field | Default | Env var | CLI flag | Description |
|-------|---------|---------|----------|-------------|
| `planning.enabled` | `true` | `ODEK_PLANNING` | `--planning` / `--no-planning` | Enable the plan tool and protected plan message |
| `planning.remind` | `false` | — | — | Soft plan reminder: after 3 non-plan tool calls with no plan, one bounded hint is injected into the tool result, and plans created after work began are flagged `provisional`. Never a hard gate; project config cannot re-enable it when the operator set it off |
| `planning.max_steps` | `12` | — | — | Plan steps allowed (clamped 1–50) |
| `planning.max_render_chars` | `2000` | — | — | Cap on the protected plan render (clamped 200–8000); checked or revised plans must fit in full, including reserved evidence space |

Acceptance checks need no additional flag: declare them through `plan create`
while planning is enabled. Large declarations may need a higher operator-set
`max_render_chars`; project config cannot raise this cap.

Check lifecycle: checks whose tool call is denied by the approval gate or
config transition to `blocked` — they stop gating step completion but stay
visible for closeout honesty. A dead, stale, or environment-denied check can
be replaced via the `check_replace` verb (fresh check or equivalent-evidence
note; justification mandatory and audit-trailed). `create` may always reset
a plan — the superseded checked plan is archived in the revision block, not
lost.

Feature behavior, verbs, and the security model are documented in [PLANNING.md](PLANNING.md).

## Execution budgets (`limits`)

Hard per-run execution budgets (part of the **odek-extension/v1** contract — see [EXTENSIONS.md](EXTENSIONS.md)). All fields are optional; zero or absent means "no limit".

```json
{
  "limits": {
    "max_runtime_seconds": 600,
    "max_tool_calls": 200,
    "max_input_tokens": 500000,
    "max_output_tokens": 100000,
    "max_cost_usd": 0.50,
    "input_cost_per_million_usd": 0.28,
    "output_cost_per_million_usd": 0.42,
    "model_prices": {
      "example-fast-model": {"input_cost_per_million_usd": 0.14, "output_cost_per_million_usd": 0.28},
      "example-pro-model": {"input_cost_per_million_usd": 1.25, "output_cost_per_million_usd": 10.0}
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `max_runtime_seconds` | Wall-clock cap for a run; checked before every LLM call **and** around the tool batch (a deadline context cancels context-aware tools when the remaining budget elapses) |
| `max_tool_calls` | Total tool calls executed; checked before each tool batch is scheduled. If a batch would overflow, remaining slots still run and the overflow calls are skipped with an error result (a denied batch does not count) |
| `max_input_tokens` / `max_output_tokens` | Cumulative prompt/completion tokens; checked after every LLM response |
| `max_cost_usd` | Estimated-spend cap — enforced only when **both** resolved per-million prices are also configured |
| `input_cost_per_million_usd` / `output_cost_per_million_usd` | Operator-configured token prices for the cost estimate (the flat fallback pair). odek never hard-codes provider prices |
| `model_prices` | Optional map of exact model ID → per-model prices. When the run's model ID matches a key **exactly** (no normalization, no prefix matching), that entry's prices are used instead of the flat pair; a missing price in the entry falls back to the flat value individually. Unknown models use the flat pair. Prices are resolved once at run setup. |

On exhaustion odek emits a `budget_exceeded` runtime event, persists the latest safe session state, and `odek run` exits with **code 4** via a typed error naming the limit, the observed value, and the configured maximum (see [CLI.md → Exit codes](CLI.md#exit-codes)).

**Merge semantics (security-relevant):** unlike every other section, `limits` is *clamped*, not blindly overlaid:

- The **global** `~/.odek/config.json` may set any limit.
- The project `./odek.json` may only **lower** an existing limit: a higher value is clamped down to the global one with a stderr warning, and zeroing/omitting a field re-inherits the global limit — a malicious repo can never raise or disable an operator budget. A project *may* set a limit the global config lacks (that only tightens its own runs).
- Project-set per-million **prices are rejected outright** — flat pair and `model_prices` alike — a lower project price would silently weaken cost enforcement — and the global values are kept. Prices belong in `~/.odek/config.json`.
- **CLI flags** (`--max-runtime`, `--max-tool-calls`, `--max-input-tokens`, `--max-output-tokens`, `--max-cost-usd`) are operator intent and set limits explicitly in either direction.
- There is **no `ODEK_*` env-var layer** for limits.

**Cost-disabled warning:** when `max_cost_usd` is set but neither `model_prices[model]` nor the flat pair yields both positive prices for the run's model, odek prints a stderr warning that cost enforcement is disabled (token budgets stay active) — the gap is never silent.

**Current limitation:** budget enforcement is wired into `odek run` and `odek subagent` only. `odek continue`, the REPL, `odek serve`, and the Telegram bot do not yet enforce limits.

Tests: `internal/budget/`, `internal/config/limits_test.go`, `internal/loop/budget_test.go`, `cmd/odek/budget_test.go`.

## Concurrency and reverse-proxy trust

| Field | Default | Env var | Description |
|-------|---------|---------|-------------|
| `max_concurrency` | `3` | `ODEK_MAX_CONCURRENCY` | Max concurrent sub-agent tasks spawned by `delegate_tasks`; tasks beyond the cap queue. 0 = default 3. The parent agent's own stream runs alongside them, so `3` means 4 concurrent provider streams — some providers (e.g. z.ai GLM) throttle around 5 concurrent streams and 429-saturate above that. |
| `trusted_proxies` | `[]` | `ODEK_TRUSTED_PROXIES` | Comma-separated list of IP addresses or CIDR ranges whose `X-Forwarded-For` / `X-Real-Ip` headers are honoured by `odek serve` for rate-limit attribution. Empty list means forwarding headers are ignored. |

`trusted_proxies` is security-relevant: misconfiguring it can allow clients to spoof their IP and bypass the per-IP rate limiters on `/ws` upgrades and session lookups. Configure it only in operator-controlled sources (`~/.odek/config.json` or `ODEK_TRUSTED_PROXIES`).

## Skills configuration

The `skills` section controls the skill system:

```json
{
  "skills": {
    "max_auto_load": 3,
    "max_lazy_slots": 5,
    "import": {
      "max_size_bytes": 1048576,
      "timeout_seconds": 5,
      "require_https": false
    }
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `max_auto_load` | — | 3 | Max skills injected into system prompt on start |
| `max_lazy_slots` | — | 5 | Max skills loaded per user input via trigger matching |
| `verbose` | — | `false` | Surface skill activity details: skill-load banners in the terminal, and skill/memory activity events in the Telegram bot. Off by default — headless/CI runs stay silent |
| `dirs` | — | [] | Extra skill directories beyond `~/.odek/skills` and `./.odek/skills` |
| `import.max_size_bytes` | — | 1048576 (1MB) | Max size for fetched skill content |
| `import.timeout_seconds` | — | 5 | HTTP timeout for skill URI fetch |
| `import.require_https` | — | false | Reject http:// URIs when true |

A project `odek.json` may only narrow `skills.import`: it can lower `max_size_bytes` and `timeout_seconds` and turn `require_https` on, but never raise a cap or turn `require_https` off relative to the operator's (or the compiled default) policy.
| `embedding` | — | *(inherits top-level `embedding`)* | Optional override of the shared embedding backend for semantic skill matching. When unset, skills inherit the top-level `embedding` default with the per-turn query timeout bounded to 2s. See [Shared embedding backend](#shared-embedding-backend-embedding--memory-sessions--skills). |

## Memory configuration

The `memory` section controls the persistent memory system (see [docs/MEMORY.md](MEMORY.md)):

```json
{
  "memory": {
    "enabled": true,
    "facts_limit_user": 4000,
    "facts_limit_env": 8000,
    "buffer_lines": 20,
    "buffer_enabled": true,
    "merge_on_write": true,
    "consolidate_on_end": true,
    "extract_on_end": true,
    "extract_facts": false,
    "llm_search": true,
    "llm_extract": true,
    "llm_consolidate": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3,
    "auto_approve_episodes": false,
    "episode_dedup_threshold": 0.92,
    "consolidate_at_cap_pct": 80,
    "max_episodes": 500,
    "episode_ttl_days": 0,
    "embedding": {
      "provider": "http",
      "base_url": "http://localhost:11434/v1",
      "model": "nomic-embed-text",
      "api_key": "${OPENAI_API_KEY}",
      "dims": 0,
      "timeout_seconds": 10
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | true | Enable memory system entirely |
| `facts_limit_user` | 4000 | Max chars for `user.md` fact file |
| `facts_limit_env` | 8000 | Max chars for `env.md` fact file |
| `buffer_lines` | 20 | Max turn summaries in session buffer |
| `buffer_enabled` | true | Enable the turn-level buffer |
| `merge_on_write` | true | Use go-vector RP similarity to auto-merge related entries (fast, no LLM — uses simple string merge) |
| `consolidate_on_end` | true | At session end, run an LLM consolidation pass over `user.md` and `env.md` in a background goroutine. This is the quality complement to `merge_on_write`: merge-on-write handles obvious duplicates immediately (no LLM), while consolidation handles near-duplicates and paraphrases at session end with full LLM quality. Requires `llm_consolidate: true`. **Note:** facts in the borderline similarity band (0.3–0.7 cosine) are now always added immediately and only merged by this consolidation pass — if you set `consolidate_on_end: false`, near-duplicate facts will accumulate rather than being merged. |
| `consolidate_at_cap_pct` | 80 | Percentage of `facts_limit_user`/`facts_limit_env` above which a background consolidation pass fires for that fact file on `AddFact` — long-lived serve/REPL sessions never reach the session-end trigger, so entries otherwise fossilize near the cap. Snapshot/preview is taken flock-free; the flock is held only for the verified apply swap, never across the LLM call. One consolidation pass runs per manager at a time; a crossing that arrives while a pass is running is queued per target and serviced when it finishes (a same-target re-run inside the cooldown window is dropped — the next crossing re-arms it). Explicit `0` disables the cap trigger. Values above 99 are treated as 99 (adds that would reach 100% of the cap are rejected, so 100 could never fire). Best-effort: errors are logged, never surfaced to callers. Requires `llm_consolidate: true`. |
| `extract_on_end` | true | At session end (≥3 turns), extract a narrative episode summary via LLM for later recall |
| `min_turns_for_extraction` | 3 | Minimum conversation turns before end-of-session extraction runs |
| `extract_facts` | **false** | **Opt-in.** At session end (≥3 turns), auto-extract a few **durable** facts (stable user preferences, project invariants) into `user.md`/`env.md`. Off by default — see the security note below. Independent of `extract_on_end`; to disable *all* end-of-session LLM extraction set `llm_extract: false`. |
| `llm_search` | true | Use LLM to rerank candidates for **explicit** `memory search` calls (the `memory` tool). Per-turn recall (`FormatEpisodeContext`) always uses the cached go-vector index — no LLM call on the hot path regardless of this setting. |
| `llm_extract` | true | Use LLM for end-of-session fact extraction |
| `llm_consolidate` | true | Use LLM to merge related fact entries |
| `merge_threshold` | 0.7 | Cosine similarity above which two fact entries are **auto-merged** without an LLM call (0.0–1.0). Raise it to merge less aggressively; lower it to merge more. |
| `add_threshold` | 0.3 | Cosine similarity below which a new fact entry is **auto-added** without an LLM call (0.0–1.0). Between `add_threshold` and `merge_threshold` the LLM decides. Keep `add_threshold` < `merge_threshold`. |
| `auto_approve_episodes` | false | **Security trade-off.** When true, untrusted episodes (sessions that touched web/MCP/out-of-workspace content) are auto-approved at session end so they are recalled without a manual `odek memory promote`. Leaving it `false` keeps the human review gate (recommended). |
| `episode_dedup_threshold` | 0.92 | Cosine similarity above which a newly written episode is treated as a near-duplicate of an existing one and **replaces** it (newest wins). An untrusted episode never replaces a trusted/approved one — the untrusted near-duplicate is dropped, not stored. `0` disables dedup. |
| `max_episodes` | 500 | Maximum number of stored episodes. On each write, episodes beyond this count are evicted oldest-first (both the summary file and the index entry). `0` disables the cap. |
| `episode_ttl_days` | 0 | Evict episodes older than this many days. `0` (default) disables TTL-based eviction. |
| `embedding` | *(inherits top-level `embedding`)* | Optional override of the embedding backend for episode recall, dedup, the non-LLM episode ranker, and fact merge-on-write. When unset, memory inherits the shared top-level [`embedding`](#shared-embedding-backend-embedding--memory-sessions--skills) default; if neither is set, local RandomProjections (lexical bag-of-words — fast, zero-cost, but no real semantics). See below. |

### Extended Memory (`memory.extended`)

`memory.extended` is an **opt-in** atomic memory layer. It extracts small, typed memory atoms from user messages and recalls them via semantic search over the atom corpus. It does not replace facts, the buffer, or episodes; it adds a fourth source of context that is injected after episodes on each turn. See [docs/EXTENDED_MEMORY.md](EXTENDED_MEMORY.md) for the full design.

> **Security note:** Project-level `./odek.json` cannot set the `memory` or `embedding` sections. Configure `memory.extended` in `~/.odek/config.json`, via the `ODEK_MEMORY_EXTENDED_*` environment variables, or with the CLI flags listed below.

```json
{
  "memory": {
    "extended": {
      "enabled": true,
      "max_size_mb": 100,
      "semantic_search_top_k": 10,
      "semantic_search_overfetch": 4,
      "semantic_search_min_score": 0.55,
      "semantic_search_rerank": true,
      "semantic_dedup_threshold": 0.92,
      "consolidate_similarity_threshold": 0.9,
      "atom_max_chars": 300,
      "memory_budget_chars": 2000,
      "decay_half_life_days": 30,
      "quarantine_ttl_days": 7,
      "ephemeral_ttl_days": 14,
      "eviction_policy": "retention_decay",
      "predictive_intents": 3,
      "auto_extract_per_turn": true,
      "infer_user_state": true,
      "llm": {
        "base_url": "http://localhost:11434/v1",
        "api_key": "",
        "model": "qwen2.5:7b",
        "max_tokens": 1024,
        "temperature": 0.2,
        "timeout_seconds": 30
      },
      "embedding": {
        "provider": "http",
        "base_url": "http://localhost:11434/v1",
        "model": "nomic-embed-text"
      }
    }
  }
}
```

| Field | Default | Env var | CLI flag | Description |
|-------|---------|---------|----------|-------------|
| `enabled` | `false` | `ODEK_MEMORY_EXTENDED_ENABLED` | `--memory-extended-enabled` | Master switch for Extended Memory. |
| `max_size_mb` | `100` | `ODEK_MEMORY_EXTENDED_MAX_SIZE_MB` | `--memory-extended-max-size-mb` | Hard disk budget for the `extended/` directory. |
| `semantic_search_top_k` | `10` | — | — | Number of atoms returned to the system prompt. |
| `semantic_search_overfetch` | `4` | — | — | Candidate multiplier before filtering and reranking. |
| `semantic_search_min_score` | `0.55` | — | — | Minimum cosine similarity for a candidate to be considered. |
| `semantic_search_rerank` | `true` | — | — | Use the memory LLM to rerank candidates. |
| `semantic_dedup_threshold` | `0.92` | — | — | Cosine similarity at or above which an incoming atom is treated as a paraphrase of an existing live atom and refreshes it instead of appending. `0` disables the semantic tier (exact-match dedup always runs). |
| `consolidate_similarity_threshold` | `0.9` | — | — | Pairwise cosine similarity at or above which live atoms are grouped for LLM merging by `odek memory extended consolidate` / `ConsolidateAtoms`. |
| `atom_max_chars` | `300` | `ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS` | `--memory-extended-atom-max-chars` | Maximum stored text length per atom. |
| `memory_budget_chars` | `2000` | `ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS` | `--memory-extended-memory-budget-chars` | Maximum injected Extended Memory context per turn. |
| `decay_half_life_days` | `30` | — | — | Days until an atom's recall/eviction weight halves. |
| `quarantine_ttl_days` | `7` | — | — | Days before a tainted atom is auto-deleted from quarantine. |
| `ephemeral_ttl_days` | `14` | — | — | Days before ephemeral-class atoms (intent, goal, error, question, file) stop being recalled. Durable classes (preference, convention, fact, decision) never expire via TTL; pinned atoms are exempt. |
| `eviction_policy` | `"retention_decay"` | — | — | Eviction algorithm. `"retention_decay"` is the only supported value. |
| `predictive_intents` | `3` | — | — | Reserved for future predictive-intent recall. Currently accepted but ignored. |
| `auto_extract_per_turn` | `true` | — | — | Extract atoms after every user message. |
| `infer_user_state` | `true` | — | — | Reserved for future user-state model inference. Currently accepted but ignored. |
| `user_state_turn_interval` | `5` | — | — | Turns between user-model inference passes (when the user-state model is active). |
| `user_state_max_pending` | `20` | — | — | Cap on pending user-model corrections queue. |
| `user_state_pending_max_age_days` | `14` | `0` disables | — | Drop unconfirmed pending corrections older than this many days (confirmed facts are never touched). |
| `associations_enabled` | `true` | — | — | Atom associations: semantic neighbors linked for co-recall. |
| `association_semantic_top_k` | `3` | — | — | Semantic neighbors linked per atom. |
| `proactive_return_after_break` | `true` | — | — | Return-after-break summary on the first turn after a gap. |
| `style_mirroring_enabled` | `true` | — | — | Mirror the user's communication style in answers (trusted atoms only). |
| `anaphora_resolution_enabled` | `true` | — | — | Resolve pronouns against remembered entities at recall. |
| `follow_up_anticipation_enabled` | `true` | — | — | Anticipate likely follow-up intents on every recall. |
| `follow_up_suggestions_enabled` | `true` | `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED` | — | Capture high-confidence predicted intents at recall time as follow-up suggestions (zero extra LLM cost). |
| `follow_up_suggestion_min_confidence` | `0.6` | `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE` | — | Minimum predicted-intent confidence for a follow-up suggestion. |
| `proactive_nudges_enabled` | `false` | `ODEK_MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED` | — | Master switch for proactive nudge delivery (`TakeNudges`). Opt-in. |
| `nudge_max_per_day` | `1` | `ODEK_MEMORY_EXTENDED_NUDGE_MAX_PER_DAY` | — | Maximum proactive nudges delivered per day. |
| `nudge_cooldown_hours` | `24` | `ODEK_MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS` | — | Per-kind cooldown before a nudge of the same kind can fire again. |
| `nudge_stale_goal_days` | `7` | `ODEK_MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS` | — | Days without activity before a goal/intent atom counts as stale for nudges. |
| `nudge_open_question_min_age_hours` | `24` | `ODEK_MEMORY_EXTENDED_NUDGE_OPEN_QUESTION_MIN_AGE_HOURS` | — | Minimum age before a `question` atom may become a nudge candidate (younger questions are usually about to be answered). |
| `llm` | omitted | — | — | Dedicated memory LLM. If omitted, the main agent LLM is reused. A warning is emitted if that model has thinking enabled. Fields left empty inside `llm` are inherited from the main agent LLM, so a partial override such as `"llm": {"thinking": "disabled"}` reuses the parent provider connection (`model` / `max_tokens` / `temperature`) while disabling reasoning for memory calls. |
| `embedding` | omitted | — | — | Dedicated embedding backend for atoms. If omitted, inherits `memory.embedding` or the shared top-level `embedding`. |

### `embedding` — real semantic embeddings (optional)

By default every similarity computation in memory uses go-vector
**RandomProjections**: a local, zero-dependency bag-of-words embedder. It is
fast but purely lexical — *"fixed the auth bug"* and *"repaired login issue"*
share no tokens and score ~0. Setting `embedding.provider` to `"http"` routes
all of those paths through any **OpenAI-compatible embeddings API** instead
(Ollama, llama.cpp server, LM Studio, vLLM, OpenAI, Voyage…), giving recall
that matches by meaning.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"rp"` | `"rp"` = local RandomProjections; `"http"` = OpenAI-compatible embeddings API. An `"http"` config missing `base_url` or `model` silently falls back to `"rp"` so memory keeps working. |
| `base_url` | — | API root, e.g. `http://localhost:11434/v1` (Ollama) or `https://api.openai.com/v1`. `${ENV_VAR}` expansion supported. |
| `model` | — | Embedding model name, e.g. `nomic-embed-text`, `text-embedding-3-small`. |
| `api_key` | — | Sent as `Authorization: Bearer <key>` when set. `${ENV_VAR}` expansion supported — keep secrets out of config files. |
| `dims` | 0 | Expected vector dimensionality; `0` infers it from the first response (recommended). |
| `timeout_seconds` | 10 | Per-request HTTP timeout. |

Operational notes:

- **Per-turn recall stays cheap.** Episode vectors live in a persisted index; a
  loop turn costs at most one embedding call (the query), bounded by
  `timeout_seconds`. If the backend is down, recall degrades to "no context"
  and rebuilds back off for 30s — the agent loop is never blocked. The index
  rebuild that follows a new episode (session-end) embeds the corpus on a fresh
  client *off* the index lock, so a slow backend never serializes concurrent
  recall; it is one batch call over the episode summaries.
- **Switching backends is safe.** The persisted index records which embedding
  space it was built in; changing `provider`/`model`/`dims` automatically
  invalidates it and rebuilds on next use (one batch embedding call). Note: with
  `dims: 0`, if a server silently changes a model's output dimensionality (e.g.
  a model upgrade under the same name) the fingerprint cannot detect it; recall
  self-heals to "no context" on the dimension mismatch and rebuilds on the next
  write. Pin `dims` if you want such a change to force an explicit rebuild.
- **`base_url` is an egress target — point it only at a server you trust.** Every
  episode summary and fact entry is POSTed there for embedding. The URL is used
  verbatim with no allowlist, so do not point it at internal/metadata endpoints
  (e.g. cloud metadata services) you would not otherwise expose. Prefer a local
  server (Ollama/llama.cpp) when episode/fact text must not leave the machine.

## Shared embedding backend (`embedding`) — memory, sessions & skills

The same embedder that powers memory also powers **semantic session search**
(the `session_search` tool) and **semantic skill matching**. Set one
**top-level `embedding` block** and *every* subsystem inherits it — one endpoint,
consistent embedding-space semantics everywhere. Each subsystem can still
override the default with its own block. The block uses the same fields as
`memory.embedding` above (`provider`/`base_url`/`model`/`api_key`/`dims`/`timeout_seconds`).

```json
{
  "embedding": {
    "provider": "http",
    "base_url": "http://localhost:11434/v1",
    "model": "nomic-embed-text"
  }
}
```

With just that block, memory recall, `session_search`, and skill matching all go
semantic.

| Subsystem | Inherits the shared `embedding`? | Optional override |
|-----------|----------------------------------|-------------------|
| **Memory** | ✅ when `memory.embedding` is unset | `memory.embedding` |
| **Sessions** (`session_search`) | ✅ when `sessions.embedding` is unset | `sessions.embedding` |
| **Skills** (lazy matching) | ✅ when `skills.embedding` is unset (timeout bounded) | `skills.embedding` |

Each override is optional and isolated — e.g. point skills at a smaller/faster
model while memory uses a higher-quality one:

```json
{
  "embedding": { "provider": "http", "base_url": "http://localhost:11434/v1", "model": "nomic-embed-text" },
  "skills":   { "embedding": { "provider": "http", "base_url": "http://localhost:11434/v1", "model": "all-minilm" } }
}
```

Operational notes:

- **Sessions self-heal across backend changes** exactly like memory: a
  `vectors_meta.json` fingerprint records the embedding space; changing
  `provider`/`model`/`dims` forces a one-time rebuild from the session files. A
  down backend degrades `session_search` to its keyword fallback and backs off
  for 30s — it never fails a session save.
- **Skill matching is the hot path — it inherits, but with a bounded timeout.**
  Skill matching runs on *every user turn*, so when skills inherit the shared
  default their per-turn query embed is capped at **2s** (regardless of the
  shared `timeout_seconds`) and any slow/failed/empty result falls back to the
  local keyword matcher. An explicit `skills.embedding` is respected verbatim —
  set its own `timeout_seconds` if you want a different bound. Memory and
  sessions are *not* capped (they embed infrequently and persist their vectors).
- The **egress warning above applies to every subsystem** — session transcripts
  and skill text are POSTed to `base_url`. Point it only at a server you trust.

### `extract_facts` — automatic fact learning (opt-in, off by default)

When enabled, after each session of ≥3 turns odek asks the LLM to pull a few
**durable** facts from the conversation — stable user preferences (`user.md`) and
project/environment invariants (`env.md`) — so it learns them without you calling
the `memory` tool. Facts are injected into **every** system prompt.

**Why it is off by default.** Turning conversation into always-injected memory is
a *persistent prompt-injection* surface. Several guards apply when it is on:

- It runs **only for trusted sessions** — a session that ingested untrusted
  content via tools (web, MCP, out-of-workspace file reads) writes no facts.
- The extractor is instructed to treat the conversation as **data**, never to act
  on instructions in it, and never to record "download-and-run" style content.
- A download-and-execute / pipe-to-shell filter drops the obvious exploit class,
  and the standard injection/credential scan, merge-on-write dedup, and char caps
  all still apply. A per-session count cap limits how many facts one session adds.

**The residual risk these do NOT remove:** the trusted-session gate only covers
content the agent fetched via *tools* — it does **not** cover untrusted text that
enters the *conversation* another way (e.g. you paste an attacker-controlled
snippet into a chat that otherwise stayed trusted). Such text is summarized by
the extractor and a *plausible, non-command* fact could still be stored and then
injected into every future prompt. This cannot be fully eliminated while the
feature is on.

**Recommendation.** Leave `extract_facts: false` (the default) on any host that
processes untrusted input. Enable it only in trusted, single-user setups where
you accept the trade-off, and periodically review stored facts with the `memory`
tool (`read`) — or remove a bad one with `memory remove`. To turn off *all*
end-of-session LLM extraction (episodes and facts), set `llm_extract: false`.

## Sub-agent configuration

The `subagent` section controls task decomposition and parallel sub-agent execution (see [docs/SUBAGENTS.md](SUBAGENTS.md)):

```json
{
  "subagent": {
    "max_concurrency": 3,
    "timeout_seconds": 120,
    "max_iterations": 15
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `max_concurrency` | global `max_concurrency` | Max sub-agents running in parallel (max 8) |
| `timeout_seconds` | 1800 | Default wall-clock budget per sub-agent, 30 minutes (overridden by `--timeout`); clamped to 1800 |
| `max_iterations` | 15 | Default think→act cycles per sub-agent (overridden by `--max-iter`); clamped to 100 |
| `max_depth` | 2 | Delegation nesting cap via `ODEK_SUBAGENT_DEPTH`; clamped to 8 |
| `announce_budget` | true | Sub-agents are told their budget at spawn and warned at 50/75/90% usage. Distinct from the top-level `announce_budget` parent toggle |
| `budget_inherit` | `"operator"` | `"share"` = a sub-agent gets min(operator limits, parent's remaining budget) |
| `default_profile` | `"default"` (built-in) | Capability profile applied when a delegated task selects none: a defined profile name, or `"none"` to disable the built-in envelope (see [Capability profiles](#capability-profiles)) |

This section is optional. Omitted fields inherit the defaults above.

This section is **operator-only**: a `subagent` section in project-level `./odek.json` is ignored with a warning — a cloned repo must not be able to extend its own sub-agents' budgets, lift the runaway-process clamps, or re-widen budget inheritance.

### Capability profiles

The top-level `profiles` section defines named permission envelopes. When a task selects one (via `delegate_tasks`'s `profile` field or `odek subagent --profile`), the profile's settings **override** the corresponding operator permissions for that sub-agent — the profile is the complete permission envelope, not a merge:

```json
{
  "profiles": {
    "research": {
      "description": "Read-only web research — fetches pages, never edits or runs anything",
      "max_risk": "safe",
      "tools": { "disabled": ["write_file", "patch", "shell"] }
    },
    "builder": {
      "description": "Write and verify code changes with project build/test commands allowlisted",
      "max_risk": "local_write",
      "allowlist": ["go test ./...", "go build ./..."]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `description` | Short summary of what the profile is FOR — surfaced by the `list_subagent_profiles` tool so the delegating model can pick by intent, not by guessing at names |
| `max_risk` | Clamps every class ranked above it to `deny` for profiled sub-agents. Order: `safe` < `local_write` < `install` < `network_egress` < `network_upload` < `code_execution` < `system_write` < `persistence` < `unknown` < `destructive` < `blocked`. So `network_egress` does not admit uploads, `code_execution` does, and `local_write` (the default) admits neither; `unread_exec` is not capped here — sub-agents never prompt, so the unread-script gate denies it. `max_risk` can only lower what the policy allows, never raise it |
| `allowlist` | **Replaces** the global allowlist for profiled sub-agents |
| `tools` | **Replaces** the global `tools` enabled/disabled filter for profiled sub-agents |

### Built-in default profile and `subagent.default_profile`

A built-in profile named **`default`** (`max_risk: "local_write"`) is always materialized unless you define your own profile with that name (yours wins) or set `subagent.default_profile: "none"`. **This is the envelope sub-agents run under when no profile is selected** — `delegate_tasks` tasks without a `profile` field and `odek subagent` runs without `--profile` are capped at `local_write`: no system writes, code execution, installs, network egress, or destructive operations.

- **Precedence:** `--profile` flag > task-file `profile` > `subagent.default_profile` (built-in `default` unless overridden).
- **Operator sovereignty:** `"none"` is honored only from your config — a task file or flag can never strip the operator's envelope; a task cannot opt out of it, only select a different defined profile.
- **Behavior change vs. earlier releases:** trusted sub-agents were previously uncapped; they are now clamped to `local_write` too. Tasks needing `code_execution`/`network_egress` must select an explicit profile (e.g. `test-runner`, `researcher`).
- **Discovery:** the agent invokes the built-in `list_subagent_profiles` tool to see every available profile — name, `description`, `max_risk`, tool filters, and which one is the effective default — before picking one for `delegate_tasks`.

### Starter set: [`profiles.template.json`](../profiles.template.json)

The repository ships a curated starter set of **21 profiles** covering the most common agent tasks — copy the entries you need into your top-level `profiles` section (operator config only) and trim from there:

- **Software engineering**: `builder`, `refactorer`, `test-runner`, `bug-investigator`, `perf-profiler`, `migrator` — write/verify code with project commands allowlisted; `code-reviewer`, `security-auditor` — strictly read-only review
- **Operations**: `ops-inspector` (read-only infra), `release-manager` (git/`gh` release flow — it can push and merge; review its allowlist before trusting it)
- **Orchestration & evaluation**: `swarm-orchestrator` (delegates via `delegate_tasks`, inspects nothing itself), `judge`, `scout`, `librarian` — read-only verifiers and explorers
- **Research**: `researcher`, `web-reader` — network egress for web work, no shell
- **Content & data**: `writer`, `translator`, `data-analyst`, `media-describer`, `summarizer`

Design rules the starter set follows:

- **Read-only roles use `tools.enabled`** (a strict whitelist — nothing undeclared runs); **productive roles use `tools.disabled`** (everything except the listed distractions).
- **`allowlist` entries are exact-match** — `go test ./...` matches only that literal string. The starter allowlists cover the canonical entry points of the major ecosystems (Go, npm/pnpm/yarn, pytest/ruff/mypy, cargo, `make` targets); swap in the exact commands your projects actually run.
- A profile is an envelope, not a merge: anything you omit inherits the operator's global policy, so the minimal useful profile is often just `max_risk`.

Rules:

- **Operator-authored only.** A `profiles` section in project-level `./odek.json` is ignored with a warning — a cloned repo must not author its own permission envelope.
- **Override, not escalation.** The non-interactive deny and the trust lockdown are applied *after* the profile and cannot be lifted by selecting one. An untrusted task stays untrusted under any profile.
- **Fail closed.** Selecting an unknown profile name fails the task (validated by `delegate_tasks` before spawn and again by the sub-agent itself); profiles with an invalid `max_risk` are dropped at load time with a warning. A broken `subagent.default_profile` (undefined name) fails the sub-agent at spawn — loudly, not silently bare.


## MCP server configuration

Connect to **external MCP servers** and register their tools as `<server>__<tool>`
(e.g. `playwright__navigate`). The `command` / `args` / `env` shape matches Claude
Code's `mcpServers` object; odek does not expand `${VAR}` in `env`. Alternatively,
set `url` on an entry to use the **Streamable HTTP transport** (no subprocess).

```json
{
  "mcp_servers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp"]
    },
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"]
    },
    "remote": {
      "url": "https://mcp.example.com/rpc",
      "token_env": "MCP_REMOTE_TOKEN"
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `command` | Executable to spawn (required unless `url` is set; ignored when `url` is set) |
| `url` | `http(s)` endpoint for the Streamable HTTP transport. Absolute URLs only, no embedded credentials (use `token_env`). The server's host is dialed through odek's SSRF guard (internal-IP refusal, DNS-rebinding-safe pinning) |
| `token_env` | Name of the environment variable holding the Bearer token sent as `Authorization` on every request to `url`. Must be uppercase `MCP_`-prefixed (e.g. `MCP_REMOTE_TOKEN`); other names are rejected. Resolved from the operator environment (e.g. `~/.odek/secrets.env`); the token itself never enters config files |
| `enabled` | Server on/off switch. Omitted or `true` = enabled (back-compat); `false` keeps the entry configured (preserving approval hashes for later re-enable) but the server is never started, discovered, or prompted for. Surfaced as `enabled` in `odek introspect` / `GET /api/mcp` so configured-but-off servers stay visible |
| `args` | Optional command-line arguments |
| `env` | Optional overrides (empty string unsets). Secret-looking keys are stripped even here |
| `timeout_seconds` | Per-request timeout (default `30`; clamped to `3600` with a warning) |
| `max_response_bytes` | Cap on one JSON-RPC response line (default 10 MiB; config above 64 MiB is rejected) |
| `max_result_chars` | Cap on model-facing result text (default `200000`; clamped to `1000000` with a warning) |
| `artifact_roots` | Directories that may host `file://` artifact refs. **Empty (default) rejects every ref.** |
| `auto_approve` | Skip server and per-tool prompts. Honored only from `~/.odek/config.json`; stripped from `./odek.json` with a warning. Trusts the execution fingerprint (command/args/env/limits/roots), not the server name |

Limit-field semantics: [EXTENSIONS.md](EXTENSIONS.md). Approvals, surfaces that load MCP
(and those that do not — Telegram chat, untrusted sub-agents), and `odek mcp` server
mode: [MCP.md](MCP.md).

## Telegram

The `telegram` section configures the Telegram bot integration and the `--deliver` flag.

```json
{
  "telegram": {
    "bot_token": "8610437446:AAElHFJ...",
    "allowed_users": [8592463065],
    "allowed_chats": [],
    "poll_interval": 1,
    "poll_timeout": 30,
    "max_msg_length": 4096,
    "max_download_size": 5242880,
    "media_quota_per_chat": 52428800,
    "session_ttl_hours": 24,
    "default_chat_id": 8592463065
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `bot_token` | `ODEK_TELEGRAM_BOT_TOKEN` | — (required) | Telegram bot API token from @BotFather |
| `allowed_users` | — | all | Restrict bot to specific user IDs |
| `allowed_chats` | — | all | Restrict bot to specific chat IDs |
| `allow_all_users` | `ODEK_TELEGRAM_ALLOW_ALL` | false | Explicitly run the bot with **no allowlist** (any user may drive the agent). Without this, an empty allowlist is a fatal misconfiguration — an open bot can never be deployed by accident |
| `bot_username` | `ODEK_TELEGRAM_BOT_USERNAME` | — | Bot username (used to strip `@bot` mentions) |
| `poll_interval` | — | 1 | Seconds between poll cycles |
| `poll_timeout` | — | 30 | Long-poll timeout (1-60 seconds) |
| `max_msg_length` | — | 4096 | Max characters per message |
| `daily_token_budget` | `ODEK_TELEGRAM_DAILY_TOKEN_BUDGET` | 0 (unlimited) | Daily token budget across bot chats |
| `agent_timeout_seconds` | `ODEK_TELEGRAM_AGENT_TIMEOUT` | 900 (15 min; 0 = unlimited) | Max duration for a single bot-triggered agent run |
| `session_ttl_hours` | — | 24 | Hours an inactive chat's session stays in the in-memory cache before being reloaded from disk. This is cache-only — on-disk session expiry is `maintenance.sessions_max_age_days` (see [MAINTENANCE.md](MAINTENANCE.md)) |
| `fallback_urls` | `ODEK_TELEGRAM_FALLBACK_URLS` | — | Alternate Telegram API base URLs tried when the primary fails |
| `health_addr` | `ODEK_TELEGRAM_HEALTH_ADDR` | — (disabled) | Listen address for the bot's health endpoint (e.g. `127.0.0.1:9090`) |
| `max_download_size` | `ODEK_TELEGRAM_MAX_DOWNLOAD_SIZE` | 5242880 (5 MiB) | Per-file byte cap for Telegram voice/photo/document downloads. Set to `-1` to disable. |
| `media_quota_per_chat` | `ODEK_TELEGRAM_MEDIA_QUOTA_PER_CHAT` | 0 (disabled) | Total bytes of downloaded media allowed per chat. `0` disables the quota. |
| `default_chat_id` | — | 0 | **Required for `--deliver`** — numeric chat ID where `odek run --deliver` sends results. Get this from your bot's update or use a tool like `@userinfobot`. |

### --deliver flag

The `--deliver` flag on `odek run` sends the agent's final response to the configured
`default_chat_id` as a plain text message. This enables **cron-based scheduled agent
workflows** — no daemon needed.

```bash
# Run an agent task and deliver the result to Telegram
odek run --deliver "Check the CI pipeline status"

# Works with task text first too
odek run "Daily summary" --deliver
```

See [docs/TELEGRAM.md](TELEGRAM.md#cron-integration) for full cron setup instructions.

## Schedules

Configures the native in-process task scheduler (`odek schedule`). Job
definitions live in `~/.odek/schedules.json`; this section only tunes the
engine. Every field has an `ODEK_SCHEDULES_*` environment override.

```json
{
  "schedules": {
    "enabled": true,
    "max_concurrent": 2,
    "timezone": "UTC",
    "catchup": false,
    "allow_telegram_management": true,
    "telegram_admin_chats": [123456789],
    "telegram_admin_users": [987654321]
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_SCHEDULES_ENABLED` | `true` | Run the embedded scheduler inside `odek telegram`. Set false to run only a standalone `odek schedule daemon`. |
| `max_concurrent` | `ODEK_SCHEDULES_MAX_CONCURRENT` | `2` | Maximum scheduled jobs running at once. |
| `timezone` | `ODEK_SCHEDULES_TIMEZONE` | `UTC` | Default timezone for jobs that don't set their own `--tz`. |
| `catchup` | `ODEK_SCHEDULES_CATCHUP` | `false` | Global default for the missed-run policy: run a missed fire once on startup. |
| `allow_telegram_management` | `ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT` | `true` | Allow the Telegram `/schedule` commands to create/remove/toggle/run jobs. When false, the bot still lists and previews jobs but mutations must go through `odek schedule`. |
| `telegram_admin_chats` | `ODEK_SCHEDULES_TELEGRAM_ADMIN_CHATS` | `[]` | Comma-separated list of operator chat IDs. These IDs may use mutating `/schedule` commands **and** `/restart`. When empty, the bot falls back to `telegram.default_chat_id`. Read-only commands are unaffected. |
| `telegram_admin_users` | `ODEK_SCHEDULES_TELEGRAM_ADMIN_USERS` | `[]` | Comma-separated list of operator user IDs. These IDs may use mutating `/schedule` commands **and** `/restart`. Read-only commands are unaffected. |
| `dangerous` | see below | `{}` | Schedule-specific override for the dangerous-operations policy. |

### Schedule-specific dangerous policy

Scheduled jobs run unattended, so by default the scheduler denies any class that would require an approval prompt (`system_write`, `code_execution`, `install`, `network_upload`, `unknown`, `persistence`, `unread_exec`). A scheduled job that must upload (a webhook `POST`, an `rsync` or `scp` to a backup host) needs `schedules.dangerous.classes: {"network_upload": "allow"}`. Note: since `network_egress` now defaults to `allow` globally, scheduled jobs also egress unprompted — unattended egress from a cron context is a higher-risk surface, so gate it explicitly via `schedules.dangerous.classes: {"network_egress": "deny"}` (or set it back to `prompt` globally) if that matters to you. You can override the scheduler policy without widening the policy for interactive CLI/REPL/WebUI use.

```json
{
  "schedules": {
    "dangerous": {
      "classes": {
        "network_egress": "allow",
        "system_write": "allow"
      },
      "allowlist": ["curl -s https://example.com/feed.xml"]
    }
  }
}
```

Environment overrides:

| Env | Format |
|---|---|
| `ODEK_SCHEDULES_DANGEROUS_CLASSES` | JSON object, e.g. `{"network_egress":"allow","system_write":"allow"}` |
| `ODEK_SCHEDULES_DANGEROUS_ALLOWLIST` | Comma-separated command strings |
| `ODEK_SCHEDULES_DANGEROUS_DENYLIST` | Comma-separated command strings |
| `ODEK_SCHEDULES_DANGEROUS_ACTION` | Global default action: `allow`, `deny`, or `prompt` |
| `ODEK_SCHEDULES_DANGEROUS_NON_INTERACTIVE` | `allow`, `deny`, or `read_only` (ignored: scheduled runs force `deny`) |

Safety floor that cannot be overridden:
- `non_interactive` is always `deny` (no human is present to approve).
- `destructive`, `blocked`, `persistence` (deferred-execution writes: shell profiles, git hooks, CI workflows, cron, systemd, launchd, lifecycle scripts), and `unread_exec` (executing a script whose contents were not read in the session) classes are always denied.

Project-level `odek.json` cannot set `schedules` at all — the whole section is ignored with a warning (schedules are global-only: the scheduler daemon is a machine-level service). Configure schedules via `~/.odek/config.json` or environment variables.

Full guide: [docs/SCHEDULES.md](SCHEDULES.md).

## Storage maintenance

Configures the background storage janitor (`internal/maintenance`). It runs a
sweep over `~/.odek` every `interval_minutes`: expiring old sessions and
audit records, rotating oversized logs, deleting stale Telegram plans and
downloaded media.
Every field has an `ODEK_MAINTENANCE_*` environment override.

```json
{
  "maintenance": {
    "enabled": true,
    "interval_minutes": 60,
    "sessions_max_age_days": 30,
    "audit_max_age_days": 14,
    "plans_max_age_days": 30
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_MAINTENANCE_ENABLED` | `true` | Run the janitor. Set false to disable all storage maintenance. |
| `interval_minutes` | `ODEK_MAINTENANCE_INTERVAL_MINUTES` | `60` | Minutes between sweeps. The first sweep runs after one interval, never at startup. |
| `sessions_max_age_days` | `ODEK_MAINTENANCE_SESSIONS_MAX_AGE_DAYS` | `30` | Delete sessions (and their index/vector-index entries) older than this. `0` = keep forever. |
| `audit_max_age_days` | `ODEK_MAINTENANCE_AUDIT_MAX_AGE_DAYS` | `14` | Delete `~/.odek/sessions/audit/*.json` records older than this. `0` = keep forever. |
| `plans_max_age_days` | `ODEK_MAINTENANCE_PLANS_MAX_AGE_DAYS` | `30` | Delete Telegram plan files (`~/.odek/plans/**/*.md`) older than this; emptied chat directories are removed. `0` = keep forever. |
| `artifacts_max_age_hours` | `ODEK_MAINTENANCE_ARTIFACTS_MAX_AGE_HOURS` | `24` | Delete sub-agent result artifact subtrees (`~/.odek/artifacts/<session>/`) older than this. This is the **backstop** — the primary lifecycle is the session-cleanup cascade (deleting a session removes its artifacts immediately). `0` = keep forever. |

Downloaded Telegram media (`~/.odek/media/`, including per-chat `chat<id>/`
subdirectories) is always swept after 1 hour; that policy is not configurable.

The `maintenance` section is **operator-only**: it governs deletion of user
data, so the project-level `./odek.json` cannot set it (a `maintenance`
section there is ignored with a stderr warning). Configure it via
`~/.odek/config.json` or the `ODEK_MAINTENANCE_*` environment variables.

## Tool configuration

Control which tools are exposed to the LLM. Use this to deploy locked-down
agents — for example, a chatbot with only `web_search`, `transcribe`, and
`vision`, or a read-only research assistant with no write tools.

```json
{
  "tools": {
    "enabled": ["web_search", "transcribe", "vision"],
    "disabled": ["shell", "write_file", "patch"]
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_TOOLS_ENABLED` | unset | Whitelist. When set, only these tools are registered. Comma-separated in env. |
| `disabled` | `ODEK_TOOLS_DISABLED` | unset | Blacklist. These tools are removed from the default set. Comma-separated in env. |

CLI flags override file and env config:

```bash
# Whitelist mode: only these tools
odek run --tool web_search --tool vision "what's new in Go?"

# Blacklist mode: remove specific tools
odek run --no-tool shell --no-tool write_file "review this code"

# Environment
ODEK_TOOLS_ENABLED=web_search,vision odek run "search and summarize"
```

Resolution rules:

- `enabled` is set by the highest-priority layer that provides it.
- `disabled` is merged across layers.
- If both are present: start from `enabled`, then subtract `disabled`.
- Unknown tool names are silently ignored.
- The `memory` tool is also subject to this filter, so a whitelist must
  include `"memory"` if you want persistent memory.

Project-level `./odek.json` **cannot enable tools** — it may only append to
`disabled`. This prevents a malicious repository from widening the tool
surface.

## Web search (`web_search`)

Backs the `web_search` tool with a self-hosted [SearXNG](https://searxng.github.io/)
instance. Operator-only (`~/.odek/config.json` or environment variables; a
project config cannot set it).

| Field | Default | Description |
|-------|---------|-------------|
| `base_url` | `""` | SearXNG instance URL (e.g. `http://127.0.0.1:8888`). **The tool is registered only when this is non-empty** — with no backend, the tool stays hidden |
| `categories` | `""` | Comma-separated SearXNG categories to restrict queries (e.g. `general,news`); empty = SearXNG default |
| `language` | `""` | SearXNG language code (e.g. `en`); empty = SearXNG default |
| `max_results` | `10` | Cap on results returned to the agent |
| `timeout_seconds` | `15` | Per-request timeout |

## Audio transcription (`transcription`)

Configures the `transcribe` tool (local whisper.cpp).

| Field | Default | Description |
|-------|---------|-------------|
| `model` | `tiny` | Whisper model name |
| `language` | `""` | ISO language code (e.g. `en`); empty = auto-detect |
| `auto_transcribe` | `true` | Automatically transcribe audio received over Telegram before the agent answers |
| `models_dir` | `""` | Directory holding whisper model files |
| `binary_path` | `""` | Explicit path to the whisper CLI; empty = `PATH` lookup |

## Vision (`vision`)

Configures the `vision` tool (MiniCPM-V via `llama-mtmd-cli`).

| Field | Default | Description |
|-------|---------|-------------|
| `backend` | `local` | `local` uses `llama-mtmd-cli`; `provider` uses the configured main LLM provider |
| `provider` | main `provider` | Optional provider override when `backend` is `provider`; credentials and endpoint come from the existing `providers` map |
| `model` | `""` | Provider model identifier; required when `backend` is `provider` |
| `max_tokens` | `1024` | Maximum provider vision output tokens (maximum `16384`) |
| `models_dir` | `/usr/local/share/minicpm-v/models` (container) with fallback to `~/.odek/minicpm-v/models` | Directory containing `model.gguf` and `mmproj.gguf` |
| `binary_path` | `""` | Explicit path to `llama-mtmd-cli`; empty = `PATH` lookup |
| `video_frames` | `8` | Number of frames sampled evenly from a video |
| `auto_describe` | `true` | Automatically describe photos received over Telegram before the agent answers (mirrors `transcription.auto_transcribe`) |

`vision` is operator-only and uses the shared `llm.request_timeout_seconds` for
provider requests. It has no vision-specific secret, endpoint, format, or
timeout fields. `video_frames` defaults to `8` and is capped at `32`.

`auto_describe` defaults to `true` whether or not a `vision` section is
present; set it to `false` explicitly to disable the Telegram preprocessing
path.

Example provider-backed configuration:

```json
{
  "provider": "openai",
  "providers": { "openai": { "api_key": "${OPENAI_API_KEY}" } },
  "vision": { "backend": "provider", "model": "gpt-4.1-mini", "max_tokens": 1024 }
}
```

## Text-to-speech (`tts`)

Configures provider-backed text-to-speech (the `speak` tool). There is no
local TTS backend, so the tool stays unregistered until this section is
present with `backend: "provider"`.

| Field | Default | Description |
|-------|---------|-------------|
| `backend` | `""` | Must be `provider` (no local TTS backend exists) |
| `provider` | main `provider` | Optional provider override; credentials and endpoint come from the existing `providers` map |
| `model` | `""` | Provider TTS model identifier; required |
| `voice` | `""` | Voice name (e.g. `alloy`); required and validated at load time |
| `format` | `mp3` | Audio container; empty falls back to the provider default |
| `speed` | `0` (omit) | Speaking-rate multiplier; `0` = provider default, capped at `4.0` |
| `max_chars` | `4096` | Per-call input text cap (maximum `65536`) |
| `telegram_voice_replies` | `false` | Send Telegram answers as voice messages when TTS is configured |

`tts` is operator-only and uses the shared `llm.request_timeout_seconds`,
bounded by the remaining run budget.

### Failure behavior (`tts`)

- **Section absent or backend not `provider`** — the `speak` tool is not
  registered; the model never sees it.
- **Invalid values at load time** (bad `speed`, `max_chars`, `voice`, missing
  `model`/`voice`) — a loud stderr error is printed and text-to-speech is
  disabled; the tool stays unregistered.
- **Provider configured but API key missing** — the tool returns an
  actionable error naming the expected environment variable; keys are never
  included in errors.
- **Request failures mid-call** — collapsed to a generic, key-free provider
  error the agent can surface; the run continues.
- **Empty or oversized provider audio** — explicit in-band tool errors.

The `speak` tool and provider transcription are classified as
`NetworkEgress` in addition to their local write: both send text/audio to a
third-party endpoint, so profiles without egress approval will decline them.

## Speech-to-text (`stt`)

Configures speech-to-text. Omitting the whole section preserves local
whisper.cpp behavior (the `transcription` section); `backend: "provider"`
opts into provider transcription.

| Field | Default | Description |
|-------|---------|-------------|
| `backend` | `local` | `local` uses whisper.cpp; `provider` uses the configured main LLM provider |
| `provider` | main `provider` | Optional provider override when `backend` is `provider` |
| `model` | `""` | Provider model identifier; required when `backend` is `provider` |
| `max_audio_mb` | `25` | Maximum accepted audio upload size in MB (maximum `25`, matching the provider ceiling) |

`stt` is operator-only: project-level `./odek.json` cannot set it. Both `tts`
and `stt` are rejected from project config with the same warning pattern as
`vision`.

### Failure behavior (`stt`)

- **Section absent** — local whisper.cpp behavior, byte-for-byte unchanged
  (the `transcription` section stays authoritative).
- **Invalid values at load time** — a loud stderr error is printed and the
  backend falls back to local whisper; transcription never silently breaks.
- **`backend: "provider"` with the provider down** — transcription fails
  with a clear provider error; there is **no silent fallback to whisper**
  (an unintended backend switch would mask the configuration problem).
- **Oversized audio** — rejected before upload with the `max_audio_mb` limit
  named in the error.

## Tool Progress

Controls how per-tool progress messages appear inside the Telegram bot during agent runs. Independent from `interaction_mode` — you can have engaging terminal output with minimal Telegram progress, or verbose terminal with rich progress bubbles.

```json
{
  "tool_progress": "all",
  "tool_progress_cleanup": true
}
```

### `tool_progress`

| Value | Behavior | Use case |
|-------|----------|----------|
| `"all"` (default) | Single editable progress bubble with smart previews — e.g. `📝 read_file: "main.go"`. Includes edit throttling (1.5s), tool dedup (`×N` counter for repeated same-tool), and automatic flood-control fallback | General use — shows what the agent is doing without spamming the chat |
| `"new"` | Same as `"all"` but only updates when the tool name changes. Consecutive `read_file` calls produce one line; a `shell` call starts a new line | Long-running agents with repetitive tool chains (e.g. reading 50 files in batch) |
| `"verbose"` | Raw tool arguments as separate messages. Each tool call sends a new message with full JSON args; on completion the result is sent as a new message `✅ (12ms, 2KB)` including execution latency and result size | Debugging — see exactly what the agent passes to each tool and how long it takes |
| `"off"` | No per-tool progress messages at all. Only the initial "🤔 Looking into that..." and final answer are shown | Privacy-sensitive contexts or users who prefer zero noise |

### `tool_progress_cleanup`

Default: `true`. Controls whether the progress message bubble is deleted after the agent's final answer arrives:
- `true` — delete the progress bubble (clean chat, no stale tool traces)
- `false` — keep the progress bubble as a breadcrumb of what the agent did

### How it works

The progress system is an evolving single message that gets edited in-place (similar to an animated status). Each tool call adds a line like:

```
📝 read_file: "main.go"
💻 shell: "npm test"
📝 read_file: "utils.go" (×3)
```

Key behaviors:
- **Smart previews** — instead of showing raw JSON args, the system extracts meaningful context: filename for file tools, the command text for shell, URL for browser, query text for memory/search tools, audio filename for transcribe, file path for vision, query for web_search
- **Edit throttling** — edits are rate-limited to one every 1.5 seconds to avoid hitting Telegram's flood control limits. Rapid tool chains don't produce 429 errors
- **Tool dedup** — when the same tool runs consecutively (common with repeated native tool calls), identical lines are collapsed into a `(×N)` counter instead of repeating N times
- **Flood control fallback** — if an edit message fails with "flood" or "retry after", the system automatically switches to sending new messages instead of editing. This prevents the bot from becoming unresponsive under heavy load
- **Content reset** — when the agent calls `send_message` mid-run to send an interim message, the progress bubble resets below that content, keeping the chat timeline in correct order

## Background commands (`background`)

The `bg_*` tool family lets the agent start long-running shell commands (dev
servers, watchers, fuzz runs) that outlive the turn that started them. Jobs
are session-scoped: they are killed when the owning session or the process
ends — there is no detach mode in v1.

```json
"background": {
  "enabled": true,
  "max_jobs": 8,
  "max_output_bytes": 1048576,
  "max_timeout_seconds": 0,
  "notify": "observe",
  "on_session_end": "kill",
  "wake_on_complete": true,
  "wake_coalesce_ms": 2000,
  "max_wakes_per_hour": 30
}
```

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Master switch; `false` removes the tools from every surface. |
| `max_jobs` | `8` | Concurrent running jobs per session; further `bg_start` calls fail. |
| `max_output_bytes` | `1048576` | In-memory output ring per job (oldest bytes drop first). Output is never written to disk. |
| `max_timeout_seconds` | `0` | Cap for explicit `timeout_seconds` on `bg_start`; `0` = uncapped (session lifetime is the bound). |
| `notify` | `"observe"` | `"observe"` injects a drained completion summary at the agent's next iteration; `"off"` requires polling with `bg_status`. |
| `on_session_end` | `"kill"` | Job fate at session end. Only `"kill"` is supported; there is no detach. |
| `wake_on_complete` | `true` | Serve surface: when a job finishes while its session is idle **and** a WebUI connection is attached, start a system-initiated turn so the model reads `bg_output` and reports unprompted. Telegram surface: the same setting wakes idle chats with a system-initiated turn (exits within `wake_coalesce_ms` coalesce into one; busy chats keep the raw exit line). Wake turns are system messages. Forced off by `notify: "off"` (a wake would point at notices that are never delivered); bounded per chat/session by `max_wakes_per_hour`. |
| `wake_coalesce_ms` | `2000` | Window in which jobs finishing together share one wake turn. Global-only: project configs may not set it. |
| `max_wakes_per_hour` | `30` | Per-session ceiling on system-initiated wake turns (spend control). `0` disables waking; values above `240` clamp to `240` regardless of config source. Project configs may only lower an operator-set value. |

A project `odek.json` may only LOWER the numeric caps (same clamp philosophy
as `limits`); a project value of `0` or a negative number does not count as lower and
keeps the operator's value. Headless `odek run` and scheduled runs are single-session
processes: background jobs end when the run ends. `/jobs` (REPL and Telegram)
lists live jobs. On `odek serve`, `bg_*` tools are available in sandbox mode. Each job is
pinned to the launching agent’s container; disconnecting or completing a REST
run releases the agent’s ownership but keeps that container until its jobs end.
Job access remains session-scoped across reconnects. Server shutdown stops all
jobs and drains pending cleanup with a bounded deadline. Deleting a session
stops its jobs and rejects late launches. Failed container removals retain retry
ownership for maintenance and final shutdown. All execution modes retry Docker
removal up to three times, with a five-second deadline per attempt. If removal
still fails, cleanup reports the container name and a manual removal command;
short-lived CLI processes cannot keep retrying after exit.
Missing sandbox routing fails closed.
Sandbox images must provide `sh` and `setsid` for process-group cleanup.

Wake-on-complete also emits two WebSocket frames for clients: `bg_job` on
every job transition (`job_id`, `session_id`, `status`, and — once terminal —
`exit_code`, `duration_ms`, `output_bytes`, plus a secret-redacted, 80-char
`command_head`; terminal-only fields are absent, not zero, while running),
and `bg_wake` when the server starts a system-initiated wake turn. Wake turns
are announced to clients like any turn: a `turn_started` frame with
`initiated: "system"` follows the `session` frame (see [WEBUI.md](WEBUI.md)).
Frames are
chronological; clients should upsert by `job_id` and ignore unknown types
(old clients are unaffected — the keys are simply absent from their
vocabulary).

## odek init

Create a config file template. The two scopes use **different templates** because project configs are untrusted — the loader ignores sensitive fields in `./odek.json` (see [Project overrides](#project-overrides-odekjson) above), so the local template only contains project-safe fields and loads warning-free.

```bash
# Local project config (./odek.json) — project-safe fields only
odek init
odek init --local   # explicit equivalent

# Global config (~/.odek/config.json) — full operator schema
odek init --global

# Overwrite existing file
odek init --force
```

The **global template** covers the full schema: connection (`provider`, `providers`, `model`, `llm`), execution (`max_iterations`, `max_tool_parallel`, `prompt_caching`, `compaction`, `announce_budget`, `interaction_mode`), sandbox resource knobs (the `sandbox` key itself is deliberately absent — unset inherits the default-on posture), `dangerous` (with `non_interactive` pinned to the documented `read_only` default), `guard`, `tools`, `profiles`, `skills`, `memory` (including the `extract_facts` / `auto_approve_episodes` opt-outs), `subagent` (including `max_depth`, `announce_budget`, `budget_inherit`, `default_profile`), `limits`, `planning`, `mcp_servers`, `web_search`, `transcription`, `vision`, `trusted_proxies`, `schedules`, `maintenance`, and `telegram`. Blocks whose mere presence changes behavior (`embedding`, `memory.embedding`, `sessions.embedding`, `skills.embedding`) are intentionally omitted — add them only when you actually run an embedder. Top-level `base_url` / `api_key` remain v1 aliases (see [MIGRATION.md](MIGRATION.md)).

The **local template** contains only fields a project may legitimately set (`model`, `thinking`, iteration/parallelism limits, `interaction_mode`, inherit-neutral `prompt_caching`/`stream`/`compaction`/`announce_budget`/`planning`/`limits`, sandbox resource knobs, `tools.disabled`, `skills` without `dirs`, `mcp_servers`). `schedules` is omitted entirely — it is global-only and the loader ignores it from project configs. Operator-only fields (`provider`, `providers`, `llm`, `api_key`, `base_url`, `system`, `dangerous`, `memory`, `sessions`, `embedding`, `guard`, `maintenance`, `telegram`, `web_search`, `transcription`, `vision`, `trusted_proxies`, `tools.enabled`, `skills.dirs`, `subagent`, `profiles`) belong in `~/.odek/config.json`. Project configs may only *enable* the sandbox — `"sandbox": false` is rejected, so neither template pins it locally.

## Recommended minimal config

A complete operator setup needs only **three files**. Everything not pinned below keeps its safe default (sandbox on, `extract_facts` off, episodes require manual approval, compaction, planning, prompt caching, and streaming on). The sample deliberately pins the few keys whose defaults are worth making explicit, and leaves the rest out — a shorter config is easier to audit.

**1. Secrets** — `~/.odek/secrets.env` (chmod 600). Never put keys in config files:

```bash
DEEPSEEK_API_KEY=sk-...
```

**2. Global config** — `~/.odek/config.json` (operator-only settings live here):

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",

  "interaction_mode": "engaging",

  "limits": {
    "max_runtime_seconds": 3600,
    "max_tool_calls": 300,
    "max_input_tokens": 4000000,
    "max_output_tokens": 500000,
    "max_cost_usd": 25,
    "input_cost_per_million_usd": 0.14,
    "output_cost_per_million_usd": 0.28
  },

  "maintenance": { "enabled": true }
}
```

Why each key is pinned:

- **`provider` + `model`** — the connection identity. The key comes from the provider env (`DEEPSEEK_API_KEY` for the default) or `providers.<id>.api_key`. A custom URL belongs under `providers.<id>.base_url`, not a top-level `base_url`.
- **`interaction_mode: "engaging"`** — the default, pinned so a future odek default change cannot silently alter your output.
- **`limits`** — a runaway agent stops at the wall-clock, tool-call, token, and spend ceilings instead of your invoice. Cost enforcement activates only because both per-million prices are set; add `model_prices` entries keyed by exact model ID when you use several models.
- **`maintenance.enabled`** — sessions, audit records, and logs get retention-swept automatically.

Deliberately **not** set, because the defaults are the recommendation:

- `sandbox` — on by default for `run`/`repl`/`serve`; `continue` pins the session bit; never turn it off on a host that runs untrusted code.
- `memory.extract_facts: false` and `memory.auto_approve_episodes: false` — the secure defaults; flip only with the trade-offs understood (see [`extract_facts`](#extract_facts--automatic-fact-learning-opt-in-off-by-default)).
- `dangerous` — the built-in class defaults (destructive/blocked/unknown denied; system writes, uploads, code execution and installs prompted; egress allowed) are the right posture; tighten per-project with an `allowlist`/`denylist` only when needed.
- `web_search.base_url` — empty hides the tool; set it only if you run a SearXNG instance.
- `mcp_servers` — none; each entry is arbitrary-code execution by design, add them deliberately.

**3. Project overrides** — `./odek.json` (optional, project-safe fields only):

```json
{
  "max_iterations": 30,
  "tools": { "disabled": ["browser", "http_request"] }
}
```

Projects can only *narrow* things — disable tools, lower limits, tighten skills — never widen them. Sensitive sections (`provider`, `providers`, `llm`, `api_key`, `base_url`, `dangerous`, `memory`, `guard`, …) are ignored with a warning if a cloned repo tries to set them.

## Quick examples

```bash
# Set API key via secrets.env (recommended — keeps secrets out of config files)
echo 'DEEPSEEK_API_KEY="sk-..."' >> ~/.odek/secrets.env
chmod 600 ~/.odek/secrets.env

# Global config (provider + model; key stays in secrets.env)
echo '{"provider":"deepseek","model":"deepseek-v4-flash"}' > ~/.odek/config.json
odek run "list files"

# Per-project override
echo '{"max_iterations": 30}' > ./odek.json
odek run "quick status"

# Project-level sandbox knobs require explicit approval (or the CI bypass)
# because they can read host env vars and pick arbitrary images/networks.
echo '{"sandbox": true, "sandbox_env": {"X": "${HOME}"}}' > ./odek.json
ODEK_APPROVE_PROJECT_SANDBOX=1 odek run "run untrusted script"

# An implicit Dockerfile.odek build is gated the same way (docker build runs
# repo-controlled RUN steps with the whole working directory as context).
# Builds use --network=none unless you opt in:
ODEK_SANDBOX_BUILD_NETWORK=1 odek run --sandbox "build the project"

# Env var override for one-off
ODEK_SANDBOX=true odek run "run untrusted script"

# Enable Extended Memory via CLI flag
odek run --memory-extended-enabled "remember that I prefer Go over Python"

# Or configure it globally in ~/.odek/config.json (memory cannot be set in ./odek.json)
# { "memory": { "extended": { "enabled": true } } }

# Sub-agent config is operator-only (ignored from ./odek.json)
# { "subagent": { "max_concurrency": 5, "timeout_seconds": 300 } }  → ~/.odek/config.json

# CLI flag always wins
odek run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```

## Runtime logging

Operational logging is enabled by default for CLI commands and writes
metadata-only JSONL records to `~/.odek/runtime.log`. The `logging` section is
operator-only; it sets the level, file, rotation size/count, and retention age.
`maintenance.enabled` and `maintenance.interval_minutes` control when the
janitor runs, while logging settings control log retention and rotation. Go
library users opt in by setting `Config.RuntimeLogPath`. See
[Runtime logging](LOGGING.md) for configuration, querying, and field meanings.
