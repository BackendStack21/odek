# odek

**Minimal Go autonomous agent runtime — tiny dep tree, small static binary, instant startup.**

One binary. One loop. Zero frameworks. ReAct (Reasoning + Acting) — think, therefore act.

```bash
# Install
go install github.com/BackendStack21/odek/cmd/odek@latest

# Use (set ODEK_API_KEY, DEEPSEEK_API_KEY, or OPENAI_API_KEY)
export ODEK_API_KEY=sk-...
odek run "How many lines in go.mod?"
# → 3 lines
```

---

## Why odek

odek is not a framework. It's a **runtime** — the smallest possible surface area between an LLM and your tools.

| | odek | Python agents (LangChain, CrewAI, etc.) |
|---|---|---|
| Dependencies | **5.** 3× stdlib, 2× 21no.de | 200+ packages |
| Binary size | ~11 MB static | 50-200 MB with venv |
| Startup | **Instant** | 2-10s (Python imports) |
| Sandbox | `--sandbox` flag | Requires manual Docker setup |
| Tool interface | One interface, one method | Class hierarchies + decorators |

---

## Strategic Features

### 🔒 Sandboxed Execution
Every session can run in an isolated Docker container: no network, no host mounts beyond the working directory, zero capabilities, destroyed on exit. `odek serve` enables the sandbox **by default**; `odek run` keeps it opt-in but warns when running unsandboxed. `--ctx` files are auto-injected into the container at `/workspace/`. Full security model in [docs/SANDBOXING.md](docs/SANDBOXING.md).

### 🛡️ Prompt-Injection-Aware
External content the agent ingests (`browser`, `read_file`, `shell`, `search_files`, `multi_grep`, `transcribe`, `vision`, `session_search`, MCP tools) is wrapped in per-call nonce'd `<untrusted_content>` boundaries so the model can distinguish data from instructions. Redirect hops are re-classified (`browser`/`http_batch`), MCP tool descriptions are scanned for injection at registration, and the MCP error channel is wrapped too. The danger classifier resists 8 known shell-evasion tricks (`$()`, backticks, `$IFS`, `command`/`exec`, `\rm`, basenamed absolute paths). Approvers engage friction mode after 3 same-class approvals in 60 s. Memory episodes from tainted sessions are stored but never auto-replayed. Skill auto-save tracks provenance and pins untrusted suggestions for explicit `odek skill promote`. `odek audit <session-id>` surfaces every ingest + per-turn divergence heuristic. Full threat model in [docs/SECURITY.md](docs/SECURITY.md).

### 🧩 Sub-Agent Delegation
Parallel OS-process sub-agents via `delegate_tasks`. True isolation — each sub-agent is a fresh `odek subagent` process with its own config, tools, and termination timeout. Up to 8 concurrent workers. [docs/SUBAGENTS.md](docs/SUBAGENTS.md)

### 🧠 Skill System (on by default)
Skill-matched `SKILL.md` files load on-demand. Auto-learns from patterns every session — detects multi-step procedures, error recoveries, repeated actions, and user corrections. **LLM-enhanced**: each detected pattern is enriched with an LLM-generated name, description, trigger keywords, and structured body with overview, steps, pitfalls, and verification sections. Use `--no-learn` to disable. Import skills from any URI with automatic LLM risk assessment. [docs/CLI.md#skills](docs/CLI.md#skills)

### 💾 Persistent Memory
Three tiers: **facts** (agent-managed durable entries), **session buffer** (auto-appended turn summaries), **episodes** (LLM-extracted knowledge from past sessions). Merge-on-write via go-vector RandomProjections — cosine >0.7 auto-merges, <0.3 auto-adds. Saves ~80% LLM calls. Every lifecycle moment (fact add/merge/consolidate, episode store/dedup/evict/promote) emits an observable event surfaced in the terminal (verbose), Web UI, Telegram, or a programmatic `MemoryEventHandler`. [docs/MEMORY.md](docs/MEMORY.md)

### 🔧 Multi-Turn Sessions
Save, resume, list, trim, and clean up conversations. Sessions persist as JSON in `~/.odek/sessions/`. Continue any session with `odek continue`. [docs/SESSIONS.md](docs/SESSIONS.md)

### 🏗️ Layerable Config
Four-layer priority chain: `global (~/.odek/config.json)` → `project (./odek.json)` → `ODEK_*` env vars → CLI flags. `${VAR}` substitution in config files. [docs/CONFIG.md](docs/CONFIG.md)

### 🔌 LLM-Agnostic
Any OpenAI-compatible endpoint: Deepseek, OpenAI, Anthropic, Ollama, vLLM, Groq, Together, Fireworks — anything that speaks `/chat/completions`. Per-model profiles for thinking depth and context windows. [docs/PROVIDERS.md](docs/PROVIDERS.md)

### 🌐 Web UI
`odek serve` — browser-based agent with `@` resource completion (`@file.go`, `@sess:abc123`), **drag-and-drop file attachments**, WebSocket streaming, and a full IDE-style console. [docs/WEBUI.md](docs/WEBUI.md)

### 🤖 Telegram Bot
Run agent tasks directly from Telegram via long-polling. Supports slash commands (`/plan`, `/sessions`, `/resume`, `/prune`, `/help`, etc.), voice message transcription, photo analysis, conversation persistence across restarts, saved plan files, and daily token budgeting. No external Telegram libraries — built on stdlib `net/http`. [docs/TELEGRAM.md](docs/TELEGRAM.md)

### ⏰ Scheduled Tasks (native cron)
Run agent tasks on a cron schedule and deliver results to Telegram, stdout, or a log — no external cron daemon. The scheduler runs **in-process** (inside `odek telegram` or a standalone `odek schedule daemon`), so a scheduled task sees the same resolved config (API key, model, bot token) an interactive run does. Stdlib-only cron parser with Vixie day-of-month/day-of-week semantics, per-job timezones, missed-run catchup, and a singleton lock so jobs never double-fire. `odek schedule add --cron "0 9 * * 1-5" --deliver telegram "..."`. [docs/SCHEDULES.md](docs/SCHEDULES.md)

### 📎 File Attachments
Attach files to any prompt with `--ctx` / `-c` (CLI), `@filename` inline references (CLI + REPL + Web UI), or drag-and-drop (Web UI). File content is injected as context blocks before the task — no tool calls needed. Comma-separate multiple files: `--ctx main.go,lib.go`. [docs/CLI.md#file-attachments](docs/CLI.md#file-attachments)

### 🔗 MCP (Two-Way)
**Server** (`odek mcp`) — expose odek's native tools (shell, read/write/search files, patch, browser) to Claude Code, Cursor, and any MCP client. **Client** (`mcp_servers` config) — odek connects to external MCP servers (Playwright, Fetch, GitHub, SQLite, etc.) and makes their tools available to the agent as `<server>__<tool>`. Both directions in one binary. [docs/MCP.md](docs/MCP.md)

### 🔍 Native Tools
Built-in `read_file`, `write_file`, `search_files`, `patch`, `shell`, and `browser` tools. All gated by a unified security layer (`dangerous` config) — classify operations as `allow` / `deny` / `prompt` per risk class. No third-party dependencies. [docs/SECURITY.md](docs/SECURITY.md)

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

# With skill learning (on by default — use --no-learn to disable)
odek run "Set up a Go project with CI"

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
| `odek skill promote <name>` | Promote a tainted auto-saved skill after review |
| `odek skill import <uri>` | Import skill from URL |
| `odek skill curate` | Audit skill quality/overlap |
| `odek audit <session-id>` | Print the prompt-injection audit log for a session |
| `odek audit --list` | List sessions with ingest counts and divergence flags |
| `odek serve [--addr :8080]` | Start Web UI server (sandbox on by default; `--no-sandbox` to disable) |
| `odek subagent --goal <string>` | Run a focused sub-task |
| `odek init [--global]` | Create config file |
| `odek mcp [--sandbox]` | Start MCP server — expose tools to Claude Code |
| `odek version` | Print version |

### Key Flags

| Flag | What it does |
|------|-------------|
| `--model <name>` | LLM model (e.g. deepseek-v4-flash, gpt-4o) |
| `--base-url <url>` | API endpoint URL |
| `--sandbox` | Run in Docker sandbox |
| `--thinking <level>` | Reasoning depth (enabled/disabled/low/medium/high) |
| `--learn` | Enable skill learning mode — on by default |
| `--no-learn` | Disable skill learning mode |
| `--system <prompt>` | Override system prompt |
| `--max-iter <n>` | Max think→act cycles (default 90) |
| `--prompt-caching` | Enable Anthropic/OpenAI/DeepSeek prompt caching markers |
| `--no-color` | Disable colored output |
| `--ctx <files>` / `-c` | Attach files as context blocks (comma-separated) |
| `--no-agents` | Skip AGENTS.md project file |

---

## Docs

| Doc | Covers |
|-----|--------|
| [CLI Reference](docs/CLI.md) | All commands, subcommands, flags, error codes |
| [Cheat Sheet](docs/CHEATSHEET.md) | CLI quick reference, key flags, config snippets |
| [Configuration](docs/CONFIG.md) | Config files, env vars, priority chain, all sections |
| [Programmatic API](docs/API.md) | **SDK Guide**: import, Agent lifecycle, Tool interface, multi-turn sessions, memory system, model profiles, complete examples |
| [Providers & Models](docs/PROVIDERS.md) | Supported providers, thinking config, context windows |
| [Prompt Caching](docs/CACHING.md) | Anthropic/OpenAI/DeepSeek caching support, config, metrics |
| [Memory](docs/MEMORY.md) | Three-tier design, go-vector merge-on-write, `memory` tool |
| [Sessions](docs/SESSIONS.md) | Multi-turn conversations, save/resume/trim/cleanup |
| [Telegram Bot](docs/TELEGRAM.md) | Telegram integration: bot client, slash commands, session management, plans, media downloads |
| [Scheduled Tasks](docs/SCHEDULES.md) | Native in-process cron: `odek schedule`, Vixie cron syntax, delivery, missed-run catchup, daemon vs embedded |
| [Sandboxing](docs/SANDBOXING.md) | Docker isolation model, config, security hardening |
| [Security](docs/SECURITY.md) | Threat model, prompt injection defense, sandbox model |
| [Sub-Agents](docs/SUBAGENTS.md) | Task decomposition, delegation tool, subagent protocol |
| [Web UI](docs/WEBUI.md) | `odek serve`, WebSocket protocol, `@` resource resolution |
| [Self-Learning](docs/LEARNING.md) | LLM-enhanced skill learning, pattern detection, auto-curation |
| [Skills](docs/CLI.md#skills) | Trigger-matched skills, learning, import, curation |
| [MCP](docs/MCP.md) | Serve tools to Claude Code + connect to external MCP servers |
| [Development](docs/DEVELOPMENT.md) | Building, testing, contributing, project structure |

---

## Programmatic API

```go
import "github.com/BackendStack21/odek"

agent, err := odek.New(odek.Config{
    Model:          "deepseek-v4-flash",
    APIKey:         os.Getenv("ODEK_API_KEY"),
    MaxIterations:  30,
    Tools:          []odek.Tool{&myCustomTool{}},
    SystemMessage:  "You are an expert at refactoring Go code.",
})
defer agent.Close()

result, err := agent.Run(context.Background(), "Refactor this module")
```

The full `Config` struct supports: `BaseURL`, `Thinking`, `SandboxCleanup`, `Renderer`, `MemoryConfig`, `MemoryDir`, `Skills`, `SkillManager`, and `NoProjectFile`.

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
