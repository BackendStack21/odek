# Go SDK Guide

Use odek as a **Go library** — build autonomous agents, custom tools, and AI-powered workflows without any frameworks or runtime overhead.

```go
import "github.com/BackendStack21/kode"
```

One binary. One loop. Zero frameworks.

---

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/BackendStack21/kode"
)

func main() {
    agent, err := odek.New(odek.Config{
        Model:  "deepseek-v4-flash",
        APIKey: os.Getenv("ODEK_API_KEY"),
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "odek: %v\n", err)
        os.Exit(1)
    }
    defer agent.Close()

    result, err := agent.Run(context.Background(), "What files are in this project?")
    if err != nil {
        fmt.Fprintf(os.Stderr, "run: %v\n", err)
        os.Exit(1)
    }
    fmt.Println(result)
}
```

Save, run:

```bash
export ODEK_API_KEY=sk-...
go mod init my-agent
go mod tidy
go run main.go
```

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   Your Code                      │
│  odek.New(Config) → Agent                        │
│  Agent.Run(ctx, task) → result                   │
│  Agent.Close()                                    │
└──────────────┬──────────────────────────────────┘
               │
┌──────────────▼──────────────────────────────────┐
│                  odek Agent                       │
│                                                   │
│  ┌─────────┐  ┌──────────┐  ┌────────────────┐  │
│  │  LLM     │  │  Tools   │  │  Memory /      │  │
│  │  Client  │◄─┤  Registry│  │  Skills /       │  │
│  │          │  │          │  │  Sandbox        │  │
│  └─────────┘  └──────────┘  └────────────────┘  │
└──────────────────────────────────────────────────┘
```

The **Agent** manages one ReAct loop: **think** (LLM decides) → **act** (tool executes) → **observe** (result fed back) → repeat until done.

You provide:
- **`Config`** — model, API key, tools, system message
- **`Tool` implementations** — one interface, one method
- **`context.Context`** — cancellation, deadlines

odek handles: LLM calls, tool dispatch, iteration limits, memory persistence, skill loading, sandbox lifecycle.

---

## Core Types

### `odek.Config`

All configuration for an agent instance. Zero values fall back to sensible defaults.

```go
type Config struct {
    // Model identifier (e.g. "deepseek-v4-flash", "gpt-4o").
    // Default: "deepseek-chat"
    Model string

    // OpenAI-compatible API endpoint.
    // Default: "https://api.deepseek.com/v1"
    BaseURL string

    // API key for the LLM provider.
    // Falls back to DEEPSEEK_API_KEY, then OPENAI_API_KEY.
    // Prefer ODEK_API_KEY for odek-specific configuration.
    APIKey string

    // Thinking depth — provider-specific semantics:
    //   DeepSeek: "enabled" | "disabled"
    //   OpenAI o-series: "low" | "medium" | "high"
    //   Empty string → model profile default
    Thinking string

    // Tools registered with the agent. The LLM can invoke these
    // during the ReAct loop. Built-in tools are not included by
    // default — if you want shell, read_file, etc., implement
    // them as your own Tool wrappers.
    Tools []Tool

    // Maximum think→act cycles (default: 90).
    // A simple query typically takes 1-3 iterations.
    // Complex multi-step tasks may need 10-20.
    MaxIterations int

    // System prompt injected at the start of every run.
    // If AGENTS.md exists in the working directory and
    // NoProjectFile is false, it's appended automatically.
    SystemMessage string

    // Skip AGENTS.md auto-loading.
    NoProjectFile bool

    // Cleanup function called by Close() — used by the CLI
    // to destroy the Docker sandbox container. When nil, Close()
    // is a no-op.
    SandboxCleanup func() error

    // Terminal renderer for colored stdout output. The CLI
    // provides one; when nil, the agent runs silently (no
    // iteration headers, no emoji prefixes).
    Renderer *render.Renderer

    // Skills configuration. When nil, skills are disabled.
    Skills *skills.SkillsConfig

    // Pre-loaded skill manager. When nil, New() auto-loads
    // from ~/.odek/skills/ and ./.odek/skills/.
    SkillManager *skills.SkillManager

    // Directory for persistent memory storage.
    // Default: ~/.odek/memory/
    MemoryDir string

    // Memory system configuration (facts, buffer, episodes).
    // Default: memory.DefaultMemoryConfig()
    MemoryConfig memory.MemoryConfig

    // PromptCaching enables prompt caching markers for supported
    // providers (Anthropic, DeepSeek, OpenAI). When enabled, the
    // system prompt and first user message are annotated for cache.
    // Default: false (no cache markers)
    PromptCaching bool

    // MaxToolParallel controls tool call concurrency per iteration.
    // When the LLM emits multiple tool calls in one response, they
    // execute concurrently — this caps the max simultaneous goroutines.
    // 0 = use default (4). I/O-bound tools benefit most.
    MaxToolParallel int

    // Approver gates dangerous tool operations. When set and the LLM
    // returns multiple tool calls in one iteration, a single batch
    // approval prompt is shown instead of N individual prompts.
    // If denied, no tools run for that iteration.
    Approver danger.Approver
}
```

> **Note:** Some types (`render.Renderer`, `skills.SkillsConfig`, `memory.MemoryConfig`) live in `internal/` packages and are not directly accessible outside the module. For most SDK use cases, you only need `Model`, `APIKey`, `Tools`, `SystemMessage`, and `MaxIterations`.

### `odek.Tool` Interface

The only extension point. Tools are plain Go structs with four methods.

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any  // JSON Schema describing parameters
    Call(args string) (string, error)
}
```

| Method | Purpose |
|--------|---------|
| `Name()` | Unique identifier — the LLM uses this to invoke the tool. Lowercase, underscore-separated (e.g. `read_file`, `delegate_tasks`). |
| `Description()` | Natural-language description of what the tool does. The LLM reads this to decide when to call it. Be specific: "Search file contents using ripgrep" vs "Search tool". |
| `Schema()` | JSON Schema object defining the tool's parameters. The LLM uses this to construct valid JSON arguments. |
| `Call(args)` | The implementation. Receives JSON-marshalled arguments, returns a string that's fed back to the LLM in the next iteration. |

### `odek.Agent`

The agent runtime returned by `New()`.

```go
type Agent struct { /* unexported */ }

func New(cfg Config) (*Agent, error)

func (a *Agent) Run(ctx context.Context, task string) (string, error)
func (a *Agent) RunWithMessages(ctx context.Context, messages []llm.Message) (string, []llm.Message, error)
func (a *Agent) TotalInputTokens() int
func (a *Agent) TotalOutputTokens() int
func (a *Agent) Close() error
func (a *Agent) Memory() *memory.MemoryManager
```

### `odek.ModelProfile` and Friends

```go
type ModelProfile struct {
    Label      string // Human-readable name (e.g. "DeepSeek v4 Pro")
    DefaultThinking string // "enabled" | "disabled" | ""
    Timeout    int    // Per-request timeout in seconds
    MaxContext int    // Context window limit in tokens
}

var KnownProfiles = []struct {
    Prefix  string
    Profile ModelProfile
}{ /* ... */ }

func LookupProfile(model string) *ModelProfile
func ProfileLabel(model string) string
func LoadProjectFile() string

const ProjectFileName = "AGENTS.md"
```

Model profiles are matched by **longest model-name prefix**. A profile for `deepseek-v4-flash` matches before a broader `deepseek-` profile. Add custom profiles by appending to `KnownProfiles`.

---

## Single-Shot Tasks

```go
result, err := agent.Run(ctx, "How many Go files are in this project?")
```

The agent loop:
1. Sends `task` (with system message + memory context) to the LLM
2. LLM responds with text or a tool call
3. If tool call: executes the tool, feeds result back, repeats
4. If text response: returns it as the final answer

### With custom tools

```go
agent, err := odek.New(odek.Config{
    Model:  "deepseek-chat",
    APIKey: os.Getenv("ODEK_API_KEY"),
    Tools:  []odek.Tool{&slackNotifier{}},
})

result, err := agent.Run(ctx, "Send a Slack message saying 'Deploy complete'")
```

### With system prompt

```go
agent, err := odek.New(odek.Config{
    Model:         "deepseek-v4-flash",
    APIKey:        os.Getenv("ODEK_API_KEY"),
    SystemMessage: "You are a Go code reviewer. Be concise and specific.",
    MaxIterations: 15,
})
```

---

## Multi-Turn Sessions

Use `RunWithMessages` to continue conversations across turns, loading prior message history:

```go
// First turn
answer, messages, err := agent.RunWithMessages(ctx, []llm.Message{
    {Role: "user", Content: "Read the main.go file"},
})

// Second turn — continue the conversation
messages = append(messages, llm.Message{Role: "user", Content: "Now refactor it"})
answer, messages, err = agent.RunWithMessages(ctx, messages)

// Third turn — continue again
messages = append(messages, llm.Message{Role: "user", Content: "Add error handling"})
answer, messages, err = agent.RunWithMessages(ctx, messages)
```

### Persisting sessions

odek's `session.Store` handles persistence. Use it to save and resume sessions:

```go
import "github.com/BackendStack21/kode/internal/session"

store, _ := session.NewStore()
sess, _ := store.Create(messages, "deepseek-chat", "Refactor auth")

// Later...
sess, _ := store.Load("20260520-abc123")
msgs := sess.GetMessages()
msgs = append(msgs, llm.Message{Role: "user", Content: "Add tests"})
answer, allMsgs, err := agent.RunWithMessages(ctx, msgs)
store.Append(sess.ID, allMsgs[len(msgs):])
```

---

## Custom Tools — Complete Walkthrough

### Step 1: Define the struct

```go
type gitLogTool struct{}
```

### Step 2: Implement Name and Description

```go
func (t *gitLogTool) Name() string { return "git_log" }

func (t *gitLogTool) Description() string {
    return "Show recent git commits. Parameters: path (optional, git directory), count (number of commits, default 10). Returns commit hashes, authors, and messages."
}
```

### Step 3: Define the JSON Schema

```go
func (t *gitLogTool) Schema() any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "path": map[string]any{
                "type":        "string",
                "description": "Git repository path (default: current dir)",
            },
            "count": map[string]any{
                "type":        "integer",
                "description": "Number of commits to show (default: 10)",
                "minimum":     1,
                "maximum":     100,
            },
        },
    }
}
```

### Step 4: Implement Call

```go
func (t *gitLogTool) Call(args string) (string, error) {
    var params struct {
        Path  string `json:"path,omitempty"`
        Count int    `json:"count,omitempty"`
    }
    if err := json.Unmarshal([]byte(args), &params); err != nil {
        return "", fmt.Errorf("git_log: parse args: %w", err)
    }
    if params.Count <= 0 {
        params.Count = 10
    }

    cmd := exec.Command("git", "log", fmt.Sprintf("-%d", params.Count),
        "--oneline", "--pretty=format:%h %an: %s")
    if params.Path != "" {
        cmd.Dir = params.Path
    }

    out, err := cmd.Output()
    if err != nil {
        return fmt.Sprintf("git log error: %v", err), nil
    }
    return string(out), nil
}
```

### Step 5: Register and use

```go
agent, _ := odek.New(odek.Config{
    Model: "deepseek-chat",
    APIKey: os.Getenv("ODEK_API_KEY"),
    Tools:  []odek.Tool{&gitLogTool{}},
})

result, _ := agent.Run(ctx, "What changed in the last 5 commits?")
```

### Tool patterns

| Pattern | Description |
|---------|-------------|
| **Zero-allocation descriptors** | `Name()` and `Description()` should be static strings or struct fields. The LLM calls these frequently during schema generation. |
| **Graceful errors** | Return errors as strings, not Go errors, when the tool ran but failed. The LLM reads the string to decide next steps. Return Go errors only for parse failures. |
| **Always return something** | If a tool produces no output, return `"(no output)"` or a descriptive message. The LLM needs a response for every tool call. |
| **JSON Schema constraints** | Use `minimum`, `maximum`, `enum`, `pattern` to constrain LLM output. The stronger the schema, the fewer hallucinated arguments. |
| **Stateless by design** | Tools should not depend on agent state. The LLM passes all context it needs via arguments. |

---

## Memory System (Agent.Memory())

Access persistent facts, buffer, and episodes through the agent's `Memory()` method:

```go
mm := agent.Memory()
if mm == nil {
    // Memory is disabled (MemoryConfig was nil or memory disabled)
    return
}
```

### Facts (persistent text entries)

```go
// Add a fact about the user
mm.AddFact("user", "User prefers snake_case for variable names")
mm.AddFact("env", "Project uses Go 1.25")

// Read all facts
userContent, envContent, err := mm.ReadFacts()

// Update a fact
mm.ReplaceFact("user", "snake_case", "User prefers camelCase for exported names")

// Remove a fact
mm.RemoveFact("user", "snake_case")

// Consolidate related entries using LLM
mm.Consolidate("user")
```

### Session Buffer (short-term conversation memory)

```go
// Append to the running buffer
mm.AppendBuffer("user", "Asked about Go version")
mm.AppendBuffer("assistant", "Responded with 1.25")

// Read buffer lines
lines := mm.BufferLines()

// Clear
mm.ClearBuffer()
```

### Episodes (LLM-extracted session summaries)

```go
// Extract episode from recent messages (typically called on session close)
mm.ExtractEpisode(sessionID, messages)
```

### Full memory lifecycle in a server application

Memory is enabled by default when odek loads a config file with memory settings. Programmatically, the memory manager activates automatically when a `MemoryDir` is set or when using CLI/web modes. Call methods on `agent.Memory()` to access facts, buffer, and episodes:

```go
agent, _ := odek.New(odek.Config{
    Model:  "deepseek-chat",
    APIKey: os.Getenv("ODEK_API_KEY"),
    // Memory is enabled via config file (~/.odek/config.json or ./odek.json)
    // In CLI mode, the --memory flag enables it automatically
})

// Each turn — memory manager is nil if disabled
if mm := agent.Memory(); mm != nil {
    mm.AppendBuffer("user", userMsg[:min(len(userMsg), 100)])
    result, _, _ := agent.RunWithMessages(ctx, messages)
    mm.AppendBuffer("assistant", result[:min(len(result), 100)])
}

---

## Model Profiles

Profiles provide per-model defaults for thinking depth, timeout, and context window.

### Built-in profiles

| Prefix | Label | Default Thinking | Timeout | Max Context |
|--------|-------|-----------------|---------|-------------|
| `deepseek-v4-pro` | DeepSeek v4 Pro | enabled | 180s | 1,000,000 |
| `deepseek-v4-flash` | DeepSeek v4 Flash | — | 90s | 131,072 |
| `deepseek-` | DeepSeek (generic) | — | 120s | 131,072 |

### Adding a profile

```go
odek.KnownProfiles = append(odek.KnownProfiles, struct {
    Prefix  string
    Profile odek.ModelProfile
}{
    Prefix: "gpt-4o",
    Profile: odek.ModelProfile{
        Label:      "GPT-4o",
        Timeout:    120,
        MaxContext: 128_000,
    },
})
```

Lookup is by longest prefix match — `deepseek-v4-pro` matches before `deepseek-`.

### Using profiles

```go
profile := odek.LookupProfile("deepseek-v4-flash")
if profile != nil {
    fmt.Println(profile.Label)  // "DeepSeek v4 Flash"
    fmt.Println(profile.Timeout) // 90
}

label := odek.ProfileLabel("gpt-4o-mini") // "gpt-4o-mini" (fallback)
```

---

## Project Files (AGENTS.md)

odek automatically loads `AGENTS.md` from the working directory and appends it to the system message:

```markdown
# Project Instructions

This project follows:
- Standard Go project layout
- `internal/` for private packages
- Test files alongside source
```

```go
// Disable auto-loading
agent, _ := odek.New(odek.Config{
    NoProjectFile: true,
    // ...
})

// Read manually
content := odek.LoadProjectFile()
```

---

## Token Tracking

```go
result, err := agent.Run(ctx, "Refactor the auth module")

fmt.Printf("Input tokens:  %d\n", agent.TotalInputTokens())
fmt.Printf("Output tokens: %d\n", agent.TotalOutputTokens())
fmt.Printf("Total tokens:  %d\n", agent.TotalInputTokens()+agent.TotalOutputTokens())
```

Token counts reset on each `Run` / `RunWithMessages` call. For session-level tracking, accumulate across turns:

```go
var sessionInput, sessionOutput int

for _, task := range tasks {
    result, err := agent.Run(ctx, task)
    sessionInput += agent.TotalInputTokens()
    sessionOutput += agent.TotalOutputTokens()
}

fmt.Printf("Session total: %d tokens\n", sessionInput+sessionOutput)
```

---

## Error Handling

### Agent creation failures

```go
agent, err := odek.New(odek.Config{
    Model:  "deepseek-chat",
    APIKey: "", // missing!
})
// err: "odek: no API key provided (set ODEK_API_KEY, DEEPSEEK_API_KEY, or OPENAI_API_KEY)"
```

### Run failures

```go
result, err := agent.Run(ctx, task)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        // Agent took too long
    }
    if errors.Is(err, context.Canceled) {
        // User cancelled
    }
    // Other errors: LLM API errors, tool failures, etc.
}
```

### Tool errors

Return errors as strings when the tool ran but had a problem:

```go
func (t *myTool) Call(args string) (string, error) {
    out, err := exec.Command("some-tool").Output()
    if err != nil {
        // Return the error as a string — LLM reads it
        return fmt.Sprintf("tool failed: %v\nstderr: %s", err, stderr), nil
    }
    return string(out), nil
}
```

Return Go errors only when the arguments can't be parsed:

```go
func (t *myTool) Call(args string) (string, error) {
    var params struct { ... }
    if err := json.Unmarshal([]byte(args), &params); err != nil {
        // Go error — agent will report it as a system error
        return "", fmt.Errorf("my_tool: parse args: %w", err)
    }
    // ...
}
```

---

## Complete Example

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "strings"

    "github.com/BackendStack21/kode"
)

// ── Custom tool: file line count ──

type lineCountTool struct{}

func (t *lineCountTool) Name() string { return "line_count" }

func (t *lineCountTool) Description() string {
    return "Count lines in a file. Returns the line count and file size."
}

func (t *lineCountTool) Schema() any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "path": map[string]any{
                "type":        "string",
                "description": "Path to the file",
            },
        },
        "required": []string{"path"},
    }
}

func (t *lineCountTool) Call(args string) (string, error) {
    var params struct {
        Path string `json:"path"`
    }
    if err := json.Unmarshal([]byte(args), &params); err != nil {
        return "", fmt.Errorf("line_count: %w", err)
    }

    // Count lines with wc (works on any OS with Unix tools)
    cmd := exec.Command("wc", "-l", params.Path)
    out, err := cmd.Output()
    if err != nil {
        return fmt.Sprintf("error: cannot read %q: %v", params.Path, err), nil
    }

    // Parse output: "  42 filename.txt"
    parts := strings.Fields(string(out))
    if len(parts) > 0 {
        return fmt.Sprintf("File %q has %s lines.", params.Path, parts[0]), nil
    }
    return "(no output)", nil
}

// ── Custom tool: Slack notifier ──

type slackNotifyTool struct {
    webhookURL string
}

func (t *slackNotifyTool) Name() string { return "slack_notify" }

func (t *slackNotifyTool) Description() string {
    return "Send a message to the team Slack channel."
}

func (t *slackNotifyTool) Schema() any {
    return map[string]any{
        "type": "object",
        "properties": map[string]any{
            "message": map[string]any{
                "type":        "string",
                "description": "The message to send",
            },
        },
        "required": []string{"message"},
    }
}

func (t *slackNotifyTool) Call(args string) (string, error) {
    var params struct {
        Message string `json:"message"`
    }
    if err := json.Unmarshal([]byte(args), &params); err != nil {
        return "", fmt.Errorf("slack_notify: %w", err)
    }
    // In production, POST to Slack webhook
    return fmt.Sprintf("Sent to Slack: %q", params.Message), nil
}

// ── Main ──

func main() {
    agent, err := odek.New(odek.Config{
        Model:         "deepseek-v4-flash",
        APIKey:        os.Getenv("ODEK_API_KEY"),
        SystemMessage: "You are a build engineer analyzing Go projects. Use line_count to examine files and slack_notify to report results.",
        MaxIterations: 10,
        Tools: []odek.Tool{
            &lineCountTool{},
            &slackNotifyTool{webhookURL: os.Getenv("SLACK_WEBHOOK")},
        },
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "odek: %v\n", err)
        os.Exit(1)
    }
    defer agent.Close()

    ctx := context.Background()
    result, err := agent.Run(ctx, "Count lines in main.go, then notify the team")
    if err != nil {
        fmt.Fprintf(os.Stderr, "run: %v\n", err)
        os.Exit(1)
    }
    fmt.Println(result)

    // Track token usage
    fmt.Printf("\n---\nTokens: %d in / %d out\n",
        agent.TotalInputTokens(), agent.TotalOutputTokens())
}
```

---

## Package Reference

All public symbols exported by `github.com/BackendStack21/kode`:

### Functions

| Signature | Description |
|-----------|-------------|
| `New(Config) (*Agent, error)` | Create a new agent with the given configuration |
| `LookupProfile(string) *ModelProfile` | Find the best-matching model profile (longest prefix) |
| `ProfileLabel(string) string` | Human-readable label for a model name |
| `LoadProjectFile() string` | Read AGENTS.md from working directory |

### Constants

| Symbol | Value | Description |
|--------|-------|-------------|
| `ProjectFileName` | `"AGENTS.md"` | Name of the project instructions file |

### Types

| Type | Description |
|------|-------------|
| `Config` | Agent configuration struct (Model, APIKey, Tools, etc.) |
| `Agent` | Agent runtime with Run, Close, Memory methods |
| `Tool` | Plugin interface: Name, Description, Schema, Call |
| `ModelProfile` | Per-model defaults: Label, DefaultThinking, Timeout, MaxContext |

### Variables

| Variable | Description |
|----------|-------------|
| `KnownProfiles` | Slice of `{Prefix, Profile}` pairs for model matching. Append custom profiles here. |

---

## Import Path

```go
import "github.com/BackendStack21/kode"
```

```
module github.com/your-project

go 1.25.0

require github.com/BackendStack21/kode v0.16.1
```

All `internal/` packages (`internal/llm`, `internal/memory`, `internal/skills`, `internal/config`, `internal/session`, `internal/danger`, `internal/resource`, `internal/render`, `internal/ws`) are not importable outside the module due to Go's `internal` package visibility rules.

