# Task Decomposition & Sub-Agents

kode can **spawn focused sub-agent OS processes** for parallel, isolated work on independent sub-tasks. Each sub-agent is its own operating system process — not a goroutine, not a lightweight thread — with its own heap, its own config, and its own context window.

```bash
# Spawn a sub-agent directly
kode subagent --goal "Build JWT auth middleware in internal/middleware/auth.go" \
  --context "Uses gin, user model at internal/models/user.go"

# Machine-readable JSON on stdout, human-readable progress on stderr
# → {"status":"success","summary":"...","files_changed":[...],"tokens_used":4200,"iterations":3}
```

## Why OS processes?

| Approach | Isolation | Panic Safety | Memory | Testability |
|----------|-----------|-------------|--------|-------------|
| **Goroutine (in-process)** | Shared heap | One panic kills all | Shared | Easy |
| **OS process** | Full isolation | Independent | Separate | Via CLI |
| **Docker container** | Kernel isolation | Independent | Heavy | Slow |

Sub-agents use OS processes (`os/exec`) — real isolation without Docker overhead. A panic in a sub-agent exits only that sub-agent (exit code 3). The parent agent continues unaffected.

## Architecture

```
┌─────────────────────┐
│   Parent Agent       │
│   (ReAct loop)       │
└────────┬────────────┘
         │ delegate_tasks({ tasks: [...] })
         │
         ▼
┌─────────────────────────────────┐
│   delegateTasksTool              │
│   ────────────────────           │
│   • Writes each task to temp     │
│     file (avoids CLI arg limits) │
│   • Spawns kode subagent         │
│     per task via exec.Command    │
│   • Semaphore for concurrency    │
│   • Collects JSON from stdout    │
│   • Returns formatted summary    │
└──┬──────────┬──────────┬────────┘
   │          │          │
   ▼          ▼          ▼
┌──────┐ ┌──────┐ ┌──────┐
│ SA 1 │ │ SA 2 │ │ SA 3 │  ← OS processes (parallel)
└──────┘ └──────┘ └──────┘
```

## When to decompose

Decompose a task when it has **clear, independent sub-tasks** with minimal cross-cutting concerns:

- **Good**: "Build a user CRUD API" → { "Create user model", "Create handler", "Create routes" }
- **Bad**: "Refactor the auth module to use JWT" — a single goal with tight coupling, better done in one shot
- **Bad**: "Fix bug X" or "Review PR Y" — too small, overhead of spawning a sub-agent isn't worth it

Each sub-agent gets a **fresh context** — no parent history, no conversation state. It starts from the system prompt and its goal. Provide enough context so it doesn't need to re-discover the project structure.

## Tool: `delegate_tasks`

The `delegate_tasks` tool is available in all kode modes (CLI, REPL, Web UI). The agent calls it automatically when it identifies independent sub-tasks.

### Schema

```jsonc
{
  "type": "object",
  "properties": {
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "properties": {
          "goal":    { "type": "string" },  // Required. Specific goal for this sub-agent.
          "context": { "type": "string" },  // Optional. Background: file paths, API contracts.
          "system":  { "type": "string" }   // Optional. System prompt for this sub-agent.
        },
        "required": ["goal"]
      }
    },
    "description": { "type": "string" }    // Optional. Logged for debugging.
  },
  "required": ["tasks"]
}
```

### Output format

```jsonc
{
  "status": "success",            // "success" or "error"
  "summary": "Built JWT auth middleware with HS256 signing",
  "files_changed": ["internal/middleware/auth.go"],
  "tokens_used": 4200,
  "iterations": 3
}
```

On failure:

```jsonc
{
  "status": "error",
  "error": "create agent: kode: no API key provided",
  "summary": "",
  "files_changed": [],
  "tokens_used": 0,
  "iterations": 0
}
```

### What the tool does

1. **Deserializes** the task array from the LLM's tool call
2. **Validates**: rejects empty, >8 tasks, or malformed JSON
3. **Writes** each task to a temp file (`kode-task-*.json`) — avoids CLI argument length limits (useful for 100KB+ context)
4. **Spawns** `kode subagent --task <file> --quiet` for each task
5. **Limits concurrency** via a buffered channel semaphore (default: 3, max: configurable)
6. **Collects** JSON result from each subprocess stdout
7. **Returns** a formatted summary with all sub-agent results tagged by task number

## CLI: `kode subagent`

Direct invocation for testing and debugging:

```bash
# Basic
kode subagent --goal "List files in /tmp"

# With context
kode subagent --goal "Build auth middleware" --context "Uses gin framework"

# From file (for large context)
kode subagent --task /path/to/task.json

# With timeout and iteration limits
kode subagent --goal "Refactor main.go" --timeout 60 --max-iter 10

# Silent mode (suppresses emoji progress on stderr)
kode subagent --goal "Run tests" --quiet

# With parent session ID (for cross-session context)
kode subagent --goal "Continue refactoring" --parent-session "20260519-abc123"
```

### Exit codes

| Code | Meaning | When |
|------|---------|------|
| `0` | Success | Task completed normally, `status: "success"` in JSON |
| `1` | Task error | Agent failed with a recoverable error, `status: "error"` in JSON |
| `2` | Timeout | Context deadline exceeded (controlled by `--timeout`) |
| `3` | Setup failure | Invalid flags, missing config, or internal panic |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--goal <string>` | — | **Required** unless `--task` specified. The sub-agent's goal. |
| `--context <string>` | `""` | Background context (file paths, design decisions) |
| `--task <file>` | — | JSON file with `{"goal":"...","context":"...}"`. Mutually exclusive with `--goal`. |
| `--timeout <sec>` | 120 | Max seconds the sub-agent may run before being killed |
| `--max-iter <n>` | 15 | Max think→act cycles |
| `--quiet` | false | Suppress emoji progress on stderr |
| `--parent-session <id>` | — | Session ID from the parent (for context relay) |

## Task file format

For large prompts that exceed CLI argument length limits, use the `--task` flag with a JSON file:

```json
{
  "goal": "Create a user registration endpoint in handlers/user.go",
  "context": "Uses gin. DB connection at internal/db/db.go. User struct in models/user.go: {ID, Email, Password, CreatedAt}. Password must be bcrypt-hashed. Returns 201 with user JSON on success."
}
```

The `delegate_tasks` tool always uses this file-based approach internally.

## Output protocol

### stdout (machine-readable)

Pure JSON. Always parseable — even on errors. The parent `delegate_tasks` tool reads this via `json.NewDecoder`:

```jsonc
// Success
{"status":"success","summary":"Created handlers/user.go with POST /users","files_changed":["handlers/user.go"],"tokens_used":3200,"iterations":5}

// Error
{"status":"error","error":"no API key provided","summary":"","tokens_used":0,"iterations":0}
```

### stderr (human-readable)

Emoji-prefixed progress for terminal users:

```
🔧 Sub-agent: Build JWT auth middleware
🧠 Need to understand the project structure...
🔧 shell: ls internal/
✅ Sub-agent complete: 4.2s, 3200 tokens, 5 iterations
```

Suppressed with `--quiet`.

## Dynamic system prompts

Every sub-agent receives a system prompt **tailored to its task** — not a one-size-fits-all default.

### Three-tier resolution

| Priority | Source | When |
|----------|--------|------|
| 1 (highest) | `system` field in `delegate_tasks` | Parent explicitly provides a custom prompt |
| 2 | `KODE_SYSTEM` / config file `system` | User-configured global override |
| 3 | `classifyGoal()` auto-detection | Fallback — analyses the goal text |

### Parent-crafted prompts

The parent agent (kode) is instructed to write system prompts for each sub-task. This is the recommended path — the parent understands the task domain and can set the right tone:

```jsonc
{
  "tasks": [
    {
      "goal": "Create user model with GORM",
      "system": "You are an expert Go engineer building production code. Consider edge cases, error handling, and maintainability from the start."
    },
    {
      "goal": "Review middleware/auth.go for security issues",
      "system": "You are a security engineer reviewing auth code. Look for: token validation gaps, timing attacks, secret exposure."
    }
  ]
}
```

### Auto-classified prompts

When no `system` field is provided, `classifyGoal()` analyzes the goal text and picks a matching category:

| Category | Trigger keywords | Prompt persona |
|----------|-----------------|----------------|
| **build** (default) | *(no match)* | Expert engineer building production code |
| **debug** | fix, bug, error, crash, broken, incorrect | Expert debugger — find root cause first |
| **test** | test, spec, coverage, assert, unit test | Testing & quality expert |
| **review** | review, audit, check, inspect, verify | Senior engineer reading every line critically |
| **refactor** | refactor, clean up, simplify, rename, extract | Code architecture expert — preserve behavior |
| **config** | setup, config, install, docker, ci, deploy | DevOps engineer — reproducible, minimal permissions |
| **research** | research, explain, compare, understand, find | Technical researcher — explore thoroughly |

Each category prompt is a focused ~80-100 tokens with a distinct persona and methodology.

### Default fallback

The original `subagentSystem` constant (~120 tokens) is retained as the ultimate fallback:

```
You are kode working on a single focused sub-task.
Complete the assigned goal and report what you did.
Do not expand scope. Do not ask questions.
Use the shell tool when you need information or to make changes.
Report: what you built, what files changed, any issues encountered.
Be concise. Output your answer, then stop.
```

### Task file format

The temp file written by `delegate_tasks` carries the system prompt:

```json
{
  "goal": "Create a user registration endpoint in handlers/user.go",
  "context": "Uses gin. DB connection at internal/db/db.go.",
  "system": "You are an expert Go engineer building production code. Consider edge cases..."
}
```

When invoked directly via `kode subagent --goal "..."`, the `--goal` path uses `classifyGoal()` (no manual override) while `--task <file>` reads the `system` field from the JSON file.

## Configuration

Config in `kode.json`:

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
| `timeout_seconds` | 120 | Default timeout per sub-agent |
| `max_iterations` | 15 | Default max think→act cycles per sub-agent |

## Security model

| Risk | Mitigation |
|------|------------|
| **Sub-agent hijacking** | Sub-agents are never prompted by the parent/user — they receive structured `goal`/`context` strings. No instruction injection path. |
| **Runaway processes** | Hard timeout (`--timeout`, default 120s). Context cancellation kills via `os.Process.Kill()`. |
| **Resource exhaustion** | Concurrency semaphore (max `max_concurrency`). Sequential spawning. No fork bomb. |
| **Panic propagation** | Each sub-agent is an OS process. Panic exits only that process with code 3 — parent sees the JSON error and continues. |
| **Temp file leakage** | Each task file is `defer os.Remove()`'d after subprocess exit. |

## Testing

The sub-agent system has three test layers:

| Layer | Tests | Runner | What's verified |
|-------|-------|--------|-----------------|
| **Contract tests** | 48 | `go test ./cmd/kode/` | Flag parsing, JSON stdout protocol, exit codes, tool schema, config parsing, classifyGoal categories, system prompt length/empty checks |
| **E2E tests** | 16 | `KODE_E2E=true go test ./cmd/kode/ -run "TestE2E_"` | Real subprocess spawning, tool → binary pipeline, stderr protocol, concurrency, timeouts, custom system prompt threading |
| **Full suite** | All | `go test -race ./...` | 12 packages, race-detector clean |

E2E tests:
- Build the `kode` binary once via `TestMain`
- Test the full pipeline: `tool.Call()` → `exec.Command("kode", "subagent", ...)` → JSON stdout → parse
- Require no LLM provider (sub-agent fails on setup, producing JSON error — which is the exact contract verified)
- Validate: binary exists, stderr emoji protocol, quiet mode, 100KB+ task files via temp files, missing binary graceful degradation

## Example: End-to-end flow

```
User: "Create a REST API for a todo app in Go with JWT auth"

Parent agent thinks:
- This has 3 independent parts: model, auth, handlers
- Each can be built in parallel
- Calls delegate_tasks

Tool call:
delegate_tasks({ tasks: [
  { goal: "Create Todo model with CRUD in models/todo.go",
    context: "Uses GORM, fields: {ID, Title, Done, CreatedAt}" },
  { goal: "Create JWT auth middleware in middleware/auth.go",
    context: "Uses gin, HS256 signing, secret from env JWT_SECRET" },
  { goal: "Create todo handlers in handlers/todo.go",
    context: "Uses gin, depends on models/todo.go. Routes: GET/POST /todos, PUT/DELETE /todos/:id" }
]})

3 sub-agents run in parallel:

  SA1: kode subagent --task /tmp/kode-task-001.json --quiet
  SA2: kode subagent --task /tmp/kode-task-002.json --quiet
  SA3: kode subagent --task /tmp/kode-task-003.json --quiet

All complete in ~5s (2 batches of 2, max_concurrency=3):

  SA1: {"status":"success","files_changed":["models/todo.go"],"tokens_used":4200}
  SA2: {"status":"success","files_changed":["middleware/auth.go"],"tokens_used":3800}
  SA3: {"status":"success","files_changed":["handlers/todo.go"],"tokens_used":5100}

Parent synthesizes: "Created 3 files:
  - models/todo.go — Todo model with CRUD
  - middleware/auth.go — JWT auth middleware with HS256
  - handlers/todo.go — REST handlers
  Total: 8 files changed, 13100 tokens, 5s parallel"
```

## Tips

- **Keep goals small** — one file, one concern per sub-agent. If a goal spans 3 files, it's probably not a good decomposition boundary.
- **Provide file paths** in context — saves the sub-agent from crawling the project tree.
- **Check the trade-off** — spawning a sub-agent takes ~500ms. Don't delegate tasks that complete in 2 tool calls.
- **Observation**: sub-agents work best for **greenfield** work (creating new files). Refactoring existing code often has too many implicit dependencies.
