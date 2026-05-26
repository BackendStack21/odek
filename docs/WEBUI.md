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

The server uses a **custom WebSocket implementation** (`internal/ws/`) — ~200 LOC of hand-written RFC 6455 framing in pure Go. No gorilla/websocket, no caddy/caddylib, no external dependencies.

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
| `session` | At start of response | `session_id`, `model` |
| `token` | Streamed text content | `content` (markdown) |
| `tool_call` | Agent invokes a tool | `name`, `command` |
| `tool_result` | Tool returns output | `name`, `output` (truncated to 500 chars) |
| `done` | Agent finishes | `latency` (seconds), `contextTokens`, `outputTokens`, `sessionContextTokens`, `sessionOutputTokens` |
| `error` | Agent or server error | `message` |
| `approval_request` | Agent needs user approval for dangerous operation | `id`, `risk` (class name), `command` (or resource), `description`, `is_operation` |

Example event sequence:

```jsonc
{"type":"session","session_id":"20260519-x1y2z3","model":"deepseek-v4-flash"}
{"type":"token","content":"Let me look at the source directory."}
{"type":"tool_call","name":"shell","command":"ls -la src/"}
{"type":"tool_result","name":"shell","output":"total 24\ndrwxr-xr-x ..."}
{"type":"token","content":"The `src/` directory contains 3 files:"}
{"type":"done","latency":4.2}
```

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

### WebSocket implementation (`internal/ws/ws.go`)

- ~200 lines of zero-dependency Go
- Supports text frames, close frames, ping/pong (pong auto-reply)
- Fragmentation is not supported (every frame is FIN=true) — fine for JSON messages
- Thread-safe writes via `sync.Mutex`
- Error handling: returns `io.EOF` on clean close, raw `net.Error` on broken connection

### Frontend (`cmd/odek/ui/index.html`)

- ~1,200 lines: single file with embedded CSS and vanilla JS (no frameworks)
- **Design system**: loaded from `https://assets.21no.de/css/tokens.css` — dark theme with CSS custom properties (`--bg-primary`, `--accent`, `--text-primary`, etc.)
- **Typeface**: loaded from `https://assets.21no.de/fonts/fonts.css` — uses `var(--font-sans)` and `var(--font-mono)`
- **Streaming**: token content is batch-rendered via `requestAnimationFrame` to avoid layout thrashing
- **DOM budget**: message list is capped at 100 elements (`MAX_MESSAGES`), older messages are pruned
- **Resilience**: auto-reconnects WebSocket on disconnect with 2s backoff

## Tips

- **Security sandbox**: `odek serve --addr localhost:8080` restricts to localhost. Use a reverse proxy (Caddy, nginx) for remote access.
- **Config inheritance**: `odek serve` reads the same config chain (`~/.odek/config.json` → `./odek.json` → env vars) as `odek run`. Set your model, API key, and sandbox settings there.
- **Session discovery**: reference any saved session via `@sess:ID` in your prompt to give the agent full context from previous conversations.
