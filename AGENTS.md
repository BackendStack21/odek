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
- **Version:** v0.58.8 — see latest tag at https://github.com/BackendStack21/odek/releases

## Source Layout

```
odek.go                       Public API (Config, New, Run, Close, ModelProfile, KnownProfiles, Tool interface)
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox setup, system prompt
  shell.go                    Built-in shell tool (local or docker exec; danger-gated)
  serve.go                    Web UI server (HTTP + WebSocket; @-resource completion)
  repl.go                     Interactive REPL with multi-turn session support
  repl_editor.go              Terminal raw-mode input editor
  telegram.go                 Telegram bot command — wires odek agent into Telegram poller
  subagent.go                 Sub-agent command (--goal, --context, --task)
  subagent_tool.go            delegate_tasks built-in tool (sub-agent spawning)
  browser_tool.go             Built-in browser tool (HTTP fetch + headless navigation)
  file_tool.go                Built-in file tools (read_file, write_file, search_files, patch, batch_read, glob, file_info)
  perf_tools.go               Performance/parallelism tools (batch_patch, parallel_shell, http_batch, math_eval, diff, count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count)
  mcp.go                      MCP server implementation (stdio + SSE transport)
  transcribe_tool.go          Whisper.cpp audio transcription
  session_search_tool.go      Session search tool
  wsapprover.go               WebSocket interactive approval relay (with friction + class-trust gates)
  refs.go                     @-resource reference resolution (files, sessions)
  untrusted.go                <untrusted_content_<nonce>> wrapper + per-call ingest recorder
  audit.go                    Per-turn audit + `odek audit` subcommand (divergence heuristic)
  subagent_key.go             FD-based API key handoff (parent → sub-agent, never via env)
  skill_promote.go            `odek skill promote` — clear NeedsReview on a tainted skill
  security_report_validation_test.go  Regression bar for every documented mitigation
  *_test.go                   200+ unit + E2E tests covering all tools
internal/
  llm/                        OpenAI-compatible HTTP client with reasoning_content support
  loop/                       ReAct engine: observe → think → parallel-act → repeat. signal.go — SignalEvent observability (context_trimmed, tool_recovery).
  tool/                       Thread-safe tool registry, clarify.go, send_message.go
  danger/                     Command/URL classification + bypass-resistant tokenizer (substitution, $IFS, wrappers, basenames). TTYApprover with friction mode.
  auth/                       Interactive approval system
  memory/                     MemoryManager (facts, buffer, episodes, merge, scan). EpisodeProvenance — tainted episodes never auto-replayed. notifier.go — MemoryEvent lifecycle observability (fact/episode events fan out to terminal/WebUI/Telegram/programmatic handler).
  session/                    Session store (CRUD, trim, cleanup, compact JSON). AuditStore + divergence heuristic.
  skills/                     Skill system (types, loader, triggers, self-improve, curator, import, cache). SkillProvenance gate — NeedsReview skills pinned to Lazy.
  config/                     Config file loading, env vars, secrets.env, priority merge
  telegram/                   Telegram bot: bot.go, poller.go, handler.go, commands.go, session.go
  render/                     Terminal output and narrator support
  narrate/                    LLM-powered emoji-rich progress messages
  redact/                     Secret redaction (13-pattern scanner)
  mcp/                        MCP server handler (tools/list, tools/call, SSE streaming)
  mcpclient/                  MCP client (connect to external MCP servers)
  sandbox/                    Docker sandbox lifecycle
  transport/                  Shared HTTP transport with connection pooling
  ws/                         RFC 6455 WebSocket framing
docs/                         Documentation (CLI, API, CONFIG, MCP, MEMORY, TELEGRAM, etc.)
benchmark/                    AIEB v2.0 benchmark suite (9 tasks, 4 tiers, automated scoring)
```

## How It Works

### Agent Loop (`internal/loop/loop.go`)
ReAct cycle: observe → think → act → repeat.
- LLM returns tool calls or a final answer.
- **Parallel tool execution** — multiple independent tool calls run concurrently (max_tool_parallel, default: 4).
- **Batch approval gate** — multiple risky tools shown at once in a single prompt.
- **Tool-failure recovery** (v0.53.0) — systematic recovery from tool call failures: retry transient errors, skip permanently failed tools, and continue the loop without crashing.
- **Context-limit protection** (v0.55.0) — trimToSurvival drops oldest messages when approaching the model's context window, keeping the agent functional under extended sessions. Fixed ordering bug in v0.56.2 (tool messages now stay grouped with their parent assistant message).
- **Interaction modes** — engaging (narrated), enhance (persistent), verbose (raw), off.
- Max 300 iterations by default.
- **Post-response async processing** (v0.56.0) — skill learning and episode extraction run in background goroutines, eliminating the 2-5 second hang after every `odek run`.
- **Artifact-aware file search** (v0.57.0) — `search_files` and `multi_grep` skip build/artifact directories (`node_modules`, `vendor`, `.git`, `__pycache__`, `.venv`, etc.) automatically, reducing noise and speeding scans.
- **Semantic session search** (v0.58.0) — the `session_search` tool uses go-vector RandomProjections + k-NN for semantic similarity search through session content. Finds relevant past conversations even when keywords don't match. Features a two-tier pipeline: vector index (fast, ~1ms) → deepSearch fallback (exhaustive, slower).

### Tools
All built-in tools with zero subprocess forks: batch_read, batch_patch, parallel_shell, http_batch, math_eval, diff, count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count, transcribe, browser, read_file, write_file, search_files, patch, shell, delegate_tasks, session_search.

### Terminal Rendering (`internal/render/`)
v0.56.2: Vertical space compression — `Start()` is now a no-op; blank lines removed from Iteration/FinalAnswer/Summary. Raw-mode cursor uses `\r\n` instead of bare `\n` for cross-platform compatibility.

### Identity
System prompt is loaded by priority: `--system` flag > `~/.odek/IDENTITY.md` > compiled-in defaultSystem. The default is a concise identity focused on TDD workflow, tool discipline, and safety rules.

### Security Architecture
Layered prompt-injection / approval-fatigue defenses. Full reference: [docs/SECURITY.md](docs/SECURITY.md).

- **Untrusted-content wrapper** (`cmd/odek/untrusted.go`) — every tool whose output sources from outside the trust boundary (`browser`, `read_file`, `shell`, `search_files`, `multi_grep`, `transcribe`, `head_tail`, `diff`, `tr`, `sort`, `json_query`, `batch_patch`, `glob`, `file_info`, `tree`, `base64` file mode, `session_search`, `@-resources`, `--ctx` files, any MCP tool) wraps results in `<untrusted_content_<nonce> source="...">…</untrusted_content_<nonce>>`. Browser page title and interactive-element text are wrapped in addition to the main content. Per-call nonce defeats wrapper-escape via literal close tag.
- **Audit log** (`cmd/odek/audit.go` + `internal/session/audit.go`) — every `wrapUntrusted` call records source + content-hash + turn into `<sessions>/audit/<id>.json`. After each turn a divergence heuristic flags `suspicious_divergence=true` when the agent ingested untrusted content AND its actions or final response reference resources that either did not appear in the user's message or were introduced by the untrusted content itself (closing response-only exfiltration and reused-resource injection bypasses). Inspect with `odek audit <session-id>` / `odek audit --list`.
- **Memory taint** (`internal/memory/provenance.go`) — `EpisodeProvenance` tracks Untrusted/Sources/UserApproved. Tainted episodes are stored but `Search()` filters them out, so a one-shot injection cannot persist via the episode pipeline. User must explicitly promote.
- **Skill provenance gate** (`internal/skills/loader.go` + `cache.go`) — `Skill.Provenance{Untrusted, Sources, NeedsReview}`. NeedsReview skills pin to Lazy regardless of `auto_load`. `odek skill promote <name>` clears the flag after user review.
- **Sub-agent damage cap** (`cmd/odek/subagent.go::applySubagentTrust`) — `delegate_tasks` carries `trust_level` + `max_risk`. Untrusted ⇒ NonInteractive=deny, Destructive/CodeExec/Install/SystemWrite/NetworkEgress all forced to Deny. `max_risk` ⇒ everything above cap forced to Deny.
- **FD-based API key handoff** (`cmd/odek/subagent_key.go`) — parent writes key to a 0600 tempfile, immediately `unlink()`s, passes the FD via `cmd.ExtraFiles`. Sub-agent reads from `$ODEK_API_KEY_FD` and closes. Key never in `/proc/<pid>/environ`.
- **Approver friction** (`internal/danger/approver.go`, `cmd/odek/wsapprover.go`) — both TTYApprover and WSApprover engage friction mode after 3 approvals of the same class in 60s: require typing literal `approve`, 1.5s pause. Trust-class shortcut disabled for `destructive` + `blocked` regardless.
- **Danger classifier bypass resistance** (`internal/danger/classifier.go`) — `normalize()` pre-processes: expand `$IFS` / `${IFS}`, extract `$(...)` / `` `...` `` substitutions, strip `command` / `exec` / `builtin` wrappers, collapse unquoted backslashes, basename absolute paths. Regression suite in `classifier_bypass_test.go`.
- **WS Origin allowlist** (`cmd/odek/serve.go::checkLocalOrigin`) — rejects non-localhost upgrades. Closes CSRF-on-localhost.
- **REST API CSRF protection** (`cmd/odek/serve.go::requireLocalOrigin`) — state-changing HTTP endpoints (POST/PUT/PATCH/DELETE) require a localhost origin or no Origin header, and static responses set `X-Frame-Options: DENY` + `Content-Security-Policy: frame-ancestors 'none'` to block clickjacking.
- **Browser history cap** (`cmd/odek/browser_tool.go`) — navigation history is capped at 50 snapshots to prevent memory DoS from repeated `browser_navigate` calls.
- **Browser element cap** (`cmd/odek/browser_tool.go`) — the number of interactive elements extracted per page is capped at 500 so a hostile page cannot OOM the agent with thousands of links or buttons.
- **Search result bounds** (`cmd/odek/file_tool.go`, `cmd/odek/perf_tools.go`) — `search_files` and `multi_grep` enforce a max match limit (500) and a total returned-content cap (1 MiB) to avoid unbounded result JSON.
- **Perf-tool file-size cap** (`cmd/odek/perf_tools.go`) — `diff`, `base64`, `tr`, `sort`, `json_query`, and `batch_patch` reject files larger than 10 MiB to avoid loading multi-gigabyte files into memory.
- **Shell output cap** (`cmd/odek/shell.go`, `cmd/odek/perf_tools.go`) — `shell` and `parallel_shell` cap captured stdout/stderr at 1 MiB per stream to prevent memory DoS from commands that dump huge files.
- **Browser request timeout** (`cmd/odek/browser_tool.go`) — the browser HTTP client enforces a 30-second request timeout so a slow/malicious server cannot hang the agent turn.
- **Transcribe input/output guard** (`cmd/odek/transcribe_tool.go`) — rejects audio files larger than 10 MiB, caps whisper stdout at 10 MiB, and writes ffmpeg output to a temp file so it cannot clobber an existing `.wav` next to the source path.
- **Tree width cap** (`cmd/odek/perf_tools.go`) — the `tree` tool limits each directory listing to 1,000 entries to avoid OOM from directories with millions of files.
- **patch tool hardening** (`cmd/odek/file_tool.go`) — `patch` rejects files larger than 10 MiB and preserves the original file mode instead of resetting it to 0644.
- **glob tool hardening** (`cmd/odek/file_tool.go`) — `glob` caps results at 1,000 matches and wraps returned paths as untrusted content.
- **Sub-agent task-file cap** (`cmd/odek/subagent.go`) — `odek subagent --task <file>` rejects task files larger than 10 MiB before loading them into memory.
- **session_search hardening** (`cmd/odek/session_search_tool.go`) — the `get` action returns at most the 100 most recent messages and wraps each message content, task, and buffer entry as untrusted; `list`/`search`/`find` also wrap session tasks.
- **@-resource / --ctx prompt wrapping** (`cmd/odek/refs.go`, `cmd/odek/serve.go`) — content resolved from `@file` references and `--ctx` files is wrapped as untrusted before being inserted into the prompt.
- **Config file size cap** (`internal/config/loader.go`) — `~/.odek/config.json` and `./odek.json` are rejected if larger than 5 MiB to prevent OOM from a malicious or broken config at startup.
- **Resource resolver size cap** (`internal/resource/resource.go`) — `@-resource` file loads are capped at 1 MiB to prevent OOM from `@hugefile` references.
- **Resource resolver symlink hardening** (`internal/resource/resource.go`) — `FileResolver.Search` uses `os.Lstat` (not `os.Stat`) for search-result metadata, so symlinks cannot leak the size of arbitrary targets outside the workspace.
- **Sub-agent summary cap + wrapping** (`cmd/odek/subagent_tool.go`) — each sub-agent result included in the `delegate_tasks` summary is truncated to 100 KiB to prevent memory DoS, and the final aggregated summary is wrapped as untrusted content so a compromised sub-agent cannot inject instructions into the parent context.
- **Tree path wrapping** (`cmd/odek/perf_tools.go`) — the `tree` tool wraps every filesystem-derived path as untrusted content.
- **head_tail output cap** (`cmd/odek/perf_tools.go`) — `head_tail` truncates returned lines so total content stays within 1 MiB, preventing multi-file/multi-line memory DoS.
- **search_files symlink hardening** (`cmd/odek/file_tool.go`) — the `files` target uses `Lstat` (not `Stat`) and skips symlinks in the glob branch, closing metadata disclosure via symlinked paths.
- **AGENTS.md size cap** (`odek.go`) — project-level `AGENTS.md` is ignored if larger than 256 KiB to prevent OOM/prompt stuffing from a malicious repo.
- **IDENTITY.md size cap** (`cmd/odek/main.go`) — `~/.odek/IDENTITY.md` is ignored if larger than 256 KiB, falling back to the default identity.
- **patch / batch_patch output expansion cap** (`cmd/odek/file_tool.go`, `cmd/odek/perf_tools.go`) — the post-replacement result is capped at 10 MiB so `ReplaceAll` cannot explode memory.
- **write_file content cap** (`cmd/odek/file_tool.go`) — the `content` argument is capped at 1 MiB to prevent disk exhaustion and memory pressure from a single enormous tool call.
- **file_info confinement + wrapping** (`cmd/odek/file_tool.go`) — `file_info` respects the same `restrictToCWD` path confinement as `write_file`/`patch`, and the returned path is wrapped as untrusted content.
- **WebSocket message-size cap** (`cmd/odek/serve.go`) — `odek serve` sets `MaxPayloadBytes` on every WebSocket connection so a local client cannot OOM the server with a huge frame.
- **Session file size cap** (`internal/session/session.go`) — session files larger than 32 MiB are rejected by `Load()` to prevent OOM from tampered or corrupted transcripts.
- **Skill file size cap** (`internal/skills/loader.go`) — `SKILL.md` files larger than 1 MiB are skipped so a malicious project cannot OOM the process at startup or bloat the system prompt.
- **Serve sandbox default-on** — `odek serve` enables `--sandbox` automatically unless `--no-sandbox` is passed.
- **Sandbox volume confinement** (`internal/sandbox/sandbox.go`) — extra `--sandbox-volume` host paths must resolve to a location under the working directory, cannot contain `..` or symlink escapes, and cannot match sensitive prefixes such as `/etc`, `/proc`, `/sys`, `/dev`, `/root`, `/home`, `/var`, `/run`, or `/var/run/docker.sock`.
- **Sandbox read-only enforcement** (`cmd/odek/sandbox_file.go` + `cmd/odek/file_tool.go` + `cmd/odek/perf_tools.go`) — when a sandbox container is active, `write_file`, `patch`, and `batch_patch` translate host paths to `/workspace/...` and copy data into the container with `docker cp`, so a read-only workspace mount (`--sandbox-readonly`) is enforced for the agent's own file tools.
- **Project config sensitive-field rejection** (`internal/config/loader.go`) — `./odek.json` is untrusted, so `base_url`, `api_key`, `system`, and the `dangerous` section set there are ignored (with stderr warnings). These can only be configured from operator-controlled sources: `~/.odek/config.json`, `ODEK_*` env vars, or CLI flags.
- **MCP subprocess environment sanitisation** (`internal/mcpclient/client.go`) — MCP server children receive only a minimal allowlist of safe environment variables plus explicit `env` overrides. Keys matching secret patterns (`*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, etc.) are stripped, preventing a compromised or malicious MCP server from reading parent secrets.
- **Telegram photo caption wrapping** (`cmd/odek/telegram.go`) — photo captions cross the Telegram trust boundary, so they are wrapped as untrusted content both when passed to the local vision model and when injected into the main agent's user message.
- **Secret redaction** (`internal/redact/redact.go`) — 20+ patterns: OpenAI, Anthropic, GitHub PAT, AWS, PEM, JWT, Vault, Google OAuth, SendGrid, Discord, DB URLs, etc.

### Platform Support
CLI, REPL, Web UI, Telegram bot — all in a single binary.

## Testing

```bash
# All unit tests
go test ./... -count=1

# Race detector
go test -race ./... -count=1

# E2E tests (builds odek binary, tests real subprocess spawning)
ODEK_E2E=true go test -v -count=1 ./cmd/odek/ -run "TestE2E_"

# MCP E2E tests (builds fakeserver from source at test time)
ODEK_E2E=true go test -v -count=1 ./cmd/odek/ -run "TestMCPE2E_"

# Sandbox integration tests (requires Docker)
go test -v -count=1 ./cmd/odek/ -run "TestSandbox"
```

Note: MCP client E2E tests build the fakeserver from `internal/mcpclient/testdata/main.go` at test time (no pre-compiled binary). Cross-platform test fixes in v0.56.2: macOS temp dirs classified correctly (LocalWrite not SystemWrite), Docker availability check now verifies daemon reachability.
