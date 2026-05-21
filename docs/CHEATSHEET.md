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
odek mcp --sse-addr :8081            # Expose via SSE

# Sandbox flags (apply to run/repl/serve)
odek run --sandbox "build safely"
odek serve --sandbox --sandbox-readonly --sandbox-network none
odek repl --sandbox --sandbox-image python:3.12
```

## Configuration (odek.json / ~/.odek/config.json)

```json
{
  "model": "deepseek-chat",
  "max_iterations": 30,
  "sandbox": true,
  "sandbox_image": "alpine:latest",
  "sandbox_network": "bridge",
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

  cos > 0.7  ─────→ auto-merge (replace)
  cos < 0.3  ─────→ auto-add
  0.3–0.7   ─────→ LLM judges → merge or add
```

Saves ~80% of LLM calls on memory writes.

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

Default network: `bridge` (internet access). Set `none` for air-gapped execution.

## Telegram Bot

- Requires `ODEK_TELEGRAM_BOT_TOKEN` env var
- Slash commands: `/start`, `/help`, `/new`, `/plan`, `/plans`, `/plan-view`, `/plan-delete`, `/sessions`, `/resume`, `/prune`, `/stats`, `/stop`, `/mode`, `/restart`
- Voice messages: automatically processed via `DownloadVoice` → OGG files in `~/.odek/media/`
- Photos: automatically processed via `DownloadPhoto` → JPG files in `~/.odek/media/`
- Conversations persist across bot restarts (`tg-<chatID>` sessions)
- Session TTL: 24h default (configurable)
- Daily token budget tracking in `~/.odek/telegram_token_usage_<date>`
- Fallback API URLs for regions where `api.telegram.org` is blocked
- Access control: restrict by chat ID or user ID
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
odek mcp --sse-addr :8081                   # SSE transport
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
| `ODEK_SYSTEM` | system |
| `ODEK_NO_COLOR` | no_color |
| `ODEK_NO_AGENTS` | no_agents |
| `ODEK_MAX_CONCURRENCY` | max_concurrency |
| `ODEK_CTX` | ctx (comma-separated file paths) |
| `DEEPSEEK_API_KEY` | api_key (fallback) |
| `OPENAI_API_KEY` | api_key (final fallback) |

## Key Design Properties

- **Minimal Go dependencies** — all zero-dep Go packages from 21no.de
- **~11 MB static binary**
- **One loop, one interface** — tool implementers write `func Call(args string) (string, error)`
- **File-based config** — no YAML, no DSL, no schema generation
- **Sandbox is opt-in** — no container runtime required for basic operation
