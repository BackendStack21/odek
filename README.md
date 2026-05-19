# kode

The fastest, minimal, zero-dependency Go autonomous agent runtime.

`kode` runs the ReAct (Reasoning + Acting) loop — "think, therefore act" — as a single binary. No frameworks, no SDKs, no Python venvs. Just one loop and your tools.

```bash
kode run "How many lines in go.mod?"
# → 3 lines

kode run "Fix the OOM bug in default-hooks.js"
# → [reads file, edits code, runs tests, reports result]
```

## Design

- **Zero deps** — `net/http`, `encoding/json`, `context`. That's it.
- **LLM-agnostic** — Any OpenAI-compatible endpoint (Deepseek, OpenAI, Ollama, vLLM...)
- **Tool-first** — Tools are the only extension point. No chains, no prompts.
- **Sandbox-ready** — `kode run --sandbox` → isolated Docker container, destroyed on exit
- **Single binary** — `go build` → one file. Drop it anywhere.
- **Multi-turn** — `kode run --session` and `kode continue` for persistent conversations
- **Configurable** — 4-layer config (global → project → env → CLI), `${VAR}` substitution
- **Skills system** — On-demand knowledge through trigger‑matched SKILL.md files. Auto‑learn patterns with `--learn`. Import skills from URIs with LLM risk assessment.

## Install

### go install (recommended)

```bash
go install github.com/BackendStack21/kode/cmd/kode@latest
```

### From source

```bash
git clone https://github.com/BackendStack21/kode.git
cd kode
go build -o kode ./cmd/kode
```

### Binary download

```bash
# Linux amd64
curl -fsSL https://github.com/BackendStack21/kode/releases/latest/download/kode-linux-amd64 -o kode
chmod +x kode && sudo mv kode /usr/local/bin/

# macOS arm64 (Apple Silicon)
curl -fsSL https://github.com/BackendStack21/kode/releases/latest/download/kode-darwin-arm64 -o kode
chmod +x kode && sudo mv kode /usr/local/bin/
```

## Quick Start

```bash
# Set your API key
export DEEPSEEK_API_KEY=sk-...

# Run a task
kode run "List the files in this directory"

# Save as session and continue
kode run --session "Refactor the auth module"
kode continue "Now add error handling"

# Use a different model
kode run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this code"

# Sandboxed execution
kode run --sandbox "npm test"

# Enable skill learning (auto-detects patterns, suggests skills)
kode run --learn "Set up a Go project with CI"
```

## Documentation

| Topic | Doc |
|-------|-----|
| **CLI Reference** | [docs/CLI.md](docs/CLI.md) — commands, flags, examples |
| **Configuration** | [docs/CONFIG.md](docs/CONFIG.md) — files, env vars, priority chain |
| **Models & Profiles** | [docs/PROVIDERS.md](docs/PROVIDERS.md) — providers, thinking, context window |
| **Multi-Turn Sessions** | [docs/SESSIONS.md](docs/SESSIONS.md) — save, continue, list, trim, cleanup |
| **Sandboxing** | [docs/SANDBOXING.md](docs/SANDBOXING.md) — Docker isolation, security, config |
| **Security** | [docs/SECURITY.md](docs/SECURITY.md) — prompt injection, sandbox model |
| **Web UI** | [docs/WEBUI.md](docs/WEBUI.md) — `kode serve`, WebSocket protocol, `@` resource completion |
| **Sub-Agents** | [docs/SUBAGENTS.md](docs/SUBAGENTS.md) — task decomposition, parallel OS-process sub-agents |
| **Skills** | [docs/CLI.md#skills](docs/CLI.md#skills) — learn, list, save, import, curate |
| **Development** | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) — building, testing, contributing |

## Quick reference

```bash
# Commands
kode run [flags] <task>                        # Single-shot task
kode run --learn [flags] <task>                # Run with skill learning
kode run --session [flags] <task>              # Save as session
kode continue [--id <id>] <task>               # Continue a session
kode session list                               # List sessions
kode session show [id]                          # Show session transcript
kode session delete <id>                        # Delete a session
kode session trim <id> <n>                     # Keep last n messages
kode session cleanup <days>                    # Delete old sessions
kode skill list                                 # List available skills
kode skill view <name>                          # View a skill
kode skill delete <name>                        # Delete a skill
kode skill import <uri> [--basic --yes]         # Import skill from URI
kode skill curate                               # Quality/overlap audit
kode serve [--addr :8080] [--open]              # Web UI server
kode subagent --goal <string> [flags]           # Run a focused sub-task (JSON stdout)
kode init [--global] [--force]                  # Create config file
kode version                                    # Print version

# Key flags
--model <name>         # LLM model (deepseek-v4-flash, gpt-4o...)
--base-url <url>       # API endpoint
--sandbox              # Docker sandbox mode
--thinking <level>     # enabled/disabled/low/medium/high
--learn                # Enable skill learning mode
--system <prompt>      # System prompt override
--no-agents            # Skip AGENTS.md
```

## Programmatic API

```go
agent, err := kode.New(kode.Config{
    Model:  "deepseek-chat",
    APIKey: os.Getenv("DEEPSEEK_API_KEY"),
    Tools:  []kode.Tool{&myTool{}},
})
defer agent.Close()

result, err := agent.Run(context.Background(), "Summarize this codebase")
```

## Test count

```bash
go test ./...
# 220+ tests, all pass, zero external dependencies
```

## License

MIT
