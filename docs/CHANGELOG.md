# Changelog

## v0.36.1 (2026-05-23) — Phase 1.5: Batch Approval Gate

### Parallel Approval Fix
- When the LLM returns **multiple tool calls** in one iteration and an **approver is set**, the engine now shows a **single batch approval prompt** instead of N concurrent inline keyboards
- If denied, all tools are rejected with `"error: batch approval denied"` without executing anything
- If approved, `SetTrustAll(true)` is called on the approver so individual tool-level `PromptCommand` calls auto-pass during that iteration
- Single tool calls (≤1 per iteration) skip the batch gate entirely — no behavior change

### New Method: `SetTrustAll(bool)`
Added to all three approver implementations:
- **`TTYApprover`** — skips `/dev/tty` prompt when enabled
- **`TelegramApprover`** — skips inline keyboard prompt when enabled
- **`wsApprover`** — skips WebSocket approval when enabled

### API: `Config.Approver`
- New `Approver danger.Approver` field on `odek.Config`
- Wired through `odek.New()` → `loop.Engine.SetApprover()`
- Telegram handler passes per-chat `TelegramApprover` to the agent config

### Test Coverage
- 3 batch approval tests: denied, approved, single-tool skip
- All tests pass with `-race`

---

## v0.36.0 (2026-05-23) — Parallel Tool Execution

### Parallel Execution
- When the LLM returns multiple tool calls in one response, tools now execute **concurrently** in goroutines (was: sequential)
- **Bounded semaphore** — at most `max_tool_parallel` goroutines run simultaneously (default: 4)
- I/O-bound tools (read_file, search_files, shell, web_search) benefit most — latency drops from `sum(latencies)` to `max(latency)`
- Configurable via `max_tool_parallel` in config or `ODEK_MAX_TOOL_PARALLEL` env var

### Three-Phase Implementation
1. **Phase 1 (sync)** — fire all `tool_call` events + narrator/rendering so the user sees progress immediately
2. **Phase 2 (parallel)** — N goroutines execute tools concurrently via channel semaphore
3. **Phase 3 (sync)** — drain semaphore, compress large outputs, append results in **original call order**

### Config
- `MaxToolParallel int` on `loop.Engine` and `odek.Config` (0 = default 4)
- `max_tool_parallel` in FileConfig (`internal/config/loader.go`)
- Wired through CLI, Telegram, and serve entry points

### Test Coverage
- 6 parallelism tests: latency (4×100ms → ~100ms vs 400ms), ordering, semaphore cap (6 tools, cap=2), default cap, error resilience, single tool
- All tests pass with `-race`

---

## v0.35.1 (2026-05-23) — secrets.env Auto-Load + File Attachments

### Secrets Management
- **`~/.odek/secrets.env` auto-loaded** as Layer 0 in the config priority chain — parsed before any config file or env var lookup
- No more plaintext secrets in `config.json` — use `"api_key": "${ODEK_API_KEY}"` with the value in `secrets.env`
- Supports `KEY=VALUE` lines, `#` comments, blank lines, and does NOT overwrite existing env vars
- Missing/unreadable file is silently ignored

### Telegram File Attachments
- **`sendMedia`** now falls back to `SendDocument` for unknown media types (zip, csv, pdf, etc.)
- **System prompt** now explicitly instructs the agent about file attachment:
  - `send_message` tool with `file` parameter for intermediate replies
  - `MEDIA:document:/path` in final answers for native file delivery

### Domain Migration
- All `kode.21no.de` → `odek.21no.de` references (defaultSystem, Quick Facts, RuntimeContext)

---

## v0.33.2 (2026-05-23) — Narrator Integration Complete

### Telegram Engaging Mode
- **Instant progress** — sends an immediate "🤔 Looking into that..." message when the agent starts
- **Live tool narration** — updates the progress message with emoji-rich descriptions on each tool call
- **Clean chat** — deletes the progress message when the final answer arrives

### Test Coverage
- InteractionMode config tests: default, `ODEK_INTERACTION_MODE`, CLI override
- `/mode` command test: verifies interaction_mode documentation

---

## v0.33.1 (2026-05-23) — InteractionMode & Narrator

### New Feature: InteractionMode
- `interaction_mode` config field: `"engaging"` (default) or `"verbose"`
- **Engaging mode** — LLM/narrator-powered emoji-rich progress messages instead of raw tool call output
- **Verbose mode** — traditional raw tool names, args, and results (existing behavior)
- `ODEK_INTERACTION_MODE` env var and `--interaction-mode` CLI flag

### New Package: `internal/narrate`
- Template-based tool narration with emojis (📖 Reading, ✏️ Editing, 🔍 Searching, etc.)
- `narrate.New(enabled)` constructor — zero deps, zero LLM calls
- 4 tests, offline fallbacks for all built-in tools

### Integration Points
- CLI (`--interaction-mode` in run, repl, serve, telegram subcommands)
- ReAct loop (loop.go) — narrator wired into tool execution and thinking phases
- Renderer — `NarratorMessage()` for terminal output
- `NewAgent()` — narrator wired based on `InteractionMode`
- `/mode` command — documents `interaction_mode` options
- Config default-overlay: unset defaults to `"engaging"`

---

## v0.33.0 (2026-05-23) — Performance Release

Six performance improvements across the stack, reducing latency per session by **~30-50%**.

### Connection Pooling
- **LLM client** now reuses TCP/TLS connections across API calls (was: new handshake per request)
- **Telegram bot** uses the same pooled transport for polling and API requests
- Saves ~200-500ms per HTTP call — ~6-15s on a typical 30-call agent session

### Context Trimmer O(n²) → O(n)
- `trimContext` now tracks a running token total instead of re-scanning all messages after every group drop
- For large conversations near the context limit: 1,770 message scans → ~60

### Session Compact JSON
- Session files are now written with `json.Marshal` (compact) instead of `json.MarshalIndent` (pretty-printed)
- ~5% smaller on disk, faster serialization — 410KB → 420KB for a Telegram session

### Memory: LLM Search Disabled by Default
- Episode search now uses RandomProjections (go-vector) by default instead of LLM ranking
- Zero LLM API calls per turn for memory search (was: 1 call per loop iteration)
- Set `llm_search: true` in config to restore LLM-based ranking

### Persistent Skill Cache
- Parsed skills are cached to `~/.odek/skills/.skills_cache.json` across `odek run` invocations
- ~30ms saved per cold start — 152 `stat()` + YAML parses → single cache read + unmarshal
- Auto-invalidated on skill mutations or format version changes

### Episode Index Cache
- Episode index (`index.json`) is cached in memory and invalidated after writes
- Avoids disk I/O + JSON unmarshal on every `FormatEpisodeContext` call
- Saves ~5ms per loop iteration across a session

---

## v0.32.x

See git log for earlier releases.
