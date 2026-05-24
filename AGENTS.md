# odek — Agent Maintenance Guide

This file is automatically loaded by odek when running inside this repository.
It provides context about the project's architecture, conventions, and how to update/maintain it.

---

## Project Identity

- **Package:** `odek` (Go module: `github.com/BackendStack21/odek`)
- **What it is:** Minimal Go autonomous agent runtime — ReAct (Reasoning + Acting) loop with zero frameworks (stdlib + a few focused packages).
- **Binary:** `odek` — single static binary, ~12 MB, instant startup.
- **Config:** Five-layer priority: `~/.odek/secrets.env` → `~/.odek/config.json` → `./odek.json` → `ODEK_*` env vars → CLI flags.
- **Benchmark:** AIEB v2.0 — 80.3% (highest published agent score on the Autonomous Intelligence Engineering Benchmark).
- **Version:** v0.42.1 — see latest tag at https://github.com/BackendStack21/odek/releases

## Source Layout

```
odek.go                       Public API (Config, New, Run, Close, ModelProfile, KnownProfiles, Tool interface)
odek_test.go                  Tests for public API
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox setup
  shell.go                    Built-in shell tool (local or docker exec; danger-gated)
  serve.go                    Web UI server (HTTP + WebSocket; @-resource completion)
  repl.go                     Interactive REPL with multi-turn session support
  repl_editor.go              REPL inline editor helpers
  refs.go                     @-reference resolver (files, sessions)
  telegram.go                 Telegram bot command — wires odek agent into Telegram poller
  subagent.go                 Sub-agent command (--goal, --context, --task)
  subagent_tool.go            delegate_tasks built-in tool (sub-agent spawning)
  browser_tool.go             Built-in browser tool (HTTP fetch + headless navigation)
  file_tool.go                Built-in file tools (read_file, write_file, search_files, patch)
  perf_tools.go               Performance/parallelism tools (batch_read, batch_patch, math_eval, diff, multi_grep, etc.)
  mcp.go                      MCP server implementation (stdio + SSE transport)
  wsapprover.go               WebSocket-based approval bridge
  ui/index.html               Single-page Web UI (~770 LOC, vanilla JS + CSS)
  *_test.go                   CLI, subagent, contract, E2E, and integration tests
internal/
  auth/                       Interactive approval system (confirm/cancel for dangerous tools)
  config/loader.go            Config file loading, env vars, secrets.env, priority merge
  danger/classifier.go        Command/URL classification for security gating
  display/display.go          ANSI terminal output helpers
  llm/client.go               OpenAI-compatible HTTP client with reasoning_content support
  loop/loop.go                ReAct engine: observe → think → parallel-act → repeat
  mcp/server.go               MCP server handler (tools/list, tools/call, SSE streaming)
  mcpclient/client.go         MCP client (connect to external MCP servers)
  memory/                     MemoryManager (facts, buffer, episodes, merge, scan, LLM search)
  narrate/narrate.go          Narrator — LLM-powered emoji-rich progress messages for engaging/enhance mode
  redact/redact.go            Secret redaction (13-pattern scanner: API keys, tokens, credentials)
  render/render.go            Terminal output with model label, color, narrator message support
  resource/resource.go        @-reference resolver for file/session completion
  sandbox/manager.go          Docker sandbox lifecycle (start, exec, stop, cleanup)
  session/session.go          Session store (CRUD, trim, cleanup, compact JSON)
  skills/                     Skill system (types, loader, triggers, self-improve, curator, import)
  telegram/                   Telegram bot: bot.go, poller.go, handler.go, commands.go, session.go, plan.go, download.go
  tool/registry.go            Thread-safe tool registry, clarify.go
  transport/http.go           Shared HTTP transport with connection pooling
  ws/ws.go                    RFC 6455 WebSocket framing (~200 LOC)
docs/                         Documentation (CLI, API, CONFIG, MCP, MEMORY, TELEGRAM, etc.)
benchmark/                    AIEB v2.0 benchmark suite (9 tasks, 4 tiers, automated scoring)
```

## How It Works

### 1. Agent Loop (`internal/loop/loop.go`)
ReAct cycle: observe → think → act → repeat.
- LLM returns tool calls or a final answer.
- **Parallel tool execution** — multiple independent tool calls run concurrently with bounded concurrency (`max_tool_parallel`, default: 4).
- **Batch approval gate** — when multiple tools need approval, all risky tools are shown at once in a single inline keyboard.
- **Interaction modes** — three modes control tool-call display:
  - `engaging` — LLM-narrated emoji-rich progress (default)
  - `enhance` — like engaging but narration persists after response
  - `verbose` — raw tool names, args, and results (debug-friendly)
- **Per-tool trace messages** — each tool call generates a reasoning-backed trace in Telegram.
- Max 90 iterations by default (`--max-iter`).

### 2. Tools

**Core** (always available):
`read_file`, `write_file`, `search_files`, `patch`, `shell`, `browser`, `memory`, `clarify`, `delegate_tasks`, `send_message`

**Performance/parallelism** (added v0.38-v0.40):
| Category | Tools |
|----------|-------|
| Parallel batch | `batch_read` — N files in 1 call, `batch_patch` — N edits atomically, `parallel_shell` — N commands true-parallel, `http_batch` — N URLs parallel fetch |
| Zero-fork data | `math_eval` — native arithmetic, `diff` — LCS diff, `json_query` — dot-path JSON, `tr` — text transform, `base64` — encode/decode |
| File analysis | `glob` — fast glob find, `file_info` — stat metadata, `count_lines` — streaming line count, `word_count` — streaming word count, `checksum` — SHA256/SHA1/MD5, `sort` — sort lines, `head_tail` — first/last N lines |
| Multi-pattern | `multi_grep` — N regex patterns parallel, `tree` — structured directory tree |
| Audio | `transcribe` — local whisper.cpp audio transcription with auto-OGG→WAV conversion via ffmpeg |

All gated by the `danger` security classifier with three actions: allow, deny, prompt.
- `shell`: Classifies commands into risk classes (safe, local_write, system_write, destructive, network_egress, code_execution, install, blocked).
- `send_message`: Sends text/photo/document/voice to the Telegram chat with inline keyboard support.
- All file tools: `O_NOFOLLOW` on opens, `danger.ClassifyPath` per path, atomic temp+rename for writes.
- All network tools: `danger.ClassifyURL` per URL, configurable network egress gate.

### 3. Skills
Trigger-matched `SKILL.md` files loaded on-demand via lazy injection. Auto-learns from patterns every session.
- Stored in `~/.odek/skills/` (user) and `skills/` (project).
- `odek skill curate` analyzes quality, staleness, and trigger overlap.
- Skills with `auto_load: true` are injected as passive reference; `auto_load: false` + strong triggers → lazy injection as system message (stronger signal).

### 4. Memory (`internal/memory/`)
Three-tier persistence:
- **Facts** — Markdown files on disk, merge-on-write via go-vector cosine similarity (~80% fewer LLM calls).
- **Session buffer** — Full conversation history.
- **Episodes** — Searchable summaries extracted by LLM on session end.
- go-vector RP is ephemeral — rebuilt from text on every write, no embedding state to persist.

### 5. Config System (`internal/config/loader.go`)
Five-layer priority chain:
```
0. ~/.odek/secrets.env     ← Auto-loaded into process environment
1. ~/.odek/config.json     ← Global defaults
2. ./odek.json             ← Project overrides
3. ODEK_* env vars         ← Runtime overrides
4. CLI flags               ← Explicit invocation (highest priority)
```
- `${VAR}` substitution in JSON config files.
- `interaction_mode` field: `engaging` | `verbose` | `enhance`.
- `max_tool_parallel`: bounded concurrency for tool execution.
- `--deliver` CLI flag delivers agent response to Telegram default chat (for cron).

### 6. MCP (Two-Way)
- **Server:** `odek mcp` exposes native tools via stdio or SSE to any MCP client (Claude Code, Cursor).
- **Client:** Connects to external MCP servers (Playwright, Fetch, databases) via `mcp_servers` config.

### 7. Telegram Bot (`cmd/odek/telegram.go` + `internal/telegram/`)
Full-featured bot with long-polling:
- Slash commands: `/start`, `/new`, `/mode`, `/plan`, `/status`, `/cancel`, `/reset`, `/budget`
- Interaction mode-aware progress display (verbose trace edits or engaging narration)
- Voice message transcription, photo analysis, file attachments
- Inline keyboard for approvals, clarifications, and cancel
- `send_message` tool — agent can send structured messages, files, and keyboards back to chat
- `--deliver` flag delivers final response to configured default chat (cron integration)
- Per-chat session management with TTL-based cleanup

### 8. Web UI
`odek serve` — single-page browser interface with WebSocket streaming, `@` resource completion, token economics display, drag-and-drop file attachments, inline loading. Built from Go's embed — zero npm.

### 9. Dynamic Model Discovery
On startup, odek queries the LLM provider's `GET /models` endpoint and auto-discovers model capabilities (max context, thinking support, parallel tool support). This replaces hardcoded model profiles — the agent adapts to whatever the provider exposes.

### 10. Secret Redaction (`internal/redact/`)
Active at two layers:
- Tool outputs are redacted before the LLM sees them (ReAct loop).
- Session files are sanitized on save (defense-in-depth).
- 13-pattern scanner covers OpenAI, GitHub, AWS, JWT, private keys, Slack, Stripe, Google, Twilio, generic API keys, Auth headers, and env var credentials.

## Key Conventions

- **No external frameworks.** Stdlib + four focused packages (go-vector, yaml.v3 for MCP, goja for JS, chroma for highlight).
- **Single binary.** Everything compiles into one static executable (~12 MB).
- **Tests live alongside code.** `*_test.go` files in the same package, never in a separate `test/` directory.
- **Test data** uses `t.TempDir()` for isolation.
- **CLI commands** follow the pattern: `cobra.Command` with `RunE` handler. Flag parsing is in `main.go`.
- **New config fields** require 11-point wiring: FileConfig → CLIFlags → ResolvedConfig → loadFile → overlayFile → env var → CLI flag → resolved mapping → flag parsing → pass-through → call sites. Missing any step = silent failure.
- **Build before test.** Always `go build ./...` first to catch compile errors before running tests.
- **Use `odek run` for analysis.** Prefer delegating complex code questions to odek itself (Pattern 2 in odek-file-qa skill).
- **The odek Tool interface** is `Call(args string) (string, error)` and `Schema() any` — NOT `Execute()` or `json.RawMessage`.
- **MarkdownV2 reserved chars** must be escaped in Telegram messages. The escape function handles `_*[]()~>#+-=|{}.!`.
- **Inline keyboard buttons** use `*telegram.InlineKeyboardMarkup`, not raw `map[string]any`.
- **Skill auto_load: false** with strong trigger keywords is STRONGER than `auto_load: true` — lazy injection as a system message right before the user message beats passive reference injection.
- **Patch old_string must be unique** in the file. For repeated patterns (class="card"), include enough surrounding context (e.g., the card's inner content).
- **After modifying shared files in parallel subagents**, always run `go build ./... && go test ./...` to catch integration errors. Subagent patch conflicts produce `Could not find a match for old_string` warnings.

## Testing

```bash
# Full test suite
go test ./... -count=1

# With race detector (recommended for loop/tool/telegram changes)
go test -race ./... -count=1

# Specific package with verbose output
go test -v ./internal/loop/ -run TestTrimContext -count=1

# Benchmark
go test -bench=. -benchmem ./internal/loop/

# Run integration/E2E tests (require Docker and/or network)
go test -v -run "TestTelegram|TestMCP" ./cmd/odek/ -count=1
```

### Test Conventions
- Coverage targets: 70%+ for new packages, no regression on existing.
- `httptest.Server` for mocking HTTP endpoints (LLM, Telegram, MCP).
- `t.Setenv` for environment variable overrides (no global env mutation).
- Parallel tests must use `-race` to catch data races on shared slices.
- `timedTool` pattern for testing parallel execution with configurable delays.
- Secret redaction tests must use runtime string concatenation to avoid triggering GitHub's push-time secret scanner.

## Common Pitfalls

- **Session files are JSON in `~/.odek/sessions/`.** Corrupt data is handled gracefully with fallback scan.
- **The Telegram bot uses long-polling (no webhooks),** built on stdlib `net/http`.
- **Background subprocesses don't inherit `ODEK_API_KEY`.** When running odek as a subprocess, the spawned process may not have the key. Pass it explicitly or save to `/tmp/.aieb_odek_key`.
- **`go build .` ≠ `go build ./cmd/odek`.** The bare `.` builds the library package (an ar archive, not an executable).
- **Config partial overlay + Go zero values = missing defaults.** Always start from `DefaultConfig()` and overlay only non-zero user fields. Sentinel values or pointer fields are needed to distinguish "unset" from "explicitly zero."
- **`delegate_tasks` sub-agents have a 120s default timeout.** Override via `subagent.timeout_seconds` config.
- **Parallel tool tests** use `timedTool` with configurable delays — always run with `-race` to catch data races on the results slice.
- **Batch approval gate** checks `e.approver != nil && len(result.ToolCalls) > 1` — single-tool responses skip the gate. When adding a new approver, implement `SetTrustAll(bool)` to benefit from batch trust cascade.
- **SetTrustAll is bounded by `defer`** — it's enabled before Phase 2 and disabled when `runLoop` returns. The mockApprover's `trustAll` field will be `false` after `Run()` returns.
- **Approval callbacks must route BEFORE `OnCallbackQuery`.** If it falls through to the generic handler, the approver's channel never receives the response → 120s timeout deadlock.
- **Async dispatch prevents update loop deadlock.** Agent loop processing MUST run in a goroutine from the callback handler.
- **`DownloadFile` hardcodes the production API URL.** Add `FileBaseURL` to Bot for testability; both `BaseURL` and `FileBaseURL` must be overridden in tests.
- **Review every interface path for goroutine safety** after writing a new subsystem (Telegram, approvals, parallel tools). Checklist: mutable shared state, mutex protection, per-chat state isolation, `httptest.Server` URL configurability.
- **`reasoning_content` echo for DeepSeek models.** If the API returns `reasoning_content`, it must be echoed back in subsequent assistant messages or the next call fails with `400 Bad Request`.
- **`frame-ancestors` in `<meta>` produces a browser warning.** CSP meta tags ignore this directive — set it via HTTP headers or accept the cosmetic warning.
- **Rebuild the binary after source changes.** `go build -o /usr/local/bin/odek ./cmd/odek`. Source changes don't take effect until rebuilt.

## Release Checklist

1. `go build ./...` — compile check
2. `go test ./... -count=1` — full test suite
3. `go test -race ./... -count=1` — race detection
4. Update `docs/CHANGELOG.md` with notable changes
5. Commit and push to `main`
6. Tag: `git tag -a vX.Y.Z -m "vX.Y.Z: short description"`
7. Push tag: `git push origin vX.Y.Z`
8. Create GitHub release: `gh release create vX.Y.Z --title "vX.Y.Z" --notes "..."`
9. Restart Telegram bot: `nohup bash build-and-restart-telegram.sh --restart-only > /tmp/odek-restart.log 2>&1 &`
10. Verify: `sleep 3 && ps aux | grep "odek telegram"`

## Documentation Files

| File | Covers |
|------|--------|
| `docs/CLI.md` | All commands, flags, file attachments, @-references |
| `docs/CONFIG.md` | Five-layer priority, secrets.env, all config fields |
| `docs/API.md` | Go SDK: Agent, Tools, memory, multi-turn, custom tools |
| `docs/TELEGRAM.md` | Bot architecture, config, API client, session management |
| `docs/MEMORY.md` | Three-tier memory, episodes, merge-on-write |
| `docs/MCP.md` | Two-way MCP (server + client), SSE transport |
| `docs/SANDBOXING.md` | Docker isolation, config, security model |
| `docs/SECURITY.md` | Prompt injection defense, danger classifier, redaction |
| `docs/LEARNING.md` | Skill auto-learning, curation, trigger system |
| `docs/SESSIONS.md` | Save, resume, list, trim, cleanup |
| `docs/SUBAGENTS.md` | delegate_tasks architecture, parallel sub-agents |
| `docs/PROVIDERS.md` | Model profiles, provider configs, model discovery |
| `docs/WEBUI.md` | Web UI architecture, WebSocket streaming |
| `docs/CACHING.md` | Response caching, cache control |
| `docs/CHEATSHEET.md` | Quick reference: commands, config, memory, env vars |
| `docs/DEVELOPMENT.md` | Building, testing, contributing |
| `docs/DAILY-WORKER.md` | Cron/daily worker integration patterns |
| `AGENTS.md` | This file — agent auto-load context |
