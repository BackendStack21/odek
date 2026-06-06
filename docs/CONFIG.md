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
- **Layer 3** `ODEK_*` env var lookups (e.g. `ODEK_API_KEY`)
- **Legacy fallbacks** like `DEEPSEEK_API_KEY` / `OPENAI_API_KEY`

## Config files

### Global defaults (`~/.odek/config.json`)

Shared across all projects:

```json
{
  "model": "deepseek-v4-flash",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "${ODEK_API_KEY}",
  "thinking": "",
  "max_iterations": 90,
  "sandbox": false,
  "interaction_mode": "engaging",
  "no_color": false,
  "no_agents": false,
  "max_tool_parallel": 4,
  "tool_progress": "all",
  "tool_progress_cleanup": true,
  "system": ""
}
```

### Project overrides (`./odek.json`)

Same schema as global. Only set the fields you want to override:

```json
{
  "model": "gpt-4o",
  "base_url": "https://api.openai.com/v1",
  "max_iterations": 30
}
```

Both files are optional. Missing files are silently ignored. String values support `${VAR}` environment variable substitution — useful for API keys without plaintext storage.

## Secrets file (`~/.odek/secrets.env`)

Auto-loaded on every `odek` invocation before any config file or env var is read.
Each `KEY=VALUE` line is injected into the process environment via `os.Setenv`.

```
ODEK_API_KEY=sk-...
GITHUB_TOKEN=ghp_...
```

Rules:
- **File format:** `KEY=VALUE` — one per line, no `export` keyword needed
- **Blank lines and `#` comments** are skipped
- **Existing env vars are NOT overwritten** — if `ODEK_API_KEY` is already in the environment, the file is ignored for that key
- **Missing/unreadable file** is silently ignored (not an error)
- **Permissions:** keep `0600` (`chmod 600 ~/.odek/secrets.env`)

This lets you keep secrets out of config files entirely:

```json
// ~/.odek/config.json — no plaintext secrets
{
  "model": "deepseek-v4-flash",
  "api_key": "${ODEK_API_KEY}"      // ← resolved from secrets.env at runtime
}
```

## Environment variables

Every config knob has a `ODEK_*` counterpart:

| Variable | Maps to | Type |
|----------|---------|------|
| `ODEK_MODEL` | `--model` | string |
| `ODEK_BASE_URL` | `--base-url` | string |
| `ODEK_API_KEY` | config files only | string |
| `ODEK_THINKING` | `--thinking` | string |
| `ODEK_MAX_ITER` | `--max-iter` | int |
| `ODEK_SANDBOX` | `--sandbox` | bool |
| `ODEK_INTERACTION_MODE` | `--interaction-mode` | string |
| `ODEK_NO_COLOR` | `--no-color` | bool |
| `ODEK_NO_AGENTS` | `--no-agents` | bool |
| `ODEK_SYSTEM` | `--system` | string |
| `ODEK_SKILLS_LEARN` | `skills.learn` | bool |
| `ODEK_PROMPT_CACHING` | `prompt_caching` | bool |
| `ODEK_TOOL_PROGRESS` | `tool_progress` | string (all\|new\|verbose\|off) |
| `ODEK_SANDBOX_IMAGE` | `--sandbox-image` | string |
| `ODEK_SANDBOX_NETWORK` | `--sandbox-network` | string |
| `ODEK_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
| `ODEK_SANDBOX_MEMORY` | `--sandbox-memory` | string |
| `ODEK_SANDBOX_CPUS` | `--sandbox-cpus` | string |
| `ODEK_SANDBOX_USER` | `--sandbox-user` | string |
| `ODEK_MAX_TOOL_PARALLEL` | `max_tool_parallel` | int |

## API key fallback order

`ODEK_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`

## Parallel tool execution

When a model emits multiple tool calls in one response (`tool_calls` array with N entries), odek executes them **concurrently** in goroutines bounded by a semaphore.

| Field | Default | Env var | Description |
|-------|---------|---------|-------------|
| `max_tool_parallel` | `4` | `ODEK_MAX_TOOL_PARALLEL` | Max concurrent tool calls per iteration. 0 = default 4. Set to 1 for sequential execution. |

I/O-bound tools (read_file, search_files, shell) benefit most — latency drops from `sum(latencies)` to `max(latency)`.

**Approval gate:** When an approver is configured and the LLM returns multiple tool calls, a single batch approval prompt is shown before any tool executes. If approved, all tools run in parallel. If denied, no tools run.

## Skills configuration

The `skills` section controls the skill system:

```json
{
  "skills": {
    "max_auto_load": 3,
    "max_lazy_slots": 5,
    "learn": true,
    "llm_learn": true,
    "llm_curate": true,
    "import": {
      "max_size_bytes": 1048576,
      "timeout_seconds": 5,
      "require_https": false
    },
    "curation": {
      "staleness_days": 90,
      "auto_prune": false,
      "auto_curate": true,
      "skip_threshold": 1,
      "skip_reset_days": 30
    },
    "auto_save": {
      "enabled": true,
      "require_llm": true,
      "max_per_run": 3
    }
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `max_auto_load` | — | 3 | Max skills injected into system prompt on start |
| `max_lazy_slots` | — | 5 | Max skills loaded per user input via trigger matching |
| `learn` | `ODEK_SKILLS_LEARN` | `true` | Enable skill learning mode (detects patterns, suggests skills). On by default |
| `llm_learn` | — | `true` | Use LLM to enrich detected patterns. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `llm_curate` | — | `true` | Use LLM for curation quality assessment. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `dirs` | — | [] | Extra skill directories beyond `~/.odek/skills` and `./.odek/skills` |
| `import.max_size_bytes` | — | 1048576 (1MB) | Max size for fetched skill content |
| `import.timeout_seconds` | — | 5 | HTTP timeout for skill URI fetch |
| `import.require_https` | — | false | Reject http:// URIs when true |
| `curation.staleness_days` | — | 90 | Days without use before flagging as stale |
| `curation.auto_prune` | — | false | Auto-delete stale skills on curate (no prompt) |
| `curation.auto_curate` | — | true | Run auto-curation after sessions (merge, dedup, prune) |
| `curation.skip_threshold` | — | 1 | Times a skill must be skipped before permanent suppression |
| `curation.skip_reset_days` | — | 30 | Days after which a skip expires (re-allows suggestion) |
| `auto_save.enabled` | — | true | Auto-save quality skill suggestions without prompting |
| `auto_save.require_llm` | — | true | Only auto-save if LLM enhancement was applied |
| `auto_save.max_per_run` | — | 3 | Max skills to auto-save per session |

## Memory configuration

The `memory` section controls the persistent memory system (see [docs/MEMORY.md](docs/MEMORY.md)):

```json
{
  "memory": {
    "enabled": true,
    "facts_limit_user": 1500,
    "facts_limit_env": 2500,
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
    "auto_approve_episodes": false
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | true | Enable memory system entirely |
| `facts_limit_user` | 1500 | Max chars for `user.md` fact file |
| `facts_limit_env` | 2500 | Max chars for `env.md` fact file |
| `buffer_lines` | 20 | Max turn summaries in session buffer |
| `buffer_enabled` | true | Enable the turn-level buffer |
| `merge_on_write` | true | Use go-vector RP similarity to auto-merge related entries (fast, no LLM — uses simple string merge) |
| `consolidate_on_end` | true | At session end, run an LLM consolidation pass over `user.md` and `env.md` in a background goroutine. This is the quality complement to `merge_on_write`: merge-on-write handles obvious duplicates immediately (no LLM), while consolidation handles near-duplicates and paraphrases at session end with full LLM quality. Requires `llm_consolidate: true`. **Note:** facts in the borderline similarity band (0.3–0.7 cosine) are now always added immediately and only merged by this consolidation pass — if you set `consolidate_on_end: false`, near-duplicate facts will accumulate rather than being merged. |
| `extract_on_end` | true | At session end (≥3 turns), extract a narrative episode summary via LLM for later recall |
| `extract_facts` | **false** | **Opt-in.** At session end (≥3 turns), auto-extract a few **durable** facts (stable user preferences, project invariants) into `user.md`/`env.md`. Off by default — see the security note below. Independent of `extract_on_end`; to disable *all* end-of-session LLM extraction set `llm_extract: false`. |
| `llm_search` | true | Use LLM to rerank candidates for **explicit** `memory search` calls (the `memory` tool). Per-turn recall (`FormatEpisodeContext`) always uses the cached go-vector index — no LLM call on the hot path regardless of this setting. |
| `llm_extract` | true | Use LLM for end-of-session fact extraction |
| `llm_consolidate` | true | Use LLM to merge related fact entries |
| `merge_threshold` | 0.7 | Cosine similarity above which two fact entries are **auto-merged** without an LLM call (0.0–1.0). Raise it to merge less aggressively; lower it to merge more. |
| `add_threshold` | 0.3 | Cosine similarity below which a new fact entry is **auto-added** without an LLM call (0.0–1.0). Between `add_threshold` and `merge_threshold` the LLM decides. Keep `add_threshold` < `merge_threshold`. |
| `auto_approve_episodes` | false | **Security trade-off.** When true, untrusted episodes (sessions that touched web/MCP/out-of-workspace content) are auto-approved at session end so they are recalled without a manual `odek memory promote`. Leaving it `false` keeps the human review gate (recommended). |

### ⚠️ `extract_facts` — automatic fact learning (opt-in, off by default)

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

The `subagent` section controls task decomposition and parallel sub-agent execution (see [docs/SUBAGENTS.md](docs/SUBAGENTS.md)):

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
| `max_concurrency` | 3 | Max sub-agents running in parallel (max 8) |
| `timeout_seconds` | 120 | Default timeout per sub-agent (overridden by `--timeout`) |
| `max_iterations` | 15 | Default max think→act cycles per sub-agent (overridden by `--max-iter`) |

This section is optional. Omitted fields inherit sensible defaults.

> **Note**: The `subagent` section is currently read only from `odek.json` by the `odek subagent` command in test code. Runtime values (`max_concurrency`, `timeout_seconds`) are hardcoded in production `odek run`/`odek serve`. This may be wired up fully in a future release.

## MCP server configuration

Connect to **external MCP servers** and expose their tools to the agent.
Any MCP server that works with Claude Code works with odek — same config format.

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
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `command` | The executable to run |
| `args` | Optional command-line arguments |
| `env` | Optional environment variable overrides (empty string removes from env) |

Tools are registered as `<server_name>__<tool_name>` (e.g., `playwright__navigate`)
and are available in `odek run`, `odek repl`, `odek continue`, and `odek serve`.

See [docs/MCP.md](docs/MCP.md#odek-as-mcp-client) for detailed instructions.

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
    "session_ttl_hours": 24,
    "log_level": "info",
    "log_file": "",
    "default_chat_id": 8592463065
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `bot_token` | `ODEK_TELEGRAM_BOT_TOKEN` | — (required) | Telegram bot API token from @BotFather |
| `allowed_users` | — | all | Restrict bot to specific user IDs |
| `allowed_chats` | — | all | Restrict bot to specific chat IDs |
| `poll_interval` | — | 1 | Seconds between poll cycles |
| `poll_timeout` | — | 30 | Long-poll timeout (1-60 seconds) |
| `max_msg_length` | — | 4096 | Max characters per message |
| `session_ttl_hours` | — | 24 | Hours before inactive session expires |
| `log_level` | — | info | Log level: debug, info, warn, error |
| `log_file` | — | stderr | Log file path (empty = stderr) |
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

See [docs/TELEGRAM.md](docs/TELEGRAM.md#cron-integration) for full cron setup instructions.

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
    "allow_telegram_management": true
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

Full guide: [docs/SCHEDULES.md](SCHEDULES.md).

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
- **Smart previews** — instead of showing raw JSON args, the system extracts meaningful context: filename for file tools, the command text for shell, URL for browser, query text for memory/search tools, audio filename for transcribe
- **Edit throttling** — edits are rate-limited to one every 1.5 seconds to avoid hitting Telegram's flood control limits. Rapid tool chains don't produce 429 errors
- **Tool dedup** — when the same tool runs consecutively (common with parallel batch tools like `batch_read`), identical lines are collapsed into a `(×N)` counter instead of repeating N times
- **Flood control fallback** — if an edit message fails with "flood" or "retry after", the system automatically switches to sending new messages instead of editing. This prevents the bot from becoming unresponsive under heavy load
- **Content reset** — when the agent calls `send_message` mid-run to send an interim message, the progress bubble resets below that content, keeping the chat timeline in correct order

## odek init

Create a config file template:

```bash
# Local project config (./odek.json)
odek init

# Global config (~/.odek/config.json)
odek init --global

# Overwrite existing file
odek init --force
```

## Quick examples

```bash
# Set API key via secrets.env (recommended — keeps secrets out of config files)
echo 'ODEK_API_KEY="sk-..."' >> ~/.odek/secrets.env
chmod 600 ~/.odek/secrets.env

# Global config (model and other settings only, no secrets)
echo '{"model": "deepseek-v4-flash"}' > ~/.odek/config.json
odek run "list files"

# Per-project override
echo '{"max_iterations": 30}' > ./odek.json
odek run "quick status"

# Env var override for one-off
ODEK_SANDBOX=true odek run "run untrusted script"

# Enable skill learning via env var
ODEK_SKILLS_LEARN=true odek run "set up CI"

# Sub-agent config (project-level)
echo '{"subagent": {"max_concurrency": 5, "timeout_seconds": 300}}' > ./odek.json

# CLI flag always wins
odek run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```
