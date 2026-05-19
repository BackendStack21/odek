# kode

**The fastest, minimal, zero-dependency Go autonomous agent runtime.**

One binary. One loop. Zero frameworks. ReAct (Reasoning + Acting) — think, therefore act.

```bash
# Install
go install github.com/BackendStack21/kode/cmd/kode@latest

# Use
export DEEPSEEK_API_KEY=sk-...
kode run "How many lines in go.mod?"
# → 3 lines
```

---

## Why kode

kode is not a framework. It's a **runtime** — the smallest possible surface area between an LLM and your tools.

| | kode | Python agents (LangChain, CrewAI, etc.) |
|---|---|---|
| Dependencies | **Zero.** stdlib only | 200+ packages |
| Binary size | ~5 MB static | 50-200 MB with venv |
| Startup | **Instant** | 2-10s (Python imports) |
| Sandbox | `--sandbox` flag | Requires manual Docker setup |
| Tool interface | One interface, one method | Class hierarchies + decorators |

---

## Strategic Features

### 🔒 Sandboxed Execution
`kode run --sandbox` — every session spawns an isolated Docker container. No network, no host mounts beyond the working directory, zero capabilities, destroyed on exit. Full security model in [docs/SANDBOXING.md](docs/SANDBOXING.md).

### 🧩 Sub-Agent Delegation
Parallel OS-process sub-agents via `delegate_tasks`. True isolation — each sub-agent is a fresh `kode subagent` process with its own config, tools, and termination timeout. Up to 8 concurrent workers. [docs/SUBAGENTS.md](docs/SUBAGENTS.md)

### 🧠 Skill System (on by default)
|Trigger-matched `SKILL.md` files load on-demand. Auto-learns from patterns every session — detects multi-step procedures, error recoveries, repeated actions, and user corrections. **LLM-enhanced**: each detected pattern is enriched with an LLM-generated name, description, trigger keywords, and structured body with overview, steps, pitfalls, and verification sections. Use `--no-learn` to disable. Import skills from any URI with automatic LLM risk assessment. [docs/CLI.md#skills](docs/CLI.md#skills)

### 💾 Persistent Memory
Three tiers: **facts** (agent-managed durable entries), **session buffer** (auto-appended turn summaries), **episodes** (LLM-extracted knowledge from past sessions). Merge-on-write via go-vector RandomProjections — cosine >0.7 auto-merges, <0.3 auto-adds. Saves ~80% LLM calls. [docs/MEMORY.md](docs/MEMORY.md)

### 🔧 Multi-Turn Sessions
Save, resume, list, trim, and clean up conversations. Sessions persist as JSON in `~/.kode/sessions/`. Continue any session with `kode continue`. [docs/SESSIONS.md](docs/SESSIONS.md)

### 🏗️ Layerable Config
Four-layer priority chain: `global (~/kode/config.json)` → `project (./kode.json)` → `KODE_*` env vars → CLI flags. `${VAR}` substitution in config files. [docs/CONFIG.md](docs/CONFIG.md)

### 🔌 LLM-Agnostic
Any OpenAI-compatible endpoint: Deepseek, OpenAI, Anthropic, Ollama, vLLM, Groq, Together, Fireworks — anything that speaks `/chat/completions`. Per-model profiles for thinking depth and context windows. [docs/PROVIDERS.md](docs/PROVIDERS.md)

### 🌐 Web UI
`kode serve` — browser-based agent with `@` resource completion (`@file.go`, `@sess:abc123`), WebSocket streaming, and a full IDE-style console. [docs/WEBUI.md](docs/WEBUI.md)

### 🔍 Native Tools
Built-in `read_file`, `write_file`, `search_files`, `patch`, `shell`, and `browser` tools. All gated by a unified security layer (`dangerous` config) — classify operations as `allow` / `deny` / `prompt` per risk class. No third-party dependencies. [docs/SECURITY.md](docs/SECURITY.md)

---

## Quick Start

```bash
# Single-shot task
kode run "List the files"

# With session persistence
kode run --session "Refactor auth module"
kode continue "Add rate limiting"

# Sandboxed (Docker isolation)
kode run --sandbox "npm audit"

# Different model
kode run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this"

# With skill learning (on by default — use --no-learn to disable)
kode run "Set up a Go project with CI"

# Interactive REPL
kode repl
```

---

## Cheatsheet

### Commands

| Command | What it does |
|---------|-------------|
| `kode run <task>` | Single-shot task |
| `kode run --session <task>` | Save conversation as session |
| `kode continue [--id <id>] <task>` | Resume a saved session |
| `kode repl` | Interactive multi-turn REPL |
| `kode session list` | List recent sessions |
| `kode session show [id]` | View session transcript |
| `kode session delete <id>` | Delete a session |
| `kode session trim <id> <n>` | Keep last n messages |
| `kode session cleanup <days>` | Delete old sessions |
| `kode skill list` | List available skills |
| `kode skill view <name>` | View skill content |
| `kode skill delete <name>` | Delete a skill |
| `kode skill import <uri>` | Import skill from URL |
| `kode skill curate` | Audit skill quality/overlap |
| `kode serve [--addr :8080]` | Start Web UI server |
| `kode subagent --goal <string>` | Run a focused sub-task |
| `kode init [--global]` | Create config file |
| `kode version` | Print version |

### Key Flags

| Flag | What it does |
|------|-------------|
| `--model <name>` | LLM model (e.g. deepseek-v4-flash, gpt-4o) |
| `--base-url <url>` | API endpoint URL |
| `--sandbox` | Run in Docker sandbox |
| `--thinking <level>` | Reasoning depth (enabled/disabled/low/medium/high) |
| `--no-learn` | Disable skill learning mode (on by default) |
| `--system <prompt>` | Override system prompt |
| `--max-iter <n>` | Max think→act cycles (default 90) |
| `--no-agents` | Skip AGENTS.md project file |

---

## Docs

| Doc | Covers |
|-----|--------|
| [CLI Reference](docs/CLI.md) | All commands, subcommands, flags, error codes |
| [Configuration](docs/CONFIG.md) | Config files, env vars, priority chain, all sections |
| [Providers & Models](docs/PROVIDERS.md) | Supported providers, thinking config, context windows |
| [Memory](docs/MEMORY.md) | Three-tier design, go-vector merge-on-write, `memory` tool |
| [Sessions](docs/SESSIONS.md) | Multi-turn conversations, save/resume/trim/cleanup |
| [Sandboxing](docs/SANDBOXING.md) | Docker isolation model, config, security hardening |
| [Security](docs/SECURITY.md) | Threat model, prompt injection defense, sandbox model |
| [Sub-Agents](docs/SUBAGENTS.md) | Task decomposition, delegation tool, subagent protocol |
| [Web UI](docs/WEBUI.md) | `kode serve`, WebSocket protocol, `@` resource resolution |
| [Skills](docs/CLI.md#skills) | Trigger-matched skills, learning, import, curation |
| [Development](docs/DEVELOPMENT.md) | Building, testing, contributing, project structure |

---

## Programmatic API

```go
import "github.com/BackendStack21/kode"

agent, err := kode.New(kode.Config{
    Model:          "deepseek-chat",
    APIKey:         os.Getenv("DEEPSEEK_API_KEY"),
    MaxIterations:  30,
    Tools:          []kode.Tool{&myCustomTool{}},
    SystemMessage:  "You are an expert at refactoring Go code.",
})
defer agent.Close()

result, err := agent.Run(context.Background(), "Refactor this module")
```

The full `Config` struct supports: `BaseURL`, `Thinking`, `SandboxCleanup`, `Renderer`, `MemoryConfig`, `MemoryDir`, `Skills`, `SkillManager`, and `NoProjectFile`.

---

## Test

```bash
go test ./...                  # 220+ tests, all pass
go test -race ./...           # race detector clean
go test -cover ./...          # 80%+ coverage
```

Everything runs with `go test` — no Docker, no network, no external services required for unit tests.

---

## License

MIT
