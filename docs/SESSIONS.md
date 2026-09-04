# Multi-Turn Sessions

odek supports **multi-turn conversations** — save a session, continue it later, and maintain full conversation history across multiple agent runs.

## Session basics

Each session is stored as a JSON file in `~/.odek/sessions/<id>.json` with the full conversation transcript including system message, user turns, assistant responses, tool calls, and tool results.

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

**Vector index maintenance:** `odek session cleanup` also removes stale entries from the vector index — orphaned vectors no longer accumulate in `vectors.gob`. This was a bug fix in v0.58.2: the primary cleanup path previously bypassed `Vec.Remove()`, leaving orphaned vectors that silently accumulated.

## Session Search Tool

The `session_search` tool (available inside the agent loop, not as a CLI command) lets the agent browse, search, and recall past sessions by semantic content. This is the primary mechanism for revisiting historical conversations.

| Action | Description |
|--------|-------------|
| `list` | Recent sessions (metadata only: ID, task, turns, timestamps, model) |
| `search` | Semantic keyword search through full message content using vector similarity |
| `get` | Full session by ID — returns ALL messages including tool calls and results |
| `find` | Find sessions by task/title |

**How search works — two-tier pipeline:**

1. **Vector index** (fast path) — sessions are embedded into a persisted vector store and ranked by cosine similarity in ~1ms with zero LLM calls. The embedder is the shared backend (`internal/embedding`): by default local go-vector **RandomProjections** (256-dim, lexical), or — when an `embedding` block is configured (the shared top-level default, or a `sessions.embedding` override) — any **OpenAI-compatible HTTP embeddings API**, giving *semantic* matches that the lexical default can't (a query and a session that share no words can still match by meaning). See [CONFIG.md → Shared embedding backend](./CONFIG.md#shared-embedding-backend-embedding--memory-sessions--skills).
2. **DeepSearch** (fallback) — when vector results are insufficient (<2 distinct token matches), or while the embedding backend is unavailable, falls back to exhaustive text search through session files on disk. Requires 2+ distinct token matches to qualify.

**Resilience & backend switching:** the index records which embedding space it was built in (`vectors_meta.json`). Changing `provider`/`model`/`dims` forces a one-time rebuild from the session files; a down HTTP backend degrades `search` to the DeepSearch tier and backs off for 30s — it never fails a session save or blocks the loop.

**IMPORTANT:** After `search` returns matching session IDs, use `get` (not `search`) to read the actual conversation content. `get` returns the full `session_messages` array with every user and assistant message.

From inside the Telegram bot, session recall is seamless: the current user message is persisted to the session store *before* the agent loop runs, so `session_search` can find the current conversation's data during the same turn.

## Programmatic API

```go
agent, err := odek.New(odek.Config{...})

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

Sessions are stored as compact JSON at `~/.odek/sessions/<id>.json` (no indentation — ~5% smaller on disk, faster serialization):

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
  ],
  "external_refs": [
    {"kind": "ci-run", "uri": "https://ci.example.test/runs/4821", "created_by": "ci-orchestrator", "read_only": true, "created_at": "2026-05-18T07:17:00Z"}
  ]
}
```

The `Session` struct has all public fields, enabling direct manipulation. This makes advanced operations (editing, truncating, merging) trivial — load, mutate, save.

## External state references

Sessions may carry operator-supplied pointers to state that lives **outside** odek — a CI run, a dashboard, an object-store key (schema `odek-extension/v1`, see [EXTENSIONS.md](EXTENSIONS.md)). The **opacity guarantee**: odek stores and transports these refs verbatim but **never resolves or dereferences** their URIs — there is no fetch/resolve code path, so a ref can never become an exfiltration or SSRF vector.

```bash
# Attach at session creation (repeatable)
odek run --session \
  --external-ref kind=ci-run,uri=https://ci.example.test/runs/4821,created_by=ci-orchestrator,read_only=true \
  "Summarize the failing tests"

# Shorthand kind=uri (created_by defaults to "cli")
odek run --session --external-ref ci-run=https://ci.example.test/runs/4821 "…"

# Attach to an existing session on continue (adds, never removes)
odek continue --external-ref dashboard=https://grafana.example.test/d/abc "…"
```

Rules:

- `kind`: 1–64 chars, `[a-z0-9_-]`; `uri`: 1–2048 chars, no control characters; `created_by`: 1–128 chars
- Refs are deduplicated on `(kind, uri, created_by)`; `created_at` is stamped when absent
- Malformed `--external-ref` values are **fatal** with a message naming the flag — the operator explicitly asked for the ref, so silently dropping it would violate least surprise
- On `odek run`, refs given without `--session` print a warning and are not persisted
- Refs survive `Save`/`Append`, the write-path size-cap trim, the redaction boundary, and `odek continue`; session files written before the field existed load unchanged

Programmatically: `Config.ExternalRefs []session.ExternalRef` (validated by `odek.New`) or `sess.AddExternalRefs(...)` on a loaded session. Tests: `internal/session/external_ref_test.go`, `cmd/odek/external_ref_test.go`.

## Sandbox persistence

When a session is created with `--sandbox`, the `sandbox` flag is stored in the session file. On resume (`odek continue` or `odek repl --id <id>`), the sandbox is automatically re-enabled even if the current config has it disabled:

```bash
odek run --session --sandbox "Install deps and build"
# → session saved with sandbox=true

# Later, in a different terminal without sandbox config:
odek continue "Run the test suite"
# → odek: session was sandboxed — enabling sandbox for this continuation
```

This prevents accidentally escaping the sandbox on resume. The sandbox image/network/memory still come from the **current** config — only the toggle bit is persisted. To force-disable sandbox on resume, pass `odek continue` in a project with `"sandbox": false` in `./odek.json` and the session flag will be overridden by the explicit config.

## Provider persistence

New sessions also store `provider` (the go-llm-sdk id used for the run). `odek continue` restores **provider + model** so a `--provider anthropic` session does not resume against the operator's current default provider.

Pre-v2 session files have an empty `provider`. Resume then uses the current default provider with the stored model id — rewrite those sessions or pass `--provider` on a new run if the pair would mismatch. REPL and Web UI stamp `provider` on newly created sessions the same way.

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

