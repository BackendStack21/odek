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

| Principle | Implementation |
|-----------|---------------|
| **Zero deps** | `net/http`, `encoding/json`, `context`. That's it. |
| **LLM-agnostic** | Any OpenAI-compatible endpoint (Deepseek, OpenAI, etc.) |
| **Tool-first** | Tools are the only extension point. No chains, no prompts. |
| **Sandbox-ready** | `kode run --sandbox` → isolated Docker container, destroyed on exit |
| **Single binary** | `go build` → one file. Drop it anywhere. |

## Install

### go install (recommended)

```bash
go install github.com/BackendStack21/kode/cmd/kode@latest
```

Zero dependencies — the binary compiles in seconds.

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
chmod +x kode
sudo mv kode /usr/local/bin/

# macOS arm64 (Apple Silicon)
curl -fsSL https://github.com/BackendStack21/kode/releases/latest/download/kode-darwin-arm64 -o kode
chmod +x kode
sudo mv kode /usr/local/bin/
```

## Quick Start

```bash
# Set your API key (Deepseek, OpenAI, or any compatible provider)
export DEEPSEEK_API_KEY=sk-...

# Run a task
kode run "List the files in this directory"

# Use a different model
kode run --model gpt-4o "Write a Go test for the loop engine"
```

---

## CLI Reference

### Commands

| Command | Description |
|---------|------------|
| `kode run [flags] <task>` | Execute a task with the agent loop |
| `kode version` | Print version and exit |

### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--model <name>` | string | `deepseek-chat` | LLM model identifier |
| `--base-url <url>` | string | `https://api.deepseek.com/v1` | OpenAI-compatible API endpoint |
| `--max-iter <n>` | int | `90` | Maximum think→act cycles before giving up |
| `--thinking <level>` | string | (none) | Reasoning depth — see [Thinking Levels](#thinking-levels) |
| `--sandbox` | bool | false | Run all shell commands inside an isolated Docker container |
| `--system <prompt>` | string | built-in | Override the system prompt |

### Examples

```bash
# Deepseek (default)
kode run "What files are in this directory?"

# OpenAI
export OPENAI_API_KEY=sk-...
kode run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this code"

# Deepseek with extended thinking
kode run --model deepseek-chat --thinking enabled "Design a database schema for a blog"

# OpenAI o1 with high reasoning effort
kode run --model o1 --base-url https://api.openai.com/v1 --thinking high "Prove the Riemann hypothesis"

# Strict, custom system prompt
kode run --system "You are a Go expert. Answer with code only." "Write a quicksort"

# Sandboxed execution
kode run --sandbox "Run the test suite"
```

---

## Providers & Models

kode is provider-agnostic. Any endpoint that speaks the OpenAI `/chat/completions` protocol works.

### Deepseek (default)

```bash
export DEEPSEEK_API_KEY=sk-...
kode run --model deepseek-chat "task"
```

API key fallback order: `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`.

### OpenAI

```bash
export OPENAI_API_KEY=sk-...
kode run \
  --model gpt-4o \
  --base-url https://api.openai.com/v1 \
  "task"
```

### Custom / self-hosted (Ollama, vLLM, LiteLLM, etc.)

```bash
export OPENAI_API_KEY=not-needed
kode run \
  --model llama3 \
  --base-url http://localhost:11434/v1 \
  "task"
```

Any endpoint that accepts `POST /chat/completions` with an OpenAI-compatible JSON body works. No provider-specific code in kode — the request format is pure OpenAI JSON.

---

## Thinking Levels

The `--thinking` flag controls how deeply the model reasons before responding. kode automatically maps your value to the provider's native format.

| Value | Deepseek sends | OpenAI o-series sends | Description |
|-------|---------------|----------------------|-------------|
| `enabled` | `{"thinking": {"type": "enabled"}}` | — | Enable extended thinking |
| `disabled` | `{"thinking": {"type": "disabled"}}` | — | Disable extended thinking |
| `low` | — | `{"reasoning_effort": "low"}` | Minimal reasoning |
| `medium` | — | `{"reasoning_effort": "medium"}` | Balanced reasoning |
| `high` | — | `{"reasoning_effort": "high"}` | Deep reasoning (more tokens, better answers) |
| (empty) | (not sent) | (not sent) | Provider default behavior |

```bash
# Deepseek — enable extended thinking
kode run --model deepseek-chat --thinking enabled "Explain monads"

# Deepseek — disable (faster, cheaper)
kode run --model deepseek-chat --thinking disabled "List files"

# OpenAI o1 — deep reasoning for hard problems
kode run --model o1 --base-url https://api.openai.com/v1 --thinking high "Optimize this distributed consensus algorithm"

# Default (no thinking field sent) — let the provider decide
kode run "What time is it?"
```

---

## Sandbox (Docker Isolation)

With `--sandbox`, every session runs inside a fresh Docker container. The agent's shell tool executes commands inside the container — never on your host.

```bash
kode run --sandbox "Run the test suite"
```

### What happens

1. A Docker container is created from `alpine:latest` with your working directory mounted **read-only** at `/workspace`
2. Every shell command the agent runs gets routed through `docker exec <container> sh -c "..."` 
3. When the agent finishes (or is interrupted), the container is **destroyed**

### Security guarantees

| Hardening | Flag | What it means |
|-----------|------|---------------|
| No capabilities | `--cap-drop ALL` | Even root in the container has zero Linux capabilities |
| No privilege escalation | `--security-opt no-new-privileges` | `setuid` binaries cannot gain extra privileges |
| No network | `--network none` | The container cannot reach the internet or your LAN |
| Read-only workspace | `-v $PWD:/workspace:ro` | The agent can read your code but cannot modify it |
| No exec from temp | `--tmpfs /tmp:noexec` | Cannot download and execute binaries |
| Ephemeral | `--rm` | Container is destroyed on exit — no state persists |

### Pre-requisites

Docker must be installed and running. The current user needs permission to run `docker` commands.

```bash
# Verify Docker is available
docker ps
```

---

## Built-in Tools

### `shell`

Runs a shell command and returns its output. This is the agent's only built-in tool — enough to read files, run tests, build code, and interact with git.

**Schema:**
```json
{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to execute"
    }
  },
  "required": ["command"]
}
```

**How the agent uses it:**

```
User:  "How many Go files are in this project?"
Agent: [thinks] → shell({ command: "find . -name '*.go' | wc -l" })
Shell: "5"
Agent: "There are 5 Go files in this project."
```

When `--sandbox` is active, all shell commands run inside the Docker container via `docker exec`. When not sandboxed, commands run on the host — use with caution.

---

## Custom Tools

kode's `Tool` interface is the only extension point. Implement four methods and drop it in.

### Interface

```go
type Tool interface {
    Name() string                          // Unique tool name (e.g., "shell")
    Description() string                   // Natural-language description for the LLM
    Schema() any                           // JSON Schema for parameters
    Call(args string) (string, error)      // Execute the tool, return result
}
```

### Example: Read-only file tool

```go
type readTool struct{}

func (t *readTool) Name() string        { return "read" }
func (t *readTool) Description() string { return "Read a file and return its contents." }

func (t *readTool) Schema() any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "path": map[string]any{"type": "string", "description": "File path to read"},
        },
        "required": []string{"path"},
    }
}

func (t *readTool) Call(args string) (string, error) {
    var input struct{ Path string `json:"path"` }
    if err := json.Unmarshal([]byte(args), &input); err != nil {
        return "", err
    }
    data, err := os.ReadFile(input.Path)
    if err != nil {
        return "", err
    }
    return string(data), nil
}
```

### Wiring it up

```go
agent, err := kode.New(kode.Config{
    Model:  "deepseek-chat",
    APIKey: os.Getenv("DEEPSEEK_API_KEY"),
    Tools: []kode.Tool{
        &readTool{},
    },
})
```

The agent now has both `shell` and `read` tools available. The LLM decides which to call based on the task.

---

## Programmatic API

Use kode as a Go library in your own projects.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/BackendStack21/kode"
)

func main() {
    agent, err := kode.New(kode.Config{
        Model:         "deepseek-chat",
        APIKey:        os.Getenv("DEEPSEEK_API_KEY"),
        MaxIterations: 50,
        Thinking:      "enabled",
        Tools:         myCustomTools(),
    })
    if err != nil {
        panic(err)
    }
    defer agent.Close()

    result, err := agent.Run(context.Background(), "Summarize this codebase")
    if err != nil {
        panic(err)
    }
    fmt.Println(result)
}
```

### Config fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Model` | string | `"deepseek-chat"` | LLM model ID |
| `BaseURL` | string | `"https://api.deepseek.com/v1"` | API endpoint |
| `APIKey` | string | `$DEEPSEEK_API_KEY` or `$OPENAI_API_KEY` | Auth token |
| `Thinking` | string | `""` | Reasoning depth — see [Thinking Levels](#thinking-levels) |
| `Tools` | `[]Tool` | `nil` | Available tools |
| `MaxIterations` | int | `90` | Max think→act cycles |
| `SystemMessage` | string | built-in | System prompt |
| `SandboxCleanup` | `func() error` | `nil` | Docker cleanup function (set by CLI) |

---

## Configuration

### Environment variables

| Variable | Purpose |
|----------|---------|
| `DEEPSEEK_API_KEY` | Primary API key (checked first) |
| `OPENAI_API_KEY` | Fallback API key |

The API key can also be set programmatically via `Config.APIKey` — explicit config always wins over environment variables.

### Defaults

| Setting | Default |
|---------|---------|
| Model | `deepseek-chat` |
| Base URL | `https://api.deepseek.com/v1` |
| Max iterations | `90` |
| Thinking | (not sent — provider default) |

---

## System Prompt

The default system prompt instructs the agent to think before acting, use the shell tool for information gathering, and be concise. Override it with `--system` or `Config.SystemMessage`.

```bash
# Expert persona
kode run --system "You are a senior Go developer. Always include error handling." "Write a HTTP server"

# Strict mode — code only
kode run --system "Answer with only the code. No explanations." "Sort a slice of ints"
```

---

## Architecture

```
kode run "task"
  │
  ├─→ llm.Call()         # THINK: send messages to LLM
  │   └─→ tool_calls?    # Model requests action
  │       ├─→ tool.Call() # ACT: execute tool
  │       └─→ loop back   # Observe result, think again
  │
  └─→ final answer        # No more tool calls = done
```

### Source layout

```
kode.go               Public API (Config, New, Run, Close)
kode_test.go          Config and API tests
internal/
  llm/
    client.go         OpenAI-compatible HTTP client
    client_test.go    JSON marshaling + response parsing tests
  loop/
    loop.go           ReAct engine (observe → think → act → repeat)
    loop_test.go      Engine tests with httptest mock server
  tool/
    registry.go       Thread-safe tool registry
    registry_test.go  Registry tests
cmd/kode/
  main.go             CLI entry point, flag parsing, sandbox orchestration
  shell.go            Built-in shell tool (local or docker exec)
```

---

## Security

### Shell execution

Without `--sandbox`, the `shell` tool runs commands directly on the host with the same permissions as the kode process. The agent can read, write, and execute anything your user can. Use `--sandbox` for untrusted tasks.

### Sandbox model

With `--sandbox`, each session is fully contained:

- **No filesystem access** beyond the working directory (mounted read-only)
- **No network** — the container cannot reach the internet or LAN
- **No capabilities** — even root inside the container has zero kernel capabilities
- **No privilege escalation** — `setuid` binaries are neutered
- **No persistence** — container destroyed on exit, no state survives
- **No executable temp files** — `/tmp` is mounted `noexec`

The agent cannot escape the container or access host resources beyond what you explicitly mount.

### API key handling

API keys are read from environment variables or explicit config. kode never logs, stores, or transmits your key beyond the HTTPS request to the LLM endpoint.

---

## Development

### Running tests

```bash
go test ./... -v -count=1
```

Requires Go 1.24+. Zero external test dependencies — tests use `httptest`, `testing`, and the standard library only.

### Test coverage

| Package | Tests | Focus |
|---------|-------|-------|
| `kode` | 11 | Config defaults, API key fallback, thinking passthrough, system message |
| `internal/llm` | 11 | JSON marshaling, thinking/reasoning_effort fields, response parsing |
| `internal/loop` | 7 | ReAct engine with httptest mock (simple answer, tool calls, max iter, cancellation) |
| `internal/tool` | 7 | Registry CRUD, Get (found/not found), duplicate detection |

### Contributing

1. Fork and clone
2. Make changes
3. Run `go test ./...`
4. Open a PR

Zero-dependency policy: contributions must not introduce external Go modules. stdlib only.

---

## License

MIT
