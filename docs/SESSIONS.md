# Multi-Turn Sessions

kode supports **multi-turn conversations** — save a session, continue it later, and maintain full conversation history across multiple agent runs.

## Session basics

Each session is stored as a JSON file in `~/.kode/sessions/<id>.json` with the full conversation transcript including system message, user turns, assistant responses, tool calls, and tool results.

### Creating a session

```bash
# Save the conversation as a session
kode run --session "Refactor the auth module to use JWT"
```

### Continuing a session

```bash
# Continue the most recent session
kode continue "Now add refresh token support"

# Continue a specific session by ID
kode continue --id 20260518-abc123 "Add unit tests"
```

### Session listing

```bash
# List recent sessions (max 20)
kode session list

# Example output:
# ID                     Turns Model                          Task
# ────────────────────────────────────────────────────────────────
# 20260518-abc123        3    deepseek-v4-flash               Refactor the auth module...
# 20260517-def456        1    gpt-4o                          Fix the OOM bug in defau...
```

### Viewing a session

```bash
# Show the most recent session transcript
kode session show

# Show a specific session
kode session show 20260518-abc123
```

### Deleting a session

```bash
# Delete a single session
kode session delete 20260518-abc123
```

### Trimming a session

Keeps only the `n` most recent messages, always preserving the system prompt:

```bash
# Keep last 10 messages (system + 9)
kode session trim 20260518-abc123 10
# → Trimmed session 20260518-abc123: 50 → 10 messages (40 dropped)
```

### Cleaning up old sessions

Deletes all sessions whose `UpdatedAt` timestamp is older than N days:

```bash
# Remove sessions untouched in 30+ days
kode session cleanup 30
# → Cleaned up 3 session(s) older than 30 days.

# Wipe every session
kode session cleanup 0
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
