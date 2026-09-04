# odek

**Minimal Go autonomous agent runtime — tiny dep tree, small static binary, instant startup.**

One binary. One loop. Zero frameworks. ReAct (Reasoning + Acting) — think, therefore act.

```bash
# Install (requires Go ≥ 1.25.12 — see "Build requirements" below)
go install github.com/BackendStack21/odek/cmd/odek@latest

# Use (set ODEK_API_KEY, DEEPSEEK_API_KEY, or OPENAI_API_KEY)
export ODEK_API_KEY=sk-...
odek run "How many lines in go.mod?"
# → 3 lines
```

**Build requirements:** Go **1.25.12 or newer**. The `go` directive in `go.mod` pins this floor because earlier 1.25.x toolchains ship reachable standard-library CVEs; CI additionally runs `govulncheck` on every push/PR so new advisories fail the build.

**New here?** The step-by-step install guide — including the GLM 5.3 Flash (z.ai) setup and the optional bodek TUI — lives in [GETTING_STARTED.md](GETTING_STARTED.md).

---

## Why odek

odek is not a framework. It's a **runtime** — the smallest possible surface area between an LLM and your tools.

| | odek | Python agents (LangChain, CrewAI, etc.) |
|---|---|---|
| Dependencies | **5.** 2× 21no.de, 3× golang.org/x | 200+ packages |
| Binary size | ~11 MB static | 50-200 MB with venv |
| Startup | **Instant** | 2-10s (Python imports) |
| Sandbox | **Default-on** Docker sandbox (`--no-sandbox` to opt out) | Requires manual Docker setup |
| Tool interface | One interface, one method | Class hierarchies + decorators |

---

## Strategic Features

### 🔒 Sandboxed Execution
Every session can run in an isolated Docker container: no network, no host mounts beyond the working directory, zero capabilities, destroyed on exit. Sandboxing is **on by default** for `odek run`, `odek repl`, and `odek serve`; opt out with `--no-sandbox` / `ODEK_NO_SANDBOX=1` (unsandboxed runs warn loudly, and `ODEK_REQUIRE_SANDBOX=1` makes them fatal). `--ctx` files are auto-injected into the container at `/workspace/`. Full security model in [docs/SANDBOXING.md](docs/SANDBOXING.md).

### 🛡️ Prompt-Injection-Aware
External content the agent ingests (`browser`, `read_file`, `shell`, `search_files`, `multi_grep`, `transcribe`, `vision`, `web_search`, `session_search`, MCP tools) is wrapped in per-call nonce'd `<untrusted_content>` boundaries so the model can distinguish data from instructions. Redirect hops are re-classified (`browser`/`http_batch`), MCP tool descriptions are scanned for injection at registration, and the MCP error channel is wrapped too. The danger classifier resists common shell-evasion tricks (`$()`/backtick substitution, `$IFS`, brace expansion, `command`/`env` wrappers, `\rm`, basenamed absolute paths, and more). Approvers engage friction mode after 3 same-class approvals in 60 s. Memory episodes from tainted sessions are stored but never auto-replayed. Imported and project skills track provenance — untrusted ones stay excluded from trigger matching until explicit `odek skill promote --force`. `odek audit <session-id>` surfaces every ingest + per-turn divergence heuristic. Full threat model in [docs/SECURITY.md](docs/SECURITY.md).

### 🧩 Sub-Agent Delegation
Parallel OS-process sub-agents via `delegate_tasks`. True isolation — each sub-agent is a fresh `odek subagent` process with its own config, tools, and termination timeout. Up to 8 concurrent workers. Operator-defined **capability profiles** (top-level `profiles` config) override a sub-agent's permissions by name and fail closed on unknown names — a curated starter set of 21 task profiles ships in [`profiles.template.json`](profiles.template.json). See [docs/SUBAGENTS.md](docs/SUBAGENTS.md) and [docs/SECURITY.md](docs/SECURITY.md).

**Background commands**: `bg_start`/`bg_list`/`bg_status`/`bg_output`/`bg_stop` run long-lived processes (dev servers, watchers, fuzz runs) that outlive the turn that started them — session-scoped, bounded in-memory output, spawn-time approval parity with `shell`, killed when the session or the process ends. Config: `background` in [docs/CONFIG.md](docs/CONFIG.md); REPL and Telegram expose `/jobs`.

### 🧠 Skill System
Skill-matched `SKILL.md` files load on-demand — skills are authored by you or imported, never auto-generated. Import skills from any URI with automatic LLM risk assessment. [docs/CLI.md#skills](docs/CLI.md#skills)

### 💾 Persistent Memory
Three tiers: **facts** (agent-managed durable entries), **session buffer** (auto-appended turn summaries), **episodes** (LLM-extracted knowledge from past sessions). Merge-on-write via go-vector RandomProjections — cosine >0.7 auto-merges, <0.3 auto-adds. Saves ~80% LLM calls. Every lifecycle moment (fact add/merge/consolidate, episode store/dedup/evict/promote) emits an observable event surfaced in the terminal (verbose), Web UI, Telegram, or a programmatic `MemoryEventHandler`. [docs/MEMORY.md](docs/MEMORY.md)

### 🔧 Multi-Turn Sessions
Save, resume, list, trim, and clean up conversations. Sessions persist as JSON in `~/.odek/sessions/`. Continue any session with `odek continue`. [docs/SESSIONS.md](docs/SESSIONS.md)

### 🏗️ Layerable Config
Five-layer priority chain: `~/.odek/secrets.env` → `global (~/.odek/config.json)` → `project (./odek.json)` → `ODEK_*` env vars → CLI flags. `${VAR}` substitution in config files. Project configs are untrusted — operator-only fields set there are ignored. [docs/CONFIG.md](docs/CONFIG.md)

### ⏱️ Execution Budgets & Runtime Events
Hard stop runaway tasks: `--max-runtime`, `--max-tool-calls`, `--max-input-tokens`, `--max-output-tokens`, `--max-cost-usd` (or the `limits` config section, with per-model pricing via `limits.model_prices`). On exhaustion the session is persisted for resume and the CLI exits with dedicated **exit code 4**. Follow any run from an external process with `--events-jsonl` (structured `odek.event/v1` JSONL, secrets-redacted, args hashed) or the `EventHandler` Go API; `GET /api/limits` on `odek serve` exposes limits + effective prices for cost rendering. [docs/EXTENSIONS.md](docs/EXTENSIONS.md)

### 🔌 LLM-Agnostic
Multi-provider via [go-llm-sdk](https://github.com/BackendStack21/go-llm-sdk): DeepSeek, OpenAI, Anthropic, Gemini, Z.ai (GLM), Kimi, plus any OpenAI-compatible gateway. Provider id + model — no auto-thinking or auto-timeout from the model name. [docs/PROVIDERS.md](docs/PROVIDERS.md)

### 🌐 Web UI
`odek serve` — browser-based agent with `@` resource completion (`@file.go`, `@sess:abc123`), **drag-and-drop file attachments**, WebSocket streaming, and a full IDE-style console. [docs/WEBUI.md](docs/WEBUI.md)

### 🤖 Telegram Bot
Run agent tasks directly from Telegram via long-polling. Supports slash commands (`/plan`, `/sessions`, `/resume`, `/prune`, `/help`, etc.), voice message transcription, photo analysis, conversation persistence across restarts, saved plan files, and daily token budgeting. No external Telegram libraries — built on stdlib `net/http`. [docs/TELEGRAM.md](docs/TELEGRAM.md)

### ⏰ Scheduled Tasks (native cron)
Run agent tasks on a cron schedule and deliver results to Telegram, stdout, or a log — no external cron daemon. The scheduler runs **in-process** (inside `odek telegram` or a standalone `odek schedule daemon`), so a scheduled task sees the same resolved config (API key, model, bot token) an interactive run does. Stdlib-only cron parser with Vixie day-of-month/day-of-week semantics, per-job timezones, missed-run catchup, and a singleton lock so jobs never double-fire. `odek schedule add --cron "0 9 * * 1-5" --deliver telegram "..."`. [docs/SCHEDULES.md](docs/SCHEDULES.md)

### 📎 File Attachments
Attach files to any prompt with `--ctx` / `-c` (CLI), `@filename` inline references (CLI + REPL + Web UI), or drag-and-drop (Web UI). File content is injected as context blocks before the task — no tool calls needed. Comma-separate multiple files: `--ctx main.go,lib.go`. [docs/CLI.md#file-attachments](docs/CLI.md#file-attachments)

### 🔗 MCP (Two-Way)
**Server** (`odek mcp`) — expose odek's native tools (shell, read/write/search files, patch, browser) to Claude Code, Cursor, and any MCP client. **Client** (`mcp_servers` config) — odek connects to external MCP servers (Playwright, Fetch, GitHub, SQLite, etc.) and makes their tools available to the agent as `<server>__<tool>`. Per-server limits (`timeout_seconds`, `max_response_bytes`, `max_result_chars`, `artifact_roots`) and validated `file://` artifact references keep large tool outputs out of the model context — see the versioned `odek-extension/v1` contract in [docs/EXTENSIONS.md](docs/EXTENSIONS.md). Both directions in one binary. [docs/MCP.md](docs/MCP.md)

### 🔍 Native Tools
Built-in `read_file`, `write_file`, `search_files`, `patch`, `shell`, and `browser` tools. All gated by a unified security layer (`dangerous` config) — classify operations as `allow` / `deny` / `prompt` per risk class. No third-party dependencies. [docs/SECURITY.md](docs/SECURITY.md)

### 🌐 Local Web Search
`web_search` queries a **self-hosted [SearXNG](https://docs.searxng.org/) metasearch instance** — no cloud search API, no keys. Returns ranked results (title, url, snippet) the agent then fetches with `browser` / `http_batch`; results are wrapped as untrusted content and gated as `network_egress`. The Docker Compose setup runs a SearXNG sidecar and enables it out of the box; standalone installs point `web_search.base_url` at any SearXNG instance. [docs/CHEATSHEET.md](docs/CHEATSHEET.md#web-search)

---

## Quick Start

```bash
# Single-shot task
odek run "List the files"

# With session persistence
odek run --session "Refactor auth module"
odek continue "Add rate limiting"

# Sandboxed (Docker isolation)
odek run --sandbox "npm audit"

# Different model
odek run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this"

# Interactive REPL
odek repl

# Attach files for context
odek run --ctx data.csv "analyze this"
odek run --ctx main.go,lib.go "compare these files"
odek run "@README.md what does this project do?"
```

---

## Cheatsheet

### Commands

| Command | What it does |
|---------|-------------|
| `odek run <task>` | Single-shot task |
| `odek run --session <task>` | Save conversation as session |
| `odek continue [--id <id>] <task>` | Resume a saved session |
| `odek repl` | Interactive multi-turn REPL |
| `odek session list` | List recent sessions |
| `odek session show [id]` | View session transcript |
| `odek session delete <id>` | Delete a session |
| `odek session trim <id> <n>` | Keep last n messages |
| `odek session cleanup <days>` | Delete old sessions |
| `odek skill list` | List available skills |
| `odek skill view <name>` | View skill content |
| `odek skill delete <name>` | Delete a skill |
| `odek skill promote <name> [--force]` | Promote a tainted skill after review |
| `odek skill import <uri>` | Import skill from URL |
| `odek audit <session-id>` | Print the prompt-injection audit log for a session |
| `odek audit --list` | List sessions with ingest counts and divergence flags |
| `odek serve [--addr <addr>]` | Start Web UI server (loopback by default; sandbox on by default, `--no-sandbox` to disable) |
| `odek subagent --goal <string>` | Run a focused sub-task |
| `odek init [--global]` | Create config file |
| `odek mcp [--sandbox]` | Start MCP server — expose tools to Claude Code |
| `odek memory list` | List pending memory facts (aliases: `ls`, `pending`) |
| `odek memory promote <session-id>` | Promote a session's pending facts to durable facts |
| `odek memory extended <subcommand>` | Extended-memory atom management (`forget`, `promote`, `pin`, `quarantine`, `compact`, `stats`, `consolidate`, `nudges`, `pending`, `confirm`, `reject`) |
| `odek telegram` | Run the Telegram bot (also hosts the embedded scheduler) |
| `odek schedule <add\|list\|...>` | Native cron scheduler (see [docs/SCHEDULES.md](docs/SCHEDULES.md)) |
| `odek cleanup [--dry-run]` | One-shot storage sweep of `~/.odek` (see [docs/MAINTENANCE.md](docs/MAINTENANCE.md)) |
| `odek upgrade [--check]` | Self-upgrade from GitHub Releases (SHA-256 verified) |
| `odek version` | Print version |

### Key Flags

| Flag | What it does |
|------|-------------|
| `--model <name>` | LLM model (e.g. deepseek-v4-flash, gpt-4o) |
| `--base-url <url>` | API endpoint URL |
| `--sandbox` | Run in Docker sandbox |
| `--thinking <level>` | Reasoning depth (enabled/disabled/low/medium/high) |
| `--system <prompt>` | Override system prompt |
| `--max-iter <n>` | Max think→act cycles (default 90) |
| `--prompt-caching` | Enable Anthropic/OpenAI/DeepSeek prompt caching markers |
| `--no-color` | Disable colored output |
| `--ctx <files>` / `-c` | Attach files as context blocks (comma-separated) |
| `--no-agents` | Skip AGENTS.md project file |
| `--events-jsonl <path>` | Stream structured runtime events (`odek.event/v1`) to a JSONL file |
| `--external-ref <ref>` | Attach an opaque external-state reference to the session (repeatable) |
| `--max-runtime/--max-tool-calls/--max-input-tokens/--max-output-tokens/--max-cost-usd` | Hard execution budgets — exhaustion exits with code 4 |

---

## Docs

| Doc | Covers |
|-----|--------|
| [CLI Reference](docs/CLI.md) | All commands, subcommands, flags, error codes |
| [Cheat Sheet](docs/CHEATSHEET.md) | CLI quick reference, key flags, config snippets |
| [Configuration](docs/CONFIG.md) | Config files, env vars, priority chain, all sections |
| [Programmatic API](docs/API.md) | **SDK Guide**: import, Agent lifecycle, Tool interface, multi-turn sessions, memory system, complete examples |
| [Providers & Models](docs/PROVIDERS.md) | go-llm-sdk registry, `--provider`, last-resort context windows |
| [Prompt Caching](docs/CACHING.md) | Anthropic-format markers; prefix stability on OpenAI-format providers |
| [Response Streaming](docs/STREAMING.md) | Live streaming of LLM responses, config, reliability semantics |
| [Memory](docs/MEMORY.md) | Three-tier design, go-vector merge-on-write, `memory` tool |
| [Sessions](docs/SESSIONS.md) | Multi-turn conversations, save/resume/trim/cleanup |
| [Telegram Bot](docs/TELEGRAM.md) | Telegram integration: bot client, slash commands, session management, plans, media downloads |
| [Scheduled Tasks](docs/SCHEDULES.md) | Native in-process cron: `odek schedule`, Vixie cron syntax, delivery, missed-run catchup, daemon vs embedded |
| [Sandboxing](docs/SANDBOXING.md) | Docker isolation model, config, security hardening |
| [Security](docs/SECURITY.md) | Threat model, prompt injection defense, sandbox model |
| [Sub-Agents](docs/SUBAGENTS.md) | Task decomposition, delegation tool, subagent protocol, capability profiles |
| [Web UI](docs/WEBUI.md) | `odek serve`, WebSocket protocol, `@` resource resolution |
| [Skills](docs/CLI.md#skills) | Trigger-matched skills, import |
| [MCP](docs/MCP.md) | Serve tools to Claude Code + connect to external MCP servers |
| [Extensions](docs/EXTENSIONS.md) | `odek-extension/v1` contract: MCP limits, artifact refs, event stream, external refs, budgets |
| [Maintenance](docs/MAINTENANCE.md) | Storage janitor: retention, log rotation, `odek cleanup` |
| [Extended Memory](docs/EXTENDED_MEMORY.md) | Atomic long-term memory layer (opt-in) |
| [Planning](docs/PLANNING.md) | Plan tool, protected plan message, security model |
| [Tool Selection](docs/TOOL_SELECTION.md) | Tool whitelist/blacklist guide and names reference |
| [Daily Worker](docs/DAILY-WORKER.md) | Headless scheduled-worker patterns |
| [Providers](docs/PROVIDERS.md) | go-llm-sdk registry, `--provider`, v2 knobs |
| [Migration (v2)](docs/MIGRATION.md) | v1 → v2 config, deleted profiles, embedder API |
| [Development](docs/DEVELOPMENT.md) | Building, testing, contributing, project structure |

---

## Programmatic API

```go
import "github.com/BackendStack21/odek"

agent, err := odek.New(odek.Config{
    Provider:       "deepseek",
    Model:          "deepseek-v4-flash",
    APIKey:         os.Getenv("DEEPSEEK_API_KEY"),
    MaxIterations:  30,
    Tools:          []odek.Tool{&myCustomTool{}},
    SystemMessage:  "You are an expert at refactoring Go code.",
})
defer agent.Close()

result, err := agent.Run(context.Background(), "Refactor this module")
```

The full `Config` struct supports: `Provider`, `Providers`, `BaseURL` (selected-provider override), `Thinking`, `SandboxCleanup`, `Renderer`, `MemoryConfig`, `MemoryDir`, `Skills`, `SkillManager`, `NoProjectFile`, plus the extension API — `EventHandler` (structured runtime events), `ExternalRefs` (opaque session references), and `Limits` (execution budgets). v2 depends on [go-llm-sdk](https://github.com/BackendStack21/go-llm-sdk); see [docs/MIGRATION.md](docs/MIGRATION.md).

---

## Test

```bash
go test ./...                 # full suite, no setup required
go test -race ./...           # also clean under the race detector
go test -cover ./...          # per-package coverage report
ODEK_E2E=1 go test ./cmd/odek/   # opt-in Docker / subprocess E2E suite
```

Everything runs with `go test` — no Docker, no network, no external services required for the default unit suite. The opt-in `ODEK_E2E=1` set exercises the sandbox, sub-agent subprocess pipeline, and Web UI handshake against real Docker / real processes.

---

## License

MIT
