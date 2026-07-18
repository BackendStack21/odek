# odek Cheat Sheet

## CLI Quick Reference

```bash
odek run "build a REST API"          # Single-shot task
odek run --ctx schema.sql "query"     # Single-shot with file context
odek run -c main.go,lib.go "compare"  # Multiple files via short flag
odek repl                            # Interactive session
odek repl --id <uuid>                # Resume session
odek serve                           # Web UI (http://127.0.0.1:8080)
odek serve --open                    # Web UI + auto-open browser
odek subagent --goal "review auth"   # Spawn subagent
odek mcp                             # Expose tools via MCP stdio
odek telegram                        # Telegram bot (also hosts the scheduler)

# Scheduled tasks (native cron — see docs/SCHEDULES.md)
odek schedule add --cron "0 9 * * 1-5" --deliver telegram "stand-up nudge"
odek schedule list                   # List jobs (id, next fire, last status)
odek schedule next "*/15 * * * *"    # Preview upcoming fire times
odek schedule daemon                 # Run the scheduler headless

# Sandbox flags (apply to run/repl/serve)
odek run --sandbox "build safely"
odek serve --sandbox --sandbox-readonly --sandbox-network none
odek repl --sandbox --sandbox-image python:3.12
```

## Configuration (odek.json / ~/.odek/config.json)

```json
{
  "model": "deepseek-v4-flash",
  "max_iterations": 30,
  "sandbox": true,
  "sandbox_image": "alpine:latest",
  "sandbox_network": "none",
  "max_concurrency": 3,

  "skills": {
    "learn": true,
    "max_auto_load": 5
  },

  "memory": {
    "enabled": true,
    "facts_limit_user": 1500,
    "facts_limit_env": 2500,
    "merge_on_write": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3
  },

  "dangerous": {
    "approval": "always"
  },

  "mcp_servers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp"]
    }
  }
}
```

Priority: `~/.odek/config.json` ← `./odek.json` ← `ODEK_*` env ← CLI flags.

### Audio Transcription
- **`transcribe`** tool uses local whisper.cpp CLI — no cloud APIs
- Model files cached in `~/.odek/whisper/models/ggml-<model>.bin` (default: `tiny`, ~75 MB)
- Configure via `transcription` section in config:

```json
{
  "transcription": {
    "model": "tiny",
    "language": "en",
    "auto_transcribe": true,
    "models_dir": "~/.odek/whisper/models",
    "binary_path": "/usr/local/bin/whisper"
  }
}
```

Settings: `model` (tiny/base/small/medium), `language` (ISO code, empty=auto), `auto_transcribe` (Telegram voice → text), `models_dir` (model directory), `binary_path` (whisper binary path).

### Image & Video Understanding
- **`vision`** tool uses local MiniCPM-V 4.6 (1.3B) via `llama-mtmd-cli` — no cloud APIs
- Accepts images (JPEG, PNG, GIF, WebP, BMP) and videos (MP4, MOV, AVI, MKV, WebM)
- Videos are sampled into evenly-spaced frames with ffmpeg; all frames analysed in one call
- Model files: `model.gguf` (~529 MB, Q4\_K\_M) + `mmproj.gguf` (~1.1 GB) — bundled in the Docker image at `/usr/local/share/minicpm-v/models/`
- **Telegram photos auto-describe** (`auto_describe`, default on): a received photo is run through the vision model first to extract a description, then the agent answers using it. Any caption you send with the photo becomes your request and focuses the extraction.
- Configure via `vision` section in config:

```json
{
  "vision": {
    "auto_describe": true,
    "models_dir": "~/.odek/minicpm-v/models",
    "binary_path": "/usr/local/bin/llama-mtmd-cli",
    "video_frames": 8
  }
}
```

Settings: `auto_describe` (Telegram photo → description before the agent answers, default true), `models_dir` (dir with `model.gguf` + `mmproj.gguf`), `binary_path` (llama-mtmd-cli path), `video_frames` (frames to sample from video, default 8).

### Web Search
- **`web_search`** tool queries a self-hosted **SearXNG** metasearch instance — no cloud search API, no keys
- Returns ranked results (title, url, snippet, engine) + direct answers; results are wrapped as untrusted content
- The agent then fetches the URLs it wants with `browser` / `http_batch`
- **Registered only when `web_search.base_url` is set.** The Docker compose setup runs a SearXNG sidecar and sets it automatically; outside Docker, run SearXNG yourself and point `base_url` at it
- Gated as `network_egress` (prompts in restricted, allowed in godmode) — the backend URL is fixed config, so there is no SSRF surface
- Configure via `web_search` section:

```json
{
  "web_search": {
    "base_url": "http://searxng:8080",
    "categories": "general",
    "language": "en",
    "max_results": 10,
    "timeout_seconds": 15
  }
}
```

Settings: `base_url` (SearXNG instance; empty = tool disabled), `categories` (optional SearXNG categories), `language` (optional language code), `max_results` (default 10), `timeout_seconds` (default 15).

**Run SearXNG standalone (outside the bundled Compose):** the only non-default
requirement is enabling the JSON API — SearXNG ships HTML-only, so a stock
instance returns HTTP 403 for `format=json`. Reuse the repo's ready-made minimal
config (`docker/searxng/settings.yml` — JSON API on, anti-bot limiter off so no
Redis/Valkey is needed). Run from the **repo root** so the relative volume path
resolves (the image tag matches the one pinned in `docker/docker-compose.yml`):

```bash
docker run -d --name searxng -p 8888:8080 \
  -e SEARXNG_SECRET="$(openssl rand -hex 32)" \
  -v "$PWD/docker/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
  searxng/searxng:2026.6.8-f3fab143b
```

Then point odek at it (global `~/.odek/config.json` or project `./odek.json`):

```json
{ "web_search": { "base_url": "http://127.0.0.1:8888" } }
```

If you bring your own `settings.yml`, the two settings that matter are
`search.formats: [html, json]` (enables the API) and, for a private single-user
instance, `server.limiter: false` (drops the Redis/Valkey dependency).

## Memory System Architecture

### Three Tiers

```
~/.odek/memory/
├── facts/
│   ├── user.md          → User profile (cap: 1,500 chars)
│   └── env.md           → Environment facts (cap: 2,500 chars)
├── project-facts/       → Per-project overlays (optional)
└── episodes/
    ├── <session-id>.md  → LLM-extracted session summaries
    └── index.json       → Search metadata
```

### Durability Model

| Component | Persists? | Source of truth |
|-----------|-----------|-----------------|
| Fact text (user.md, env.md) | ✅ Plain markdown | The text is the durable state |
| Episode summaries | ✅ Markdown files | Durable |
| Episode index | ✅ JSON | Durable |
| go-vector RP embeddings | ❌ Ephemeral | Rebuilt from text via `Fit()` |

**Key insight:** go-vector `RandomProjections` is stateless. `Fit(corpus)` builds vocabulary deterministically — same text → same embeddings. Rebuilt on every fact mutation. No persistent state to save or restore.

**Restart flow:**
1. Fact text loads from disk
2. MergeDetector starts empty
3. First mutation calls `Fit()` with all persisted facts → full merge protection
4. Until then, `Classify()` returns `"nobody"` → entry added directly (no merge check — optimization, not correctness)

### Merge-on-Write (go-vector)

```
RP.embed(newEntry) → cosine similarity vs each existing entry

  cos > 0.7  ─────→ simple merge (no LLM — substring or concatenation)
  cos < 0.3  ─────→ auto-add
  0.3–0.7   ─────→ auto-add (deferred to session-end consolidation)
```

AddFact makes zero LLM calls. Near-duplicate dedup happens at session end
via background consolidation (`consolidate_on_end`, default true).

### Memory Tool

Single `memory` tool, 6 actions: `add`, `replace`, `remove`, `consolidate`, `read`, `search`. Targets: `user` or `env`. Facts are frozen in system prompt at session start — live writes appear next session.

## Subagents

```bash
# Spawn from CLI
odek subagent --goal "audit security" --context "repo in /workspace"

# From agent tool
delegate_tasks tasks=[{goal: "task A", context: "..."}, {goal: "task B"}]
```

- Max 8 tasks per `delegate_tasks` call
- Max concurrency: configurable via `max_concurrency` / `ODEK_MAX_CONCURRENCY` (default 3)
- 120s timeout per subagent
- Subagents get read-only fact snapshot, no `memory` tool
- Results collated into summary, returned to calling agent

## Sandbox

```bash
odek run --sandbox --sandbox-image node:20 "install deps"
odek serve --sandbox --sandbox-readonly --sandbox-network none
odek repl --sandbox --sandbox-memory 2g --sandbox-cpus 2
```

Flags: `--sandbox`, `--sandbox-image`, `--sandbox-network`, `--sandbox-readonly`, `--sandbox-memory`, `--sandbox-cpus`, `--sandbox-user`.

Env vars: `ODEK_SANDBOX=true`, `ODEK_SANDBOX_IMAGE`, `ODEK_SANDBOX_NETWORK`, etc.

> **Project config approval:** sandbox knobs set in `./odek.json` (`sandbox_env`, `sandbox_image`, `sandbox_network`, `sandbox_volumes`) require an interactive approval prompt. Use `ODEK_APPROVE_PROJECT_SANDBOX=1` in CI/scripts, or set sandbox config via `~/.odek/config.json` / env vars / CLI flags instead.

Default network: `bridge` (internet access). Set `none` for air-gapped execution.

## Telegram Bot

- Requires `ODEK_TELEGRAM_BOT_TOKEN` env var
- Slash commands: `/start`, `/help`, `/new`, `/plan`, `/plans`, `/plan_view`, `/plan_delete`, `/plan_resume`, `/sessions`, `/resume`, `/prune`, `/stats`, `/stop`, `/mode`, `/restart`
- Plans: stored as `~/.odek/plans/<slug>.md`; `/plan` generates via agent, `/plan_resume` injects most recent plan into session
- Voice messages: automatically processed via `DownloadVoice` → OGG files in `~/.odek/media/`
- Photos: automatically processed via `DownloadPhoto` → JPG files in `~/.odek/media/`
- Conversations persist across bot restarts (`tg-<chatID>` sessions)
- Session TTL: 24h default (configurable)
- Daily token budget tracking in `~/.odek/telegram_token_usage_<date>`
- Spawn+exit restart: SIGHUP spawns child process then exits — no binary overwrite races, fresh connections
- Singleton lock: advisory `flock` on `~/.odek/telegram.lock` prevents duplicate polling instances
- Restart marker (`~/.odek/restart.json`) written with `0600` to protect active chat IDs
- Fallback API URLs for regions where `api.telegram.org` is blocked
- Access control: restrict by chat ID or user ID
- Incoming message/caption length enforced in UTF-16 code units, matching Telegram's limits
- `send_message` tool escapes text for Telegram MarkdownV2 before sending, so prompt-injected formatting cannot hide links or fake buttons
- Logging: configurable log level and log file

See [docs/TELEGRAM.md](docs/TELEGRAM.md) for full documentation.


## Sessions

- Stored in `~/.odek/sessions/<uuid>.json`
- `odek repl --id <uuid>` to resume
- Buffer preserved across resumption
- Sessions with ≥3 turns get episode extraction on close

## MCP

```bash
# Client mode — connect to external MCP servers
odek run "analyze this page"

# → Uses MCP servers defined in odek.json:
#   "mcp_servers": { "playwright": { "command": "npx", "args": ["@playwright/mcp"] } }

# Server mode — expose tools to other clients
odek mcp                                    # stdio transport
```

## Env Vars

| Variable | Maps to |
|----------|---------|
| `ODEK_MODEL` | model |
| `ODEK_BASE_URL` | base_url |
| `ODEK_API_KEY` | api_key |
| `ODEK_THINKING` | thinking |
| `ODEK_MAX_ITER` | max_iterations |
| `ODEK_SANDBOX` | sandbox |
| `ODEK_SANDBOX_IMAGE` | sandbox_image |
| `ODEK_SANDBOX_NETWORK` | sandbox_network |
| `ODEK_SANDBOX_READONLY` | sandbox_readonly |
| `ODEK_SANDBOX_MEMORY` | sandbox_memory |
| `ODEK_SANDBOX_CPUS` | sandbox_cpus |
| `ODEK_SANDBOX_USER` | sandbox_user |
| `ODEK_APPROVE_PROJECT_SANDBOX` | auto-approve project-level sandbox config (CI) |
| `ODEK_SYSTEM` | system |
| `ODEK_NO_COLOR` | no_color |
| `ODEK_NO_AGENTS` | no_agents |
| `ODEK_PROMPT_CACHING` | prompt_caching |
| `ODEK_MAX_CONCURRENCY` | max_concurrency |
| `ODEK_CTX` | ctx (comma-separated file paths) |
| `ODEK_API_KEY` | api_key (preferred) |
| `DEEPSEEK_API_KEY` | api_key (fallback) |
| `OPENAI_API_KEY` | api_key (final fallback) |

## Key Design Properties

- **Minimal Go dependencies** — all minimal-dependency Go packages from 21no.de
- **~11 MB static binary**
- **One loop, one interface** — tool implementers write `func Call(args string) (string, error)`
- **File-based config** — no YAML, no DSL, no schema generation
- **Sandbox is opt-in** — no container runtime required for basic operation

## Native Tools Reference

### File I/O (zero-fork, O_NOFOLLOW gated)
| Tool | Description |
|------|-------------|
| `read_file` | Read with line numbers, offset/limit pagination |
| `write_file` | Atomic temp+rename write, path confinement |
| `patch` | Find-and-replace with unified diff output |
| `batch_read` | Read N files in parallel, one call |
| `batch_patch` | Apply N edits atomically across files |
| `glob` | Find files by glob pattern |
| `file_info` | Stat metadata (size, mod_time, mode, type) |
| `sort` | Sort lines asc/desc/unique/numeric/case-insensitive |
| `head_tail` | First/last N lines, streaming, parallel |
| `search_files` | Regex content search or glob file find |

### Data Processing (in-process, no shell fork)
| Tool | Description |
|------|-------------|
| `math_eval` | Arithmetic via go/parser AST (`42*17+256/10 = 97`) |
| `diff` | LCS structured line diff |
| `json_query` | Dot-path query with array indexing (`users[0].name`) |
| `tr` | Text transform: upper/lower/char/string/delete |
| `base64` | Encode files/strings, decode base64 |
| `count_lines` | Streaming line/byte count, parallel files |
| `word_count` | Streaming word/line/char/byte count |
| `checksum` | SHA-256, SHA-1, MD5 hashing |
| `tree` | Structured directory tree listing |

> **Size limits:** file inputs for `sort`, `head_tail`, `diff`, `json_query`,
> `tr`, `base64`, `count_lines`, `word_count`, `checksum`, and `batch_patch` are
> capped at 10 MiB. Inline `string`/`content` arguments for `base64` and `tr` are
> also capped at 10 MiB to prevent prompt-injected multi-hundred-megabyte
> payloads from OOMing the process.

### Multi-Pattern (parallel goroutine search)
| Tool | Description |
|------|-------------|
| `multi_grep` | Search N regex patterns in parallel |
| `search_files` | Single-pattern content/file search |

### Execution (shell replacement)
| Tool | Description |
|------|-------------|
| `shell` | Single command, danger-classified |
| `parallel_shell` | N commands, true parallel, per-cmd timeout (capped at 30m), process-group kill on cancel |
| `http_batch` | N URLs parallel fetch (no HTML parse) |
| `browser` | HTTP fetch + regex HTML extraction; link URLs wrapped as untrusted |

### Agent Infrastructure
| Tool | Description |
|------|-------------|
| `delegate_tasks` | Spawn sub-agent OS processes |
| `memory` | Persistent fact CRUD with cosine-merge |
| `session_search` | Browse, search, and recall past sessions (semantic vector search) |
| `clarify` | Ask the user for clarification |
| `send_message` | Send text/photo/document to Telegram |
| `skill_load/list/save/patch/delete` | Skill CRUD |
