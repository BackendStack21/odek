# odek — Agent Maintenance Guide

This file is automatically loaded by odek when running inside this repository.
It provides context about the project's architecture, conventions, and how to update/maintain it.

---

## Project Identity

- **Package:** `odek` (Go module: `github.com/BackendStack21/odek`)
- **What it is:** Minimal Go autonomous agent runtime — ReAct (Reasoning + Acting) loop with zero frameworks (stdlib + a few focused packages).
- **Binary:** `odek` — single static binary, ~11 MB, instant startup.
- **Config:** Five-layer priority: `~/.odek/secrets.env` → `~/.odek/config.json` → `./odek.json` → `ODEK_*` env vars → CLI flags.
- **Extension contract:** `odek-extension/v1` (docs/EXTENSIONS.md) — MCP server limits, artifact refs, runtime events, external session refs, execution budgets.
- **Releases:** tag-driven (`git tag vX.Y.Z && git push --tags` builds binaries + release). See [GitHub Releases](https://github.com/BackendStack21/odek/releases).

## Source Layout

```
odek.go                       Public API (Config, New, Run, Close, ProfileLabel, Tool interface)
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox setup, system prompt,
                              --events-jsonl/--external-ref/budget flag wiring, init config templates
  dispatch.go                 CLI subcommand dispatch (+ budget.Error → exit code 4 mapping)
  shell.go                    Built-in shell tool (local or docker exec; danger-gated; optional timeout_seconds)
  serve.go                    Web UI server (HTTP + WebSocket; @-resource completion; protocol v2: delta
                              streaming, ping/pong, WS cancel, session_switch)
  serve_api.go                REST management API (/api/health, sessions search/pagination/pin/export,
                              memory facts + episode promote + consolidate, skills + promote, tools,
                              profiles, sanitized config view, MCP listing, shutdown)
  serve_runs.go               Headless REST runs (POST /api/prompt → run registry, remote approval
                              bridge, cancel) + events ring (/api/events), usage stats (/api/usage),
                              WS connection registry (/api/connections, kick)
  repl.go                     Interactive REPL with multi-turn session support
  repl_editor.go              Terminal raw-mode input editor
  telegram.go                 Telegram bot command — wires odek agent into Telegram poller
  subagent.go                 Sub-agent command (--goal, --context, --task) + flag parsing/limits
  subagent_tool.go            delegate_tasks built-in tool (sub-agent spawning)
  subagent_key.go             FD-based API key handoff (parent → sub-agent, never via env)
  browser_tool.go             Built-in browser tool (HTTP fetch + headless navigation)
  file_tool.go                Built-in file tools (read_file, write_file, search_files, patch, batch_read, glob, file_info)
  external_ref.go             --external-ref flag parsing (run + continue) → session.ExternalRef
  perf_tools.go               Performance/parallelism tools (batch_patch, parallel_shell, http_batch, math_eval, diff,
                              count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count)
  mcp.go                      MCP server implementation (stdio transport)
  mcp_approval.go             Per-tool MCP server approval UI and persistence (key hashes limits/artifact_roots)
  project_sandbox_approval.go Project-level sandbox config approval gate
  transcribe_tool.go          Whisper.cpp audio transcription
  vision_tool.go              Vision / image-input tool
  web_search_tool.go          Web search tool
  session_search_tool.go      Session search tool
  bg_tools.go                 Background command tools (bg_*) + per-surface runtime
  bg_telegram.go              Telegram background-job integration
  wsapprover.go               WebSocket interactive approval relay (with friction + class-trust gates)
  refs.go                     @-resource reference resolution (files, sessions)
  untrusted.go                <untrusted_content_<nonce>> wrapper + per-call ingest recorder
  audit.go                    Per-turn audit + `odek audit` subcommand (divergence heuristic)
  sandbox_file.go             Sandbox-aware file-tool bridge
  ssrf_guard.go               URL / SSRF validation helpers
  skill_promote.go            `odek skill promote` — clear NeedsReview on a tainted skill
  schedule.go                 `odek schedule` command and scheduler wiring
  memory_cmd.go               `odek memory` command
  cleanup.go                  `odek cleanup [--dry-run]` one-shot storage sweep + janitor wiring (telegram/serve/schedule daemon)
  upgrade.go                  `odek upgrade [--check]` self-upgrade from GitHub Releases (checksums.txt SHA-256 verified)
  parallel.go                 Parallelism helpers
  toolctx.go                  Tool-call context plumbing
  security_report_validation_test.go  Regression bar for every documented mitigation
  *_test.go                   250+ unit + E2E tests covering all tools
internal/
  llmclient/                  Adapter over go-llm-sdk (DTO mapping, temperature polarity, SimpleCall)
  loop/                       ReAct engine: observe → think → parallel-act → repeat. signal.go — SignalEvent observability
                              (context_trimmed, tool_recovery, tool_running heartbeat). Budget enforcement (budget.Checker)
                              + odek.event/v1 emission.
  tool/                       Thread-safe tool registry, clarify.go, send_message.go
  danger/                     Command/URL classification + bypass-resistant tokenizer. TTYApprover with friction mode.
  auth/                       Interactive approval system
  memory/                     MemoryManager (facts, buffer, episodes, merge, scan). EpisodeProvenance — tainted episodes never auto-replayed.
  session/                    Session store (CRUD, trim, cleanup, compact JSON). AuditStore + divergence heuristic.
                              ExternalRef — opaque operator-supplied refs, never dereferenced.
  artifact/                   odek.artifact-ref/v1 + odek.tool-result/v1: fail-closed ref validation
                              (roots/symlinks/sha256/size) and model-safe rendering (metadata only, no paths/content).
  events/                     odek.event/v1 runtime event stream: Event, non-blocking panic-isolated Emitter
                              (args hashed, redact applied), JSONLSink (0600, no symlinks, flush per event).
  budget/                     Hard execution budgets: Limits (+ model_prices resolution), typed Error, per-run Checker.
  maintenance/                Storage-maintenance janitor (session/audit/plan retention, log rotation, media sweep).
                              Config: `maintenance` section (operator-only).
  skills/                     Skill system (types, loader, triggers, import, cache). SkillProvenance gate.
  config/                     Config file loading, env vars, secrets.env, priority merge, limits clamp (project may only lower budgets)
  telegram/                   Telegram bot: bot.go, poller.go, handler.go, commands.go, session.go, health.go, plan.go, media_path.go
  render/                     Terminal output and narrator support
  narrate/                    LLM-powered emoji-rich progress messages
  redact/                     Secret redaction (20+ patterns)
  mcp/                        MCP server handler (tools/list, tools/call, SSE streaming)
  mcpclient/                  MCP client (connect to external MCP servers); per-server limits (timeout/response bytes/result
                              chars/artifact roots) + odek-extension/v1 contract (contract.go), artifact-ref enforcement in CallTool
  sandbox/                    Docker sandbox lifecycle
  flock/                      Advisory file-locking helpers
  fsatomic/                   Atomic file-write helpers
  pathutil/                   Path helpers
  resource/                   @-resource resolver (files, sessions) with size/symlink hardening
  transport/                  Shared HTTP transport with connection pooling
  ws/                         RFC 6455 WebSocket framing
docs/                         Documentation (CLI, API, CONFIG, MCP, EXTENSIONS, MEMORY, TELEGRAM, SECURITY, etc.)
```

## How It Works

### Agent Loop (`internal/loop/loop.go`)
ReAct cycle: observe → think → act → repeat.
- LLM returns tool calls or a final answer.
- **Parallel tool execution** — independent tool calls run concurrently (`max_tool_parallel`, default: 4).
- **Batch approval gate** — multiple risky tools shown in one prompt. `classifyToolCall` classifies every command inside `parallel_shell`, every path inside `batch_patch`, and the `browser` tool; shows full commands; withholds blanket `SetTrustAll` when unclassifiable tools (incl. MCP tools, classified `unknown`) remain.
- **Tool-failure recovery** — retry transient errors, skip permanent failures, continue without crashing. Stall detection: 3 consecutive identical tool calls inject a corrective hint + fire `tool_recovery` — a hint, never aborts the run.
- **Context-limit protection** — graduated trimming near the context window: old large tool results replaced with markers (4 most recent kept intact), then oldest turn groups dropped atomically (tool messages stay grouped with their parent). The protected head (system prompt, memory block, compaction digest, original task) is never dropped. Token estimator counts tool schemas + reasoning content; safety margin self-tightens when provider-reported tokens exceed estimates (`margin_calibrated` signal). `trimToSurvival` handles provider context-length errors. **Rolling compaction** (on by default; `compaction: false` / `ODEK_COMPACTION=false` / `--no-compaction`) summarizes dropped groups into a rolling digest instead of losing them.
- **Interaction modes** — engaging (narrated), enhance (persistent), verbose (raw), off.
- Max 90 iterations by default. On iteration-budget exhaustion the engine makes one final tool-less LLM call for a partial-progress summary (30s bound), returned marked `[Iteration budget reached — partial summary]`.
- **Post-response async processing** — episode extraction and extended-memory extraction run in background goroutines; `Agent.Close` drains them (~15s bound).
- **Per-turn session persistence** — `SetMessagesPersistCallback` fires after each completed step with a fresh snapshot; CLI/REPL/serve/Telegram wire it to `Store.SaveNoIndex` (atomic, skips remote vector indexing). Interrupted runs resume via `odek continue` from the last completed step; error paths persist partial history minus dangling tool calls. The final save per turn still updates the semantic index once.
- **Storage maintenance janitor** — `maintenance.Start` sweeps `~/.odek` (retention, log rotation, media sweep) inside `odek telegram`/`serve`/`schedule daemon`; `odek cleanup [--dry-run]` runs it on demand. Session files are trimmed at write time when they would exceed `MaxSessionFileBytes`. Operator-only config. See docs/MAINTENANCE.md.
- **Artifact-aware file search** — `search_files`/`multi_grep` skip `node_modules`, `vendor`, `.git`, `__pycache__`, `.venv`, etc.
- **Semantic session search** — `session_search` tool: go-vector RandomProjections + k-NN, two-tier (vector index → exhaustive fallback).
- **Background commands** — `bg_start`/`bg_list`/`bg_status`/`bg_output`/`bg_stop` tools over a process-scoped, session-keyed process manager (`internal/bgproc`): session-scoped job lifetime, bounded in-memory output rings, spawn-time danger classification parity with `shell`, group-signal stop (sandbox mode via the pidfile follow-up). Config: `background` section (docs/CONFIG.md).

### Extension capabilities (odek-extension/v1, v1.24.0)
- **MCP per-server limits** — `timeout_seconds` (30s/3600s cap), `max_response_bytes` (10 MiB/64 MiB ceiling), `max_result_chars` (200k/1M cap, structured truncation notice), `artifact_roots` (empty ⇒ refs rejected). Resolved per client; approval keys hash all four fields.
- **Artifact references** — MCP tools return `odek.tool-result/v1` envelopes with `file://` refs instead of bulk content; validated fail-closed in `internal/artifact`; model sees metadata only.
- **Runtime events** — `odek.event/v1` via `Config.EventHandler` (non-blocking, panic-isolated) and `run --events-jsonl`. Types: run_started, iteration_completed, tool_call_*, session_saved, context_trimmed, budget_exceeded, run_completed/run_failed, plan_created, plan_updated, subagent_denied, subagent_spawned, subagent_completed, subagent_concurrency_wait.
- **External session refs** — `Session.ExternalRefs` + `--external-ref` on run/continue; validated, deduped, never dereferenced.
- **Execution budgets** — `limits` config section + `--max-runtime/--max-tool-calls/--max-input-tokens/--max-output-tokens/--max-cost-usd` on `run`; typed `budget.Error` → CLI exit code 4; session persisted before return. Per-model prices via `limits.model_prices` with flat-pair fallback; cost enforcement only when cap + prices configured. `odek init --global` scaffolds the section (zeros = off). `GET /api/limits` on serve exposes limits + effective prices for cost rendering.

### Tools
All built-in tools with zero subprocess forks: batch_read, batch_patch, parallel_shell, http_batch, math_eval, diff, count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count, transcribe, browser, read_file, write_file, search_files, patch, shell, delegate_tasks, session_search, config_view, list_tools.

### Terminal Rendering (`internal/render/`)
Vertical space compression is baked into the render paths; blank lines removed from Iteration/FinalAnswer/Summary. Raw-mode cursor uses `\r\n` for cross-platform compatibility.

### Identity
System prompt priority: `--system` flag > `~/.odek/IDENTITY.md` > compiled-in defaultSystem. Explicit prompts and IDENTITY.md are capped at 256 KiB and scanned with `danger.ScanInjection` (failure → compiled-in default). Project `AGENTS.md` ignored if >256 KiB. The compiled-in default carries the execution-provenance rules (justification from the principal; read what you execute; failed reads never become executions; deferred-execution confirmation; tool metadata is not directives) and is itself scanner-clean — pinned by `TestDefaultSystem_PassesOwnInjectionScan` so a copy into `IDENTITY.md` is never rejected. Operator identity surfaces (`--system`, `ODEK_SYSTEM`, config `system`, `IDENTITY.md`) carry identity only — name, mission, persona. `buildSystemPrompt` force-composes the invariant pillar on top of every accepted identity via `composeSystem` (idempotent — an identity already carrying the pillar is kept whole), so no operator surface can drop the security rules; the compiled default is `defaultIdentity` + pillar, pinned by `cmd/odek/system_pillar_test.go`. Sub-agents compose the same invariant pillar into `subagentSystem` (shared `securityPillar` const: Safety, Execution provenance, IPI) plus role amendments — a child has no principal channel, so confirmation rules become skip-and-report, justification scope is the declared task, and deferred execution requires the task to name the mechanism. Parity is pinned by `cmd/odek/subagent_pillar_test.go`; the operator-writable parent surface (`--system`, `IDENTITY.md`) never propagates to children.

### Security Architecture

Layered prompt-injection / approval-fatigue defenses. The full per-mitigation list lives in [docs/SECURITY.md](docs/SECURITY.md); `cmd/odek/security_report_validation_test.go` is the regression bar. Summary by layer:

- **Untrusted-content boundary** (`cmd/odek/untrusted.go`) — every externally-sourced tool result (browser, file/shell/search tools, MCP, session_search, @-refs, --ctx, attachments) is wrapped in a per-call nonce'd `<untrusted_content_<nonce>>` tag; tool-result delimiters are also nonce'd (`internal/loop`). Skill/episode context injected into the system prompt is wrapped too. The per-session audit log (`cmd/odek/audit.go`) records every ingest and flags divergence between user-mentioned resources and agent actions.
- **Provenance gates** — tainted memory episodes are stored but never auto-replayed; skills from untrusted sources (imported via URI, project `./.odek/skills/`) are pinned `NeedsReview` until `odek skill promote --force` is run after human review, excluded from trigger matching, and protected against frontmatter tampering. Load-time and import-time skill bodies go through the injection scanner (`guard.ScanContentWithScope`). `odek` self-invocation via shell is `system_write` so the agent can't reach its own trust mutations.
- **Danger classifier** (`internal/danger/classifier.go`) — bypass-resistant normalization ($IFS, command substitution, wrappers, backslashes, basenames); covers awk/sed/editor escapes, pipe-fed xargs composition, root-level mutation targets, git data-loss verbs, `gh` as network egress, `git -c`/config code exec, find/rsync destructive flags, env dumps, shell operand/redirect path classification (writes to shell rc files, ~/.ssh, ~/.odek escalate to system_write). Trust anchors under `~/.odek` are write-protected from generic file tools. The read ledger is fingerprinted (`WasReadFresh`: post-read mutation re-fires the unread-script gate), and unread-script approvals carry a pre-exec injection-scan enrichment incl. single-layer base64/hex decode (`cmd/odek/unreadscan.go`, scan never populates the ledger).
- **Approval friction** — TTY/WS/Telegram approvers engage friction after 3 same-class approvals in 60s (type `approve`, pause, trust shortcut hidden); `destructive`/`blocked`/`unknown` never get trust shortcuts. TTY prompts are process-wide serialized.
- **Sub-agent caps** — `delegate_tasks` carries trust_level + max_risk enforced via the sub-agent's DangerousConfig; MCP tools withheld from untrusted sub-agents; API keys handed off via unlinked-tempfile FD, never env.
- **MCP hardening** — subprocess env sanitization (secret-pattern stripping), tool-name/description/inputSchema validation + injection scans, per-tool approval for every server (keys hash command/args/env + schema hash + description text + all four limit fields), per-server limits with absolute ceilings, artifact-ref fail-closed validation.
- **Config trust split** — `./odek.json` is untrusted: sensitive sections (provider, providers, llm, base_url, api_key, system, dangerous, memory, telegram, web_search, embedding, sessions, skills.dirs) ignored with warnings; sandbox knobs gated behind explicit operator approval (incl. implicit `Dockerfile.odek` builds, content-hash keyed); project limits may only lower global budgets, project prices rejected outright. Global config/secrets permission-checked; config files size-capped.
- **Serve / network surface** — per-instance CSRF token on `/ws` and all `/api/*`, loopback Host checks, local-origin requirement for mutations, per-session auth tokens + rate limiting, clickjacking headers, WS message-size caps. SSRF dial guard (DNS-rebinding-safe, internal-IP refusal, proxy refusal) on browser/http_batch/web_search.
- **Budgets, events, refs (v1.24.0)** — budget clamp merge (see above); event stream carries SHA-256 arg hashes + sizes only (never raw args), redact applied, JSONL sink 0600/no-symlink/fsync-per-event, drop-on-full dispatch; external refs validated and never dereferenced.
- **Resource bounds** — pervasive size caps (shell output 1 MiB/stream, perf-tool files 10 MiB, session files 32 MiB, skill files 1 MiB, browser snapshots/history/elements, tree width, search results, write_file content, patch expansion) to keep hostile input from OOMing the process.
- **Telegram** — chat-scoped sessions/plans/media, callback binding to originating user, outbound media allowlist + approval, secret-subtree rejection, singleton flock, 0600 logs.
- **Redaction** — `internal/redact` (20+ patterns: provider keys, cloud creds, PEM, JWT, DB URLs, …) applied to sessions, logs, and the event stream.

### Security findings (`sec_findings.md`)

`sec_findings.md` at the repository root is the running security audit log. It is
intentionally listed in `.gitignore` so that audit output and in-progress
findings are not committed to the repository by default. Do not commit this
file in pull requests unless you explicitly intend to publish a finalized
audit snapshot.

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

# PIGuard sidecar E2E (local only — too heavy for CI; provisions the
# docker stack, runs the env-gated test, tears down. Use --linux on macOS
# for full socket-mode coverage.)
docker/piguard-e2e.sh

# Fuzz soaks
go test -fuzz=FuzzExtractJSON -fuzztime=30s ./internal/memory/extended/
go test -fuzz=FuzzParseSkillContent -fuzztime=30s ./internal/skills/
go test -fuzz=FuzzSessionLoad -fuzztime=30s ./internal/session/
go test -fuzz=FuzzParseEnvelope -fuzztime=30s ./internal/artifact/      # + FuzzValidateRef
go test -fuzz=FuzzEventJSON -fuzztime=30s ./internal/events/
go test -fuzz=FuzzExternalRefValidate -fuzztime=30s ./internal/session/
go test -fuzz=FuzzParseExternalRefFlag -fuzztime=30s ./cmd/odek/
go test -fuzz=FuzzClampProjectLimits -fuzztime=30s ./internal/config/
```

CI also runs `golangci-lint` (staticcheck) and `govulncheck` on every push/PR — run both locally before pushing.

Note: MCP client E2E tests build the fakeserver from `internal/mcpclient/testdata/main.go` at test time (the extension mock is env-gated via `FAKE_ARTIFACT_MODE=1`). macOS temp dirs are classified as `LocalWrite` (not `SystemWrite`), and the Docker availability check verifies daemon reachability (5s timeout) before running sandbox tests.

Agent workflow notes:

- **Scope test runs.** Prefer `go test -count=1 <changed packages>` over `./...`; run `-race` only on changed packages. A full `go test -race ./...` takes several minutes (race-instrumented build + 5–10× runtime slowdown).
- **Sandbox test runs use `make test-sandbox`.** Inside the odek sandbox (`Dockerfile.odek` image), `/tmp` is mounted noexec and the workspace mount does not enforce write bits, so tests must run via `make test-sandbox` — it splits TempDir by need (exec-capable `GOTMPDIR`/`TMPDIR` for the cmd/odek mocks and the mcpclient fakeserver; the permission-enforcing default for flock/maintenance). Host runs keep `make test` / `make test-cmd`.
- **The `shell` tool is fully buffered.** Nothing is shown or returned until the command exits, and the default timeout is 30 minutes — a long command looks "stuck" even though it is running (a `tool_running` heartbeat signal fires every 60s). Set `timeout_seconds` explicitly for known long-running commands (builds, test suites) so a genuinely stuck command fails fast, and pass `go test -timeout` for test runs.
