# Multi-Turn Sessions

odek supports **multi-turn conversations** — save a session, continue it later, and maintain full conversation history across multiple agent runs.

## Session basics

Each session is stored as a JSON file in `~/.kode/sessions/<id>.json` with the full conversation transcript including system message, user turns, assistant responses, tool calls, and tool results.

### Creating a session

```bash
# Save the conversation as a session
odek run --session "Refactor the auth module to use JWT"
```

### Continuing a session

```bash
# Continue the most recent session
odek continue "Now add refresh token support"

# Continue a specific session by ID
odek continue --id 20260518-abc123 "Add unit tests"
```

### Session listing

```bash
# List recent sessions (max 20)
odek session list

# Example output:
# ID                     Turns Model                          Task
# ────────────────────────────────────────────────────────────────
# 20260518-abc123        3    deepseek-v4-flash               Refactor the auth module...
# 20260517-def456        1    gpt-4o                          Fix the OOM bug in defau...
```

### Viewing a session

```bash
# Show the most recent session transcript
odek session show

# Show a specific session
odek session show 20260518-abc123
```

### Deleting a session

```bash
# Delete a single session
odek session delete 20260518-abc123
```

### Trimming a session

Keeps only the `n` most recent messages, always preserving the system prompt:

```bash
# Keep last 10 messages (system + 9)
odek session trim 20260518-abc123 10
# → Trimmed session 20260518-abc123: 50 → 10 messages (40 dropped)
```

### Cleaning up old sessions

Deletes all sessions whose `UpdatedAt` timestamp is older than N days:

```bash
# Remove sessions untouched in 30+ days
odek session cleanup 30
# → Cleaned up 3 session(s) older than 30 days.

# Wipe every session
odek session cleanup 0
# → Cleaned up 12 session(s) older than 0 days.
```

## Programmatic API

```go
agent, err := kode.New(kode.Config{...})

// Multi-turn with explicit message history
messages := []llm.Message{
    {Role: "system", Content: systemPrompt},
    {Role: "user", Content: task},
}
result, allMessages, err := agent.RunWithMessages(ctx, messages)

// Save to store for later continuation
store, _ := session.NewStore()
sess, _ := store.Create(allMessages, model, task)
fmt.Printf("Session %s saved\n", sess.ID)
```

## Storage format

Sessions are stored as JSON at `~/.kode/sessions/<id>.json`:

```json
{
  "id": "20260518-abc123",
  "created_at": "2026-05-18T07:17:00Z",
  "updated_at": "2026-05-18T07:22:00Z",
  "model": "deepseek-v4-flash",
  "turns": 3,
  "task": "Refactor the auth module to use JWT",
  "messages": [
    {"role": "system", "content": "..."},
    {"role": "user", "content": "Refactor the auth module..."}
  ]
}
```

The `Session` struct has all public fields, enabling direct manipulation. This makes advanced operations (editing, truncating, merging) trivial — load, mutate, save.

## Sandbox persistence

When a session is created with `--sandbox`, the `sandbox` flag is stored in the session file. On resume (`odek continue` or `odek repl --id <id>`), the sandbox is automatically re-enabled even if the current config has it disabled:

```bash
odek run --session --sandbox "Install deps and build"
# → session saved with sandbox=true

# Later, in a different terminal without sandbox config:
odek continue "Run the test suite"
# → kode: session was sandboxed — enabling sandbox for this continuation
```

This prevents accidentally escaping the sandbox on resume. The sandbox image/network/memory still come from the **current** config — only the toggle bit is persisted. To force-disable sandbox on resume, pass `odek continue` in a project with `"sandbox": false` in `./odek.json` and the session flag will be overridden by the explicit config.

### REPL sandbox flags

`odek repl` accepts the same sandbox CLI flags as `odek run`. You can start a sandboxed REPL session directly from the command line:

```bash
# Start a sandboxed REPL session
odek repl --sandbox

# With custom image and network isolation
odek repl --sandbox --sandbox-image node:20-alpine --sandbox-network none

# Resume a sandboxed session (sandbox auto-enabled)
odek repl --id 20260518-abc123
```

Sandbox state is saved with the session — resuming via `--id` auto-enables the sandbox container on startup.

