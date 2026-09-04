# Web UI (`odek serve`)

odek ships with a **single-page web UI** built entirely from Go's `embed` and zero external dependencies (no npm, no React, no build step). It's served from the same binary that runs on your terminal.

```bash
odek serve
# → odek serve ⚡  http://127.0.0.1:8080/?token=<per-instance token>
#   WebSocket: ws://127.0.0.1:8080/ws
#   WS token:  <per-instance token>
#   Type @ to reference files, drop or attach files inline.
```

Open the printed **token URL** in your browser — the token authenticates the
WebSocket handshake and every `/api/*` call (cookie + `X-Odek-Ws-Token`
header + `odek.<token>` subprotocol). A plain `http://localhost:8080` loads
the UI but cannot connect until you use the token URL. The UI reconnects
automatically (exponential backoff, 1s → 30s cap) if the server restarts.

## Architecture

```
┌─────────────┐  WebSocket /ws (interactive)  ┌──────────────┐
│   Browser    │ ◄──────────────────────────► │   odek serve  │
│  index.html  │  REST /api/* (management)    │  (Go binary)  │
└─────────────┘                               └──────┬───────┘
                                                     │
                                           ┌─────────┴─────────┐
                                           │    Agent Loop      │
                                           │  (ReAct engine)    │
                                           └───────────────────┘
```

Two surfaces, one binary:

- **`/ws`** — the interactive chat transport (prompt → stream → done, approvals, heartbeat). One agent per connection.
- **`/api/*`** — the REST management surface used by the UI *and* by external clients (bodek, curl, dashboards): sessions, headless runs with remote approvals, memory/skills/tools, events, usage, connections, health, config. See "REST endpoints" below.

The WebSocket transport is provided by `golang.org/x/net/websocket` (RFC 6455 handshake and frame I/O); `internal/ws/` holds the shared frame constants, and `serve.go` owns the per-connection lifecycle.

## Building an external client (TUI, bots, tools)

Everything the bundled WebUI does is available to any client over the same
two surfaces. This section is the integration contract; the full reference
follows below.

### Authentication model

Two token layers:

1. **Instance token** (one per `odek serve` process) — printed to stderr as
   the token URL (`http://127.0.0.1:8080/?token=<64-hex>`) at startup.
   Present it as the `X-Odek-Ws-Token` header on every REST call, and/or as
   the `odek.<token>` WebSocket subprotocol, and/or rely on the
   `odek_ws_token` cookie a browser obtained from the token URL. Without it
   every `/api/*` call is **403** and the WS handshake is rejected.
2. **Session token** (per session) — required by session-scoped mutations
   and detail reads (`X-Session-Token` header). A fresh token is issued
   when the session is created: the WS `session` event carries
   `auth_token`, and REST runs echo it in detail responses. To adopt a
   session you didn't create (or after losing the token), GET
   `/api/sessions/{id}` with the **instance** token header — the server
   bootstraps and returns the session token in the `X-Session-Token`
   response header.

### Minimal interactive flow (WebSocket)

1. Read the token URL from serve's stderr; extract the token.
2. `GET /?token=<t>` (optional — validates the token, sets the cookie).
3. Open `ws://…/ws` with subprotocol `odek.<t>` → receive `server_info`.
4. Send `{"type":"prompt","content":"…","session_id":null}` → receive
   `session` (capture `session_id` + `auth_token`), then `thinking_delta` /
   `token_delta` fragments (or one bulk `token` when streaming is off or
   the provider falls back), `tool_call`/`tool_result` pairs, and finally
   `done`.
   **`done` is emitted only after the session is persisted** — refreshing
   session state on `done` is race-free by contract.
5. Answer `approval_request` with `{"type":"approval_response","id":…,
   "action":"approve"|"deny"|"trust"}` (default timeout 60s).
6. Keep alive with `{"type":"ping"}` (answered inline with `pong`, even
   mid-run). The server also pushes `keepalive` every 20s so idle proxies
   do not drop a socket waiting on a thinking model. Cancel with
   `{"type":"cancel","session_id":…,"auth_token":…}`.
7. Continue the session by including `session_id`+`auth_token` on the next
   prompt, or `{"type":"session_switch", …}` to adopt one without prompting.

### Minimal headless flow (REST only — no WebSocket)

1. `POST /api/prompt` `{"content":"…","approval_timeout_seconds":300}` →
   `202 {run_id, session_id, status:"running"}`.
2. Poll `GET /api/runs/{id}` until `status` is `completed` / `failed` /
   `cancelled`; read `result`, `input_tokens`/`output_tokens`, and the
   bounded `events` tail.
3. While `status` is `waiting_approval`: `GET /api/runs/{id}/approvals`,
   answer with `POST /api/runs/{id}/approvals/{aid}` `{"action":"approve"}`.
4. Cancel with `DELETE /api/runs/{id}` or `POST /api/runs/{id}/cancel`.

### Cross-cutting contracts

- **Caching**: every `/api/*` response carries `Cache-Control: no-store`;
  static assets carry strong ETags with `no-cache, must-revalidate`.
- **Errors**: `403` missing/invalid instance token; `401` missing/invalid
  session token; `404` unknown id; `400` malformed input; `405` wrong
  method; `429` session-detail rate limit (60/min per IP).
- **Limits**: WS frame ≤ 8 MiB; ≤ 20 concurrent WS connections; 30 WS
  upgrades/min per IP; prompt ≤ 1 MiB; attachments ≤ 5 MiB each / 10 MiB
  total; model ids ≤ 128 chars matching `[A-Za-z0-9_.:/@-]+`; REST request
  bodies ≤ 2 MiB on the runs surface (`POST /api/prompt`), ≤ 1 MiB on the
  management endpoints (`/api/*` in `serve_api.go`).
- **Token field naming**: WS event fields are camelCase
  (`contextTokens`, `outputTokens`); REST JSON is snake_case
  (`input_tokens`, `session_id`). Don't mix them up.

## Features

### Chat interface

- **Plain text input** — type your prompt, press `Enter` to send, `Shift+Enter` for a newline
- **Multi-turn sessions** — each prompt continues the same conversation (sidebar shows session history)
- **Live streaming** *(opt-in: `--stream` / `stream: true` / `ODEK_STREAM=true`)* — answer and reasoning fragments arrive as they are generated (`token_delta` / `thinking_delta`) and render through the same rAF-batched pipeline; the ⚡ badge in the top bar indicates streaming is active. Providers that reject SSE fall back silently to the bulk path.
- **Reasoning blocks** — thinking models get a collapsible *reasoning* block above the answer. It **auto-expands while its turn is live** (with auto-follow scrolling) and **auto-collapses when the next turn starts** — only renderer-opened blocks collapse, so manually opened history is never touched; blocks reloaded from history start collapsed
- **Tool call blocks** — each tool invocation renders as a collapsible block with arguments, result (truncated behind a "show all" expander), latency, and an untrusted-source badge when the output was wrapped
- **Sub-agent cards** — `delegate_tasks` runs render as a per-task grid with live logs and final summaries
- **Inline approvals** — dangerous operations block the run and show a decision card (risk class, plain-language explanation, verbatim command). Friction mode (after 3 same-class approvals in 60s) requires typing the literal word `approve`; `trust session` is hidden for destructive/blocked/unknown classes. Keyboard: `A` approve, `D` deny, `T` trust
- **Cancel** — the ✕ button cancels the running prompt over the WebSocket (`cancel` message), with the REST endpoint as fallback
- **Thinking toggle** — `think` button or `Alt+T` toggles extended reasoning for the next prompt (persisted)
- **Model switching** — the picker lists `GET /api/models` (provider `ListModels` catalog, configured model marked current, with context sizes) plus an "Other…" free-text entry; switches apply from the next prompt
- **History navigation** — `↑`/`↓` arrows cycle through your previous prompts (stored in `localStorage`)
- **Keyboard shortcuts** — `?` toggles the cheat sheet (`Enter`, `@` completion, `⌘R` refresh sessions, `Alt+T` thinking, `Alt+M` panels, `A/D/T` approvals)
- **File attachments** — drag-and-drop files onto the chat area, or use the paperclip button. Attached files appear as chips with filename, size, and a remove button. 5 MB per file, 10 MB total per prompt; content crosses the trust boundary wrapped in the untrusted-content envelope

### Server status & heartbeat

The top-bar status group (`connected / reconnecting…`) doubles as a **health popover** — click it for version, uptime, model, sandbox/streaming state, live connection count, WebSocket round-trip latency, and lifetime usage (prompts, tokens, estimated cost when prices are configured). An application-level heartbeat (`ping`/`pong` every 20s) measures RTT and detects dead links early; the server also pushes `keepalive` every 20s so idle proxies do not drop a silent thinking turn. The latency chip turns amber above 1s.

### Management panels (Alt+M)

The right-side drawer exposes the REST management surface in the UI:

- **memory** — user/env facts with add/remove, caps, and the pending-review episode queue with promote
- **skills** — discovered skills with source, auto-load, usage counts, and `needs review` / `untrusted` provenance badges
- **tools** — the built-in registry with enabled/disabled state and the MCP server count
- **runs** — headless REST runs (`POST /api/prompt`): live status, tokens, results, cancel, and inline approve/deny/trust for pending approvals (polled every 3s while visible)
- **events** — the recent `odek.event/v1` feed (`run_started`, `iteration_completed`, `tool_call_*`, `run_completed`, …)
- **plan** — the active session's structured task plan (read-only; polls `GET /api/sessions/{id}/plan` every 5s while visible — see the endpoint reference below)

### @ resource completion

Type `@` followed by a filename to see an autocomplete dropdown. odek resolves matching files and sessions:

### Token economics

Each response shows **per-message token stats** appended to the assistant bubble:

- ⚡ **Latency**: wall-clock time for the agent loop
- **Input tokens**: cumulative prompt tokens across all iterations
- **Output tokens**: cumulative completion tokens
- **Cache metrics** (when non-zero): `stored` (cache writes), `read` (cache hits), `cached` (automatic prefix match)

The **top bar carries a consolidated metrics cluster** (appears once a run reports data):

- **Context gauge** — `ctx ▓▓▓░░ 40%`: the live context-window usage from per-iteration `usage` events, against the model's window size from `/api/models`. Amber above 60%, red above 85%; a `context_trimmed` signal flashes the gauge. Without a known window size it shows raw tokens. Hover for exact numbers and the trimming note.
- **Session tokens** — `⇥ in ↦ out`, cumulative session totals from `done` events.
- **Session cost** — `◈ $0.201`, estimated from the session's token totals and the resolved prices (`/api/limits`: `model_prices` per-model override, flat pair fallback — the client-side twin of `limits.ResolvePrices`). Hidden entirely when no prices are configured.

Each assistant message's stats footer also gains a per-turn cost (`◈`) when prices are configured, and the inline loading indicator shows **live elapsed time and iteration count** (`thinking · 7s · iter 2`) while the run is in flight. Sidebar items carry per-session cumulative usage (⇥ in / ↦ out), and `/api/usage` aggregates server-lifetime totals with cost.

### Inline loading indicator

When you send a prompt, a compact **`.loading-indicator`** appears below your message (not a full-screen overlay). It shows:

- An animated spinner
- Cycling status messages every 2s: *"⚡ Thinking..."*, *"🔬 Analyzing..."*, *"🧪 Running diagnostics..."*, etc.
- 8 rotating messages keep you informed without blocking the UI

The indicator is removed automatically when the first streamed fragment or `token` event arrives, or on error.

### Smart autoscroll

The chat **only auto-scrolls when you're near the bottom** (within 60px). If you scroll up to read previous content while the agent responds, the page **does not steal your scroll position**. When you send a new message, it force-scrolls to the latest response.

This uses `requestAnimationFrame` batching to avoid layout thrashing during high-frequency token updates.

| Prefix | Source | Example |
|--------|--------|---------|
| `@` + path | Current directory files | `@src/main.go` → inlines `src/main.go` |
| `@sess:` + id | Saved sessions | `@sess:20260519-abc123` → inlines session transcript |

The dropdown fetches from `GET /api/resources?q=<query>&limit=8`. Results include files (recursive directory walk, skips `.git`, `node_modules`, etc.) and sessions.

**Security**: file paths are resolved relative to the working directory. Symlinks are blocked. Content is truncated at 50KB.

### Session management

- **Auto-save**: every prompt creates a new session if none is active, or appends to the current one; per-turn persistence means an interrupted run resumes from the last completed step
- **Sidebar**: paginated listing (50 per page, "load more"), highlights the active one, shows turns, age, model chip, and cumulative token usage
- **Search**: the sidebar search box queries **server-side** (`GET /api/sessions?q=…`, case-insensitive over task/model/id, debounced 250ms)
- **Pin**: 📌 floats a session to the top of the list (persisted on the session, `POST /api/sessions/{id}` `{pinned}`)
- **Rename**: ✎ inline-edits the session label
- **Export**: ⇩ downloads the transcript — markdown by default, JSON with `Shift` held (`GET /api/sessions/{id}/export`)
- **Delete**: ✕ with a confirmation dialog
- **Switching**: clicking a session renders the full transcript (tool calls, reasoning, sub-agents) and sends a `session_switch` message so the connection's agent adopts the session (memory-buffer restore) before you type
- **Session data**: stored in `~/.odek/sessions/` as JSON files, same format used by `odek session`

## REST endpoints

### Background jobs

- `GET /api/jobs` — the authenticated session's background jobs (id, command head, status, runtime, exit code).
- `GET /api/jobs/{id}/output?since=N&limit=N` — output window with `next_cursor` for continuation.
- `POST /api/jobs/{id}/stop` — kill a running job.

All three require the same per-instance CSRF token, loopback Host check, and
session authentication as the other `/api` routes; the POST additionally
requires the local-origin check. Session tokens are validated strictly on
every call — the token must be presented (legacy token-less sessions are
minted but never bootstrap-passed on jobs routes). Foreign job ids answer
`{"status":"unknown"}` (same shape as stale ids — no existence oracle across
sessions).

All `/api/*` endpoints require the per-instance CSRF token (`odek_ws_token` cookie or `X-Odek-Ws-Token` header) and a loopback `Host` header; state-changing methods additionally require a local `Origin`. Missing/invalid credentials return 403.

Every response carries `Cache-Control: no-store`. The surface covers six
groups, detailed below: **sessions** (search/list, detail, rename/pin,
export, delete, cancel), **budgets** (`/api/limits`), **agent state**
(models, profiles, resources, tools, memory, skills), **headless runs**
(`/api/prompt` + `/api/runs/*`), **observability** (health, usage, events,
connections), and **administration** (config view, MCP listing, memory
consolidate, skills promote, shutdown). Session-scoped reads and mutations
additionally require the per-session auth token (`X-Session-Token` header or the `session_token` cookie — the header wins when both are present), which
the server issues when the session is created (see "Building an external
client" above for the bootstrap flow). The agent itself cannot reach any of
these — its browser/http tools refuse loopback via the SSRF guard and it
never holds the instance token.

### `GET /api/resources?q=&limit=`

`@`-reference search over workspace files, saved sessions, and skills — the
completion backend. `limit` defaults to 10, capped at 100.

```jsonc
[
  { "id": "@src/main.go",  "type": "file",    "label": "src/main.go",  "detail": "Go source, 142 lines" },
  { "id": "@sess:2026…",   "type": "session", "label": "fix login bug", "detail": "3 turns" }
]
```

Inline a result by embedding its `id` verbatim in the prompt content; the
server resolves and wraps it as untrusted content.

### `GET /api/models`

The provider's `ListModels` catalog plus the configured model (always present, `current: true`). Context windows come from the provider, then the last-resort table, else 0. Capped at 256 entries. `/api/profiles` is retired.

```jsonc
[
  { "id": "glm-5.3-flash", "max_context": 1000000, "description": "GLM 5.3 Flash — 976K ctx", "current": true },
  { "id": "glm-5.3", "max_context": 1000000, "description": "GLM 5.3 — 976K ctx" }
]
```

`max_context` feeds the metrics gauge. The picker also has an "Other…" free-text entry for ids not in the catalog. If `ListModels` fails, the response is the configured model only.

### `GET/POST/DELETE /api/sessions/{id}`

The core session CRUD (session-token gated):

- **GET** — the full session record (messages, buffer, `auth_token`,
  `pinned`, `input_tokens`/`output_tokens`). The effective session token is
  echoed in the `X-Session-Token` response header; presenting only the
  instance token bootstraps and returns it for sessions you didn't create.
  Rate-limited to 60 lookups/min per IP (**429** beyond that).
- **POST** `{"name"?: string, "pinned"?: bool}` — rename and/or pin;
  at least one field required (**400** otherwise).
- **DELETE** — removes the session and its index entry; **204**.

### `POST /api/cancel?session_id=`

Cancels the prompt currently executing on a session (the REST twin of the
WS `cancel` message). Requires the session's auth token. Returns **200**
with the outcome — `idle:true` means no live prompt was registered for
that session, so nothing was cancelled:

```jsonc
{ "session_id": "20260519-abc123", "idle": false }
```

### Cancellation

All cancel paths — the WebUI ✕ button (WS `cancel` message),
`POST /api/cancel`, and `POST /api/runs/{id}/cancel` — interrupt a pending
approval wait instead of leaving the run blocked until the approval
timeout (60s by default; headless runs may raise it, capped at 10
minutes). On the `cancelled` event the UI dismisses pending approval
cards, so an approval cannot be answered after a cancel.

Sub-agents get the same lifecycle parity:

- **Turn-level cancel kills sub-agents.** Each child runs under the
  parent prompt's context; a turn cancel (or socket disconnect) SIGKILLs
  every running sub-agent process.
- **Per-sub-agent stop.** The WS `subagent_cancel` message (session-token
  scoped like `cancel`) cancels one task by `task_id` without touching
  the turn or its siblings. Children killed before reporting still emit
  a terminal `subagent_state` (`finished`/`cancelled`) so cards and the
  `/api/subagents` registry converge.
- **Status framing.** A user/turn cancel reports `cancelled`; only the
  per-task deadline reports `timeout` — the two are never conflated.

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

### `GET /api/health`

Server metadata for monitoring and the WebUI status popover. Never carries secrets.

```jsonc
{
  "status": "ok",
  "version": "1.35.0",         // ldflags build version ("" for dev builds)
  "started_at": "2026-08-21T08:19:29Z",
  "uptime_seconds": 1903,
  "model": "glm-5.3",           // configured model
  "sandbox": true,              // sandbox mode
  "stream": true,               // live delta streaming enabled
  "ws_connections": 2           // live WebSocket connections
}
```

### `GET /api/sessions?q=&limit=&offset=`

Server-side search and pagination over the session list. With **no** query parameters the endpoint returns the legacy bare JSON array (existing consumers pin that shape). With any of `q` / `limit` / `offset` present it returns an envelope:

```jsonc
{
  "sessions": [ /* session records, auth tokens stripped */ ],
  "offset": 0,
  "limit": 50,                  // capped at 200
  "count": 50,
  "query": "deploy"             // lowercased echo of q
}
```

`q` is a case-insensitive substring match over task, model, and session id.

### `GET /api/sessions/{id}/export?format=md|json`

Downloads a transcript. Session-token auth applies exactly like the detail read (`X-Session-Token` header, `session_token` cookie, or the instance-token bootstrap). `format=md` (default) renders a standalone markdown document — metadata header, `## user` / `## assistant` sections, tool calls and results as fenced blocks, reasoning behind `<details>`, untrusted-content envelopes unwrapped; `format=json` returns the raw session record. Responses carry `Content-Disposition: attachment`.

### `GET /api/sessions/{id}/plan`

Read-only structured plan view (the WebUI **plan** tab's data source; see
[PLANNING.md](PLANNING.md) for the full surface map). Session-token auth
applies exactly like the detail read, and the same 60/min-per-IP
session-lookup rate limit applies (**429** beyond that). The server parses
the newest parseable `[Current plan:` system message with the same strict
resume parser the engine uses (`loop.ExtractPlan`) — no plan state is stored
anywhere beyond the transcript itself.

```jsonc
{
  "session_id": "2026…",
  "version": 3,
  "steps": [
    { "id": "s1", "title": "Scaffold command skeleton", "status": "done" },
    { "id": "s2", "title": "Wire flag parsing", "status": "in_progress",
      "note": "waiting on schema decision" }
  ],
  "found": true
}
```

`found:false` (still HTTP 200) means the transcript carries no parseable
plan message — `version`/`steps` are then zero/empty (a collapsed all-done
plan parses to a version with `steps: []`). Unknown session ids return
**404**; `note` is omitted when empty. Strictly GET-only: non-GET requests
to `…/plan` do not fall through to the base-session mutators.

### `GET /api/memory` · `POST/DELETE /api/memory/facts` · `POST /api/memory/episodes/promote`

Operator-gated memory management (the REST face of `odek memory`):

- `GET /api/memory` → facts grouped by target (`user` / `env`, entries split on the `§` separator), configured caps, episode totals, and the pending-review queue (tainted episodes stored but excluded from recall).
- `POST /api/memory/facts` `{target:"user"|"env", content}` — adds through the same MemoryManager path the agent's memory tool uses, including the unsafe-content filter (`curl … | sh`-style facts are rejected).
- `DELETE /api/memory/facts` `{target, old_text}` — removes the matching entry.
- `POST /api/memory/episodes/promote` `{session_id}` — promotes a tainted episode to recallable, the same human gate as the CLI. The agent cannot reach these endpoints (its browser/http tools refuse loopback via the SSRF guard, and it never holds the instance token), so the gate stays human.

### `GET /api/skills`

Skill listing with provenance: `name`, `description`, `auto_load`, `usage_count`, `source` (directory), `needs_review`, `untrusted`. Bodies are omitted (size and injection hygiene — load them via the agent's `skill_load`). Skills pinned `needs_review` are excluded from trigger matching and from `skill_load` (agent-side body reads) until `odek skill promote`.

### `GET /api/tools`

The built-in tool registry with the resolved enabled/disabled state after `tools.enabled` / `tools.disabled` filtering, plus the configured MCP server count (per-connection tool lists vary with MCP).

### `POST /api/prompt` — headless runs

Runs the full agent without a WebSocket. The body is the prompt message:

```jsonc
{
  "content": "summarize the repo",   // required, same 1 MiB cap as WS prompts
  "session_id": "2026…",             // optional — omit to create a session
  "auth_token": "…",                 // session token when continuing
  "model": "glm-5.3",                // optional per-run model override
  "thinking": "enabled",             // optional per-run thinking toggle
  "approval_timeout_seconds": 300,   // approval wait (default 60s, cap 600s)
  "attachments": [{ "name": "f.txt", "content": "…" }]
}
```

Returns `202 {run_id, session_id, status:"running"}`. The run executes the
exact `handlePrompt` path (refs, audit, per-turn persistence); events land in
the run record and the `/api/events` ring. Approvals block the run and are
answerable over REST:

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/runs` | GET | Recent runs (status, timing, tokens; newest first) |
| `/api/runs/{id}` | GET | Detail incl. event tail (200 events) + result |
| `/api/runs/{id}` | DELETE | Cancel |
| `/api/runs/{id}/cancel` | POST | Cancel (reports `idle:true` when already finished) |
| `/api/runs/{id}/approvals` | GET | Pending approval requests (risk, command) |
| `/api/runs/{id}/approvals/{aid}` | POST | `{action: "approve" \| "deny" \| "trust"}` |

Answers flow through the same `wsApprover` path as the WebUI — trust caching
and friction behave identically. Tainted/dangerous classes still never offer
`trust`. The registry keeps the newest ~100 runs (≥20 completed) and evicts
oldest completed first.

Failed runs keep their session linkage: the user prompt is persisted to the
session before the first LLM call, and a failed turn closes with an explicit
`[Turn aborted: …]` assistant note instead of ending on a dangling tool call —
`run.session_id` is therefore always populated and the transcript never loses
the prompt. Persistent provider rate limits (HTTP 429 past the client's retry
budget) surface as a typed summary in the run record and the durable serve
log: `~/.odek/serve.log` by default (`--log-file` to override, mode 0600,
rotated by the storage janitor alongside `telegram.log`/`schedule.log`). The
log records run/turn lifecycle — IDs, statuses, latencies, failure
classifications — never prompt or completion content.

### `GET /api/events?limit=&run_id=&session_id=`

Recent `odek.event/v1` runtime events (ring of 500, oldest-first, filtered).
Event payloads carry SHA-256 arg hashes and redacted fields only — never raw
tool arguments. Every WS prompt and REST run feeds the same ring.

### `GET /api/usage`

Server-lifetime aggregates: `prompts_started/completed/failed`,
`tokens_in/out`, `estimated_cost_usd` via `limits.ResolvePrices`, and
`prices_configured` (false ⇒ render costs as "unavailable"). Sessions also
carry cumulative `input_tokens`/`output_tokens` (shown in list/detail).

### `GET /api/subagents?key=`

Sub-agent lifecycle registry snapshot (ring of 256, oldest evicted): one
entry per delegated task — `task_id`, `run_key` (connection id for WS runs,
run id for headless runs), redacted + truncated `goal`, `phase`
(`started`/`active`/`finished`), `status`, `pid`, timestamps, `iterations`,
`step`, `last_tool`, `duration_seconds`, `tokens_used`. Filter by
`?key=<run_key>`; unfiltered returns all recent entries.

### `GET /api/connections` · `DELETE /api/connections/{id}`

Live WebSocket connections (id, remote addr, connected-at, session, model,
busy, prompt count). DELETE kicks a connection by closing its socket — the
handler's defers tear down the agent and sandbox cleanly.

### `GET /api/config`

Sanitized resolved-config view: provider id (not the `providers` map), model, sandbox knobs, stream/compaction/
caching flags, iteration/parallelism limits, memory/skills/tool-filter
summaries, maintenance retention, dangerous default action, guard scan
toggles, sub-agent budgets (`subagent`), background-command settings
(`background`), and execution budgets with effective token prices
(`limits`). Secrets (`api_key`, `base_url`, env values, search backends)
are never included. The agent-facing `config_view` tool renders this same
view (section-filterable); parity is pinned by tests.

### `GET /api/mcp`

Configured MCP servers with command/args and extension limits, plus
`project` markers for servers sourced from `./odek.json`. Env values are
withheld (they may carry credentials).

### `POST /api/skills/promote`

`{name, force?}` — the REST face of `odek skill promote`: clears
NeedsReview so a skill can auto-load. Tainted skills still require `force`.

### `POST /api/memory/consolidate`

`{target: "user"|"env"}` — merges similar facts through the LLM (same
`MemoryManager.Consolidate` the agent uses).

### `POST /api/shutdown`

Triggers the graceful drain (stop accepting → close WebSockets → wait for
sandbox cleanup). For remote-restart management flows.

### Session pinning

`POST /api/sessions/{id}` now accepts `{name?, pinned?}` (either or both);
listings return pinned sessions first, and both list and detail carry
`pinned`, `input_tokens`, `output_tokens`, and `model`.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr <addr>` | `127.0.0.1:8080` | Listen address (loopback by default; e.g. `--addr localhost:9090`). Binding a non-loopback address prints a loud warning |
| `--open` | false | Open browser automatically after starting |
| `--tool <name>` | — | Enable a specific tool for served runs (repeatable; highest-priority whitelist layer) |
| `--no-tool <name>` | — | Disable a specific tool for served runs (repeatable; merged with lower-priority disabled lists) |
| `--trusted-proxies <ips/cidrs>` | — | Client IPs trusted for `X-Forwarded-For` / `X-Real-Ip` resolution, used by rate limiting. **Only set this for proxies you control** — a spoofed header from an untrusted client defeats per-IP rate limits |
| `--log-file <path>` | `~/.odek/serve.log` | Durable run/turn log (mode 0600, symlink-resistant). Lifecycle lines only — never prompt or completion content. Rotated by the storage janitor |
| `--stream` | config | Stream LLM responses live to the WebUI (`token_delta` / `thinking_delta` events) |
| `--no-stream` | config | Disable live streaming (bulk `token` events only) |
| `--help`, `-h` | — | Show usage |

Plus the shared sandbox flags (`--sandbox`, `--no-sandbox`, `--sandbox-image`, …) — see `odek serve --help`.

## WebSocket Protocol

The UI communicates entirely over a single WebSocket at `/ws`. Messages are newline-delimited JSON. The protocol is a simplex prompt → stream → done flow: the client sends one prompt, and the server streams back events until done.

### Client → Server

```jsonc
// Prompt — send a task to the agent. Caps: content ≤ 1 MiB; model ≤ 128
// chars of [A-Za-z0-9_.:/@-]; attachments ≤ 5 MiB each, 10 MiB total.
{
  "type": "prompt",
  "content": "What files are in src/?",
  "session_id": "20260519-abc123",  // optional — omit for new session
  "auth_token": "…",                // when continuing a session
  "model": "glm-5.3",               // optional per-run model override
  "thinking": "enabled",            // optional per-run thinking toggle
  "attachments": [{ "name": "f.txt", "content": "…" }]
}

// Approval response — answer a security prompt
{
  "type": "approval_response",
  "id": "apr-a1b2c3d4",
  "action": "approve"  // "approve" | "deny" | "trust"
}

// Heartbeat — answered inline by the socket reader, so it works while a
// prompt is running. Server replies with a pong carrying a server snapshot.
{ "type": "ping" }

// Cancel the running prompt over the socket (same session-scoped auth as
// POST /api/cancel — the target session's auth_token is required).
{
  "type": "cancel",
  "session_id": "20260519-abc123",
  "auth_token": "…"
}

// Stop ONE running sub-agent (the card stop button). Handled inline by
// the socket reader, so it works while delegate_tasks occupies the
// prompt processor. Same session-scoped auth as cancel. The server
// replies with a subagent_cancelled ack; the card's terminal state
// arrives as a subagent_state finished/cancelled transition.
{
  "type": "subagent_cancel",
  "session_id": "20260519-abc123",
  "auth_token": "…",
  "task_id": "task-uuid"
}

// Switch the connection to an existing session without sending a prompt:
// restores the memory buffer into the connection's agent and emits the
// standard `session` event.
{
  "type": "session_switch",
  "session_id": "20260519-abc123",
  "auth_token": "…"
}
```

### Server → Client

| Event Type | When | Fields |
|------------|------|--------|
| `server_info` | Pushed once on connect | `version`, `model`, `sandbox`, `stream`, `uptime_seconds`, `ws_connections` |
| `pong` | Reply to a client `ping` | `t` (unix ms), plus the `server_info` snapshot fields |
| `keepalive` | Server-initiated idle traffic every 20s (independent of client pings) so proxies/NATs do not close a socket waiting on a slow LLM | `t` (unix ms) |
| `session` | At start of response, and after `session_switch` | `session_id`, `auth_token`, `model`, `sandbox` |
| `turn_started` | Emitted for **every** turn (operator and system-initiated wake alike) immediately after the matching `session` frame and before the first streamed frame — clients open/upsert the streaming card by `turn_id`, so a missed `session` frame can no longer strand a turn | `turn_id` (`t_<hex>`), `session_id`, `initiated` (`"operator"` or `"system"` — computed server-side via the wake provenance gate; client input cannot influence it), `model` (mirrors the `session` frame's `model`) |
| `token_delta` | Live streamed answer fragment (streaming on) | `content` (markdown fragment) |
| `thinking_delta` | Live streamed reasoning fragment (streaming on) | `content` |
| `cancelled` | After a `cancel` message is honored | `session_id`, `idle` (true when nothing was running) |
| `subagent_cancelled` | Ack for a `subagent_cancel` message | `session_id`, `task_id`, `accepted` (false is a benign race — the task already finished) |
| `token` | Final answer text (bulk; **suppressed when streamed via `token_delta`**) | `content` (markdown) |
| `thinking` | Reasoning content (bulk; suppressed when `thinking_delta` streamed it) | `content` |
| `tool_call` | Agent invokes a tool | `name`, `data` (raw tool-arguments JSON) |
| `tool_result` | Tool returns output | `name`, `data` (full, untruncated output) |
| `subagent_log` | Sub-agent progress within `delegate_tasks` | `task_idx`, `task_id`, `name`, `event`, `data` (redacted, capped 8 KiB) |
| `subagent_state` | Per-task sub-agent lifecycle transition (`started`/`active`/`finished`); child emits `subagent_started`/`subagent_progress`/`subagent_finished` records over the same protocol. A sub-agent killed without reporting (user stop, turn cancel, timeout, flood-kill, crash) gets its terminal `finished` transition emitted by the parent instead, so cards never stay `running` | `task_idx`, `task_id`, `run_key`, `phase`, `status`, `step`, `iterations`, `tool`, `duration_seconds`, `tokens_used` |
| `done` | Agent finishes — **emitted only after the session is persisted**, so refreshing session state on `done` is race-free | `latency` (seconds), `contextTokens`, `outputTokens`, `cacheCreationTokens`, `cacheReadTokens`, `cachedTokens`, `sessionContextTokens`, `sessionOutputTokens` |
| `usage` | After each LLM iteration of a running turn | `contextTokens`, `outputTokens` (camelCase — the per-iteration context size drives the metrics gauge) |
| `error` | Agent or server error | `message` |
| `approval_request` | Agent needs user approval for dangerous operation; blocks the run up to `timeout_seconds` (60s default) | `id`, `risk` (class name), `command` (or resource), `description`, `is_operation`, `allow_trust`, `friction`, `friction_approvals`, `timeout_seconds` (the effective server-enforced wait in seconds — render the card's countdown from it) |
| `approval_ack` | Server confirms an approval response | `id`, `action` |
| `approval_expired` | Server declares the approval dead after `timeout_seconds` elapsed with no response — autoclose the matching card; late `approval_response` frames for the id are dropped | `id` |
| `skill_event` | Skill lifecycle event (`loaded`/`autoloaded`/`used`/`deleted` — `skill_save`/`skill_patch` were removed with the self-learning feature) | `event`, `skill_name`, `skills`, `heuristic` |
| `memory_event` | Memory lifecycle event | `event`, `target`, `session_id`, `content`, `count`, `new_count`, `untrusted` |
| `agent_signal` | Agent self-observability signal | `event`, `detail`, `tool`, `count` |

Every frame of an active turn — `token`, `thinking`, `tool_call`,
`tool_result`, `done`, `error` — also carries `turn_id`, matching the
turn's `turn_started.turn_id`, so a client that attached mid-turn (after a
reconnect) can attribute stray frames and reconcile card state without
heuristic idle detection. Lifecycle frames (`session`, `server_info`,
`pong`, `keepalive`, `usage`, `cancelled`, `subagent_*`, `approval_*`, `skill_event`,
`memory_event`, `agent_signal`) and the live `*_delta` fragments never
carry it. The `session` frame's legacy `system_initiated: true` stamp
(wake turns only) remains for old clients; `turn_started.initiated`
supersedes it. Versioning: all new fields are additive and absent when
not applicable — old clients ignore the unknown frame and unknown fields,
and new clients against an old server simply never see `turn_started`
(and fall back to the `session` stamp or lazy card open).

Example event sequence:

```jsonc
{"type":"session","session_id":"20260519-x1y2z3","model":"deepseek-v4-flash"}
{"type":"turn_started","turn_id":"t_9f86d081884c7d65","session_id":"20260519-x1y2z3","initiated":"operator","model":"deepseek-v4-flash"}
{"type":"token","content":"Let me look at the source directory.","turn_id":"t_9f86d081884c7d65"}
{"type":"tool_call","name":"shell","data":"{\"command\":\"ls -la src/\"}","turn_id":"t_9f86d081884c7d65"}
{"type":"tool_result","name":"shell","data":"<untrusted_content_a1b2c3d4 source=\"shell\">\ntotal 24\ndrwxr-xr-x ...\n</untrusted_content_a1b2c3d4>","turn_id":"t_9f86d081884c7d65"}
{"type":"token","content":"The `src/` directory contains 3 files:","turn_id":"t_9f86d081884c7d65"}
{"type":"done","latency":4.2,"turn_id":"t_9f86d081884c7d65"}
```

With streaming enabled (`--stream` / `stream: true` / `ODEK_STREAM=true`) the
answer arrives as `token_delta` / `thinking_delta` fragments as the provider
generates them, and the bulk `token` / final-answer `thinking` re-sends are
suppressed. Providers that reject SSE transparently fall back to the buffered
path — no deltas fire and the bulk events return. See docs/STREAMING.md.

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

### Server stack (`cmd/odek/`)

| Component | File | Purpose |
|-----------|------|---------|
| HTTP server + static | `serve.go` (`handleStatic`) | Serves the embedded UI with strict CSP; injects the per-instance token via the `?token=` URL |
| WebSocket upgrade | `golang.org/x/net/websocket`, wired in `serve.go` | RFC 6455 handshake + framing; `internal/ws/ws.go` holds the shared frame constants |
| WebSocket handler | `serve.go` (`handleWS`) | Per-connection agent lifecycle, connection registry, ping/pong, server `keepalive`, `cancel` / `session_switch` |
| Prompt handler | `serve.go` (`handlePrompt`) | Transport-agnostic (event-sink) prompt path: `@` refs, attachments, audit, per-turn persistence, streaming-suppression logic — shared by the socket and headless REST runs |
| Approvals | `wsapprover.go` | WS approver with friction, class-trust, and a configurable approval timeout |
| Management REST | `serve_api.go` | health, sessions (search/pagination/pin/export), memory (+consolidate), skills (+promote), tools, profiles, config view, MCP listing, shutdown |
| Runs + observability | `serve_runs.go` | headless run engine (`POST /api/prompt`), remote approval bridge, events ring, usage stats, connection registry |
| Resource API | `serve.go` (`handleResourceSearch`) | `@` completion search endpoint |

### WebSocket implementation

- Transport is `golang.org/x/net/websocket`: the RFC 6455 upgrade, frame parsing, and close/ping/pong handling come from the library; `internal/ws/ws.go` defines the shared frame-type constants
- Messages are JSON over text frames; fragmentation details are handled by the library
- Frame writes are serialized per connection (`golang.org/x/net/websocket` is not safe for concurrent sends) and bounded by a 30-second write deadline
- A clean client close surfaces as `io.EOF`; broken connections surface as `net.Error`
- Per-connection agent lifecycle (registry, ping/pong, server `keepalive`, `cancel` / `session_switch`) lives in `serve.go` (`handleWS`)

### Frontend (`cmd/odek/ui/`)

- Vanilla JS + CSS SPA split into native ES modules under `js/` — no build step, no bundler, no CDN. Module map: `main` (init/wire-up) · `ws` (protocol v2 dispatch) · `api` (typed REST client — the single place token headers are attached) · `sessions` (sidebar: search/pagination/pin/export) · `panels` (management drawer) · `plan` (structured-plan panel) · `health` (heartbeat + popover) · `render`/`markdown`/`untrusted` (transcript rendering) · `approvals` · `input` (send, history, `@` completion, attachments) · `state`/`dom`/`utils`/`net`/`escape`
- **Escaping**: all server-controlled strings are inserted escaped (`escapeHtml`/`escapeAttr`/`textContent`); `markdownToHtml` HTML-escapes all input by default and allowlists link schemes — see "Content sanitization contract" above. No inline scripts or handlers anywhere (CSP `script-src 'self'`); generated content uses event delegation
- **Untrusted envelope**: `js/untrusted.js` unwraps the model-facing `<untrusted_content_*>` envelope before display (body shown, source as badge)
- **Design**: self-contained "EMBER" theme — electric amber on layered blue-charcoal surfaces with hairline borders, glass topbar, and ≤200ms micro-interactions. Design tokens are CSS custom properties in `style.css` (`--bg-0…4`, `--amber`, `--line`, spacing/radius/motion scales) with a full light-mode variant and `prefers-reduced-motion` support; the Azeret Mono variable font is self-hosted from `ui/fonts/` so the UI works offline
- **Streaming**: fragments (`token_delta`/`thinking_delta`) and bulk `token` events share one rAF-batched render pipeline
- **DOM budget**: the message list is capped at 80 elements (`MAX_MESSAGES`); older messages are pruned
- **Resilience**: auto-reconnect with exponential backoff (1s doubling to a 30s cap, reset after a stable connection) plus the 20s application heartbeat and the server's 20s `keepalive`
- **Tests**: `node --test cmd/odek/ui/js/` (markdown + untrusted-envelope goldens, and api.js request-shape E2E against a mocked fetch) plus Go-side WebUI E2E (`cmd/odek/webui_e2e_test.go`): asset/header/CSP contract, token injection, JS↔HTML id and JS↔CSS class contracts, and full client journeys (streamed WS run, headless run with the remote-approval bridge, kick, pin/export) through the production mux (`newServeMux` — the same constructor `serveCmd` uses, so tests cannot drift from the real mounting)

## Tips

- **Security sandbox**: `odek serve --addr 127.0.0.1:8080` restricts to localhost. Use a reverse proxy (Caddy, nginx) for remote access. Serve mode enables the Docker sandbox by default — opt out with `--no-sandbox`.
- **Config inheritance**: `odek serve` reads the same config chain (`~/.odek/config.json` → `./odek.json` → env vars) as `odek run`. Set your model, API key, and sandbox settings there.
- **Live streaming**: thinking-default models feel dramatically faster with `--stream` — reasoning and answer render as they generate instead of after the full run.
- **Headless clients**: scripts and TUIs don't need the WebSocket — `POST /api/prompt` + poll `GET /api/runs/{id}`, and answer approvals through `/api/runs/{id}/approvals/{aid}`. The bundled WebUI's *runs* tab uses exactly this surface.
- **Session discovery**: reference any saved session via `@sess:ID` in your prompt to give the agent full context from previous conversations.
