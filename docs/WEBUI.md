# Web UI (`odek serve`)

odek ships with a **single-page web UI** built entirely from Go's `embed` and zero external dependencies (no npm, no React, no build step). It's served from the same binary that runs on your terminal.

```bash
odek serve
# → odek serve ⚡  http://[::]:8080
#   WebSocket: ws://[::]:8080/ws
#   Type @ in the input to reference files and sessions.
```

Open `http://localhost:8080` in your browser. The UI auto-reconnects if the server restarts.

## Architecture

```
┌─────────────┐       WebSocket (RFC 6455)      ┌──────────────┐
│   Browser    │ ◄─────────────────────────────► │   odek serve  │
│  index.html  │   JSON messages (see protocol)  │  (Go binary)  │
└─────────────┘                                  └──────┬───────┘
                                                        │
                                              ┌─────────┴─────────┐
                                              │    Agent Loop      │
                                              │  (ReAct engine)    │
                                              └───────────────────┘
```

The server uses a **custom WebSocket implementation** (`internal/ws/`) — a compact hand-written RFC 6455 framer in pure Go. No gorilla/websocket, no caddy/caddylib, no external dependencies.

## Features

### Chat interface

- **Plain text input** — type your prompt, press `Enter` or `Ctrl+Enter`
- **Multi-turn sessions** — each prompt continues the same conversation (sidebar shows session history)
- **Streaming responses** — tokens are rendered in real-time via `requestAnimationFrame`-batched updates
- **Tool call blocks** — each tool invocation is rendered as a collapsible block showing the command and its output
- **History navigation** — `↑`/`↓` arrows cycle through your previous prompts (stored in `localStorage`)
- **File attachments** — drag-and-drop files onto the chat area, or use the paperclip button next to the input. Attached files appear as chips with filename, size, and a remove button. 5 MB per file, 10 MB total per prompt. File content is injected as context blocks.

### @ resource completion

Type `@` followed by a filename to see an autocomplete dropdown. odek resolves matching files and sessions:

### Token economics

Each response shows **per-message token stats** appended to the assistant bubble:

- ⚡ **Latency**: wall-clock time for the agent loop
- ⌂ **Context tokens**: cumulative prompt tokens across all iterations
- ⎇ **Output tokens**: cumulative completion tokens

The **top bar** displays **session-level totals** (∑ ⌂ context · ⎇ output), reset when you start a new session.

### Inline loading indicator

When you send a prompt, a compact **`.loading-indicator`** appears below your message (not a full-screen overlay). It shows:

- An animated spinner
- Cycling status messages every 2s: *"⚡ Thinking..."*, *"🔬 Analyzing..."*, *"🧪 Running diagnostics..."*, etc.
- 8 rotating messages keep you informed without blocking the UI

The indicator is removed automatically when the first `token` event arrives or on error.

### Smart autoscroll

The chat **only auto-scrolls when you're near the bottom** (within 60px). If you scroll up to read previous content while the agent responds, the page **does not steal your scroll position**. When you send a new message, it force-scrolls to the latest response.

This uses `requestAnimationFrame` batching to avoid layout thrashing during high-frequency token updates.

Type `@` followed by a filename to see an autocomplete dropdown. odek resolves matching files and sessions:

| Prefix | Source | Example |
|--------|--------|---------|
| `@` + path | Current directory files | `@src/main.go` → inlines `src/main.go` |
| `@sess:` + id | Saved sessions | `@sess:20260519-abc123` → inlines session transcript |

The dropdown fetches from `GET /api/resources?q=<query>&limit=8`. Results include files (recursive directory walk, skips `.git`, `node_modules`, etc.) and sessions.

**Security**: file paths are resolved relative to the working directory. Symlinks are blocked. Content is truncated at 50KB.

### Session management

- **Auto-save**: every prompt creates a new session if none is active, or appends to the current one
- **Sidebar**: lists all saved sessions (max 50), highlights the active one
- **Session data**: stored in `~/.odek/sessions/` as JSON files, same format used by `odek session`

## REST endpoints

All `/api/*` endpoints require the per-instance CSRF token (`odek_ws_token` cookie or `X-Odek-Ws-Token` header) and a loopback `Host` header; state-changing methods additionally require a local `Origin`. Missing/invalid credentials return 403.

### `GET /api/limits`

Returns the execution-budget configuration resolved at server start, so clients can render session costs without duplicating the price-resolution rule.

```jsonc
{
  "model": "deepseek-v4-flash",   // the server's configured model
  "limits": {                     // resolved budget.Limits as-is (see docs/CONFIG.md)
    "max_runtime_seconds": 300,
    "max_tool_calls": 50,
    "max_cost_usd": 0.50,
    "input_cost_per_million_usd": 2.0,
    "output_cost_per_million_usd": 8.0,
    "model_prices": {
      "deepseek-v4-flash": {
        "input_cost_per_million_usd": 0.14,
        "output_cost_per_million_usd": 0.28
      }
    }
  },
  "effective_prices": {           // limits.ResolvePrices(model) — model_prices entry wins, flat pair otherwise
    "input_cost_per_million_usd": 0.14,
    "output_cost_per_million_usd": 0.28
  }
}
```

Cost rendering for the current session model uses `effective_prices` directly:

```
cost_usd = input_tokens / 1e6 * input_cost_per_million_usd
         + output_tokens / 1e6 * output_cost_per_million_usd
```

When no prices are configured, `effective_prices` is `0`/`0` — treat that as "costs unavailable". To price a different model, look it up in `limits.model_prices` and fall back to the flat pair per field.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr :8080` | `:8080` | Listen address (e.g. `--addr localhost:9090`) |
| `--open` | false | Open browser automatically after starting |
| `--help`, `-h` | — | Show usage |

## WebSocket Protocol

The UI communicates entirely over a single WebSocket at `/ws`. Messages are newline-delimited JSON. The protocol is a simplex prompt → stream → done flow: the client sends one prompt, and the server streams back events until done.

### Client → Server

```jsonc
// Prompt — send a task to the agent
{
  "type": "prompt",
  "content": "What files are in src/?",
  "session_id": "20260519-abc123"  // optional — omit for new session
}

// Approval response — answer a security prompt
{
  "type": "approval_response",
  "id": "apr-a1b2c3d4",
  "action": "approve"  // "approve" | "deny" | "trust"
}
```

### Server → Client

| Event Type | When | Fields |
|------------|------|--------|
| `session` | At start of response | `session_id`, `auth_token`, `model`, `sandbox` |
| `token` | Streamed text content | `content` (markdown) |
| `thinking` | Streamed reasoning content | `content` |
| `tool_call` | Agent invokes a tool | `name`, `data` (raw tool-arguments JSON) |
| `tool_result` | Tool returns output | `name`, `data` (full, untruncated output) |
| `subagent_log` | Sub-agent progress within `delegate_tasks` | `task_idx`, `name`, `event`, `data` |
| `usage` | After each LLM turn within a run | `contextTokens`, `outputTokens` (cumulative for the run) |
| `done` | Agent finishes | `latency` (seconds), `contextTokens`, `outputTokens`, `cacheCreationTokens`, `cacheReadTokens`, `cachedTokens`, `sessionContextTokens`, `sessionOutputTokens` |
| `error` | Agent or server error | `message` |
| `approval_request` | Agent needs user approval for dangerous operation | `id`, `risk` (class name), `command` (or resource), `description`, `is_operation`, `allow_trust`, `friction`, `friction_approvals` |
| `approval_ack` | Server confirms an approval response | `id`, `action` |
| `skill_event` | Skill lifecycle event | `event`, `skill_name`, `skills`, `heuristic` |
| `memory_event` | Memory lifecycle event | `event`, `target`, `session_id`, `content`, `count`, `new_count`, `untrusted` |
| `agent_signal` | Agent self-observability signal | `event`, `detail`, `tool`, `count` |

Example event sequence:

```jsonc
{"type":"session","session_id":"20260519-x1y2z3","model":"deepseek-v4-flash"}
{"type":"token","content":"Let me look at the source directory."}
{"type":"tool_call","name":"shell","data":"{\"command\":\"ls -la src/\"}"}
{"type":"tool_result","name":"shell","data":"<untrusted_content_a1b2c3d4 source=\"shell\">\ntotal 24\ndrwxr-xr-x ...\n</untrusted_content_a1b2c3d4>"}
{"type":"token","content":"The `src/` directory contains 3 files:"}
{"type":"done","latency":4.2}
```

### Content sanitization contract

The server sends all message content **raw and unsanitized**. HTML-escaping
is the client's responsibility: any frontend (the bundled WebUI or a
third-party client) MUST escape/sanitize every string field before inserting
it into a DOM, terminal UI, or other rendering surface. Untrusted fields
include `token.content`, `thinking.content`, `tool_call.data`,
`tool_result.data`, `subagent_log.data`, `error.message`, all
`approval_request` strings, `skill_event.*`, `memory_event.*`, and
`agent_signal.*`.

Tool results (and user messages containing attachments or `@`-resource
references) may embed the nonce'd untrusted-content envelope:

```
<untrusted_content_<nonce> source="shell">
...raw body...
</untrusted_content_<nonce>>
```

The envelope is **model-facing trust metadata** (prompt-injection defense),
not user content. Clients SHOULD unwrap it for display: render the body
(escaped) and, optionally, present the `source` attribute as a badge. The
closing tag repeats the opening nonce — treat envelopes whose nonces don't
match as plain text. The bundled WebUI implements this in
`cmd/odek/ui/js/untrusted.js`.

## Implementation details

### Server stack (`cmd/odek/serve.go`)

| Component | File | Purpose |
|-----------|------|---------|
| HTTP server | `serve.go` (`handleStatic`) | Serves `index.html` from embedded FS |
| WebSocket upgrade | `internal/ws/ws.go` | RFC 6455 handshake + framing |
| WebSocket handler | `serve.go` (`handleWebSocket`) | Per-connection agent lifecycle |
| Prompt handler | `serve.go` (`handlePrompt`) | Resolves `@` refs, runs agent, streams result |
| Resource API | `serve.go` (`handleResourceSearch`) | `@` completion search endpoint |
| Session API | `serve.go` (`handleSessionList`) | Session listing endpoint |
| Limits API | `serve.go` (`handleLimits`) | Execution-budget config + effective prices endpoint |

### WebSocket implementation (`internal/ws/ws.go`)

- ~200 lines of zero-dependency Go
- Supports text frames, close frames, ping/pong (pong auto-reply)
- Fragmentation is not supported (every frame is FIN=true) — fine for JSON messages
- Thread-safe writes via `sync.Mutex`
- Error handling: returns `io.EOF` on clean close, raw `net.Error` on broken connection

### Frontend (`cmd/odek/ui/`)

- Vanilla JS + CSS SPA split into native ES modules under `js/` (state, dom, utils, escape, markdown, untrusted, render, approvals, sessions, input, ws, main, net) — no build step
- **Escaping**: all server-controlled strings are inserted escaped (`escapeHtml`/`escapeAttr`/`textContent`); `markdownToHtml` HTML-escapes all input by default and allowlists link schemes — see "Content sanitization contract" above
- **Untrusted envelope**: `js/untrusted.js` unwraps the model-facing `<untrusted_content_*>` envelope before display (body shown, source as badge)
- **Design system**: loaded from `https://assets.21no.de/css/tokens.css` — dark theme with CSS custom properties (`--bg-primary`, `--accent`, `--text-primary`, etc.)
- **Typeface**: loaded from `https://assets.21no.de/fonts/fonts.css` — uses `var(--font-sans)` and `var(--font-mono)`
- **Streaming**: token content is batch-rendered via `requestAnimationFrame` to avoid layout thrashing
- **DOM budget**: message list is capped at 80 elements (`MAX_MESSAGES`), older messages are pruned
- **Resilience**: auto-reconnects WebSocket on disconnect with 2s backoff
- **Tests**: `node --test "cmd/odek/ui/js/**/*.test.js"` (golden tests for the markdown renderer and the untrusted-envelope parser)

## Tips

- **Security sandbox**: `odek serve --addr localhost:8080` restricts to localhost. Use a reverse proxy (Caddy, nginx) for remote access.
- **Config inheritance**: `odek serve` reads the same config chain (`~/.odek/config.json` → `./odek.json` → env vars) as `odek run`. Set your model, API key, and sandbox settings there.
- **Session discovery**: reference any saved session via `@sess:ID` in your prompt to give the agent full context from previous conversations.
