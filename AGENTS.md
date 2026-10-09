# odek — Agent Maintenance Guide

This file is automatically loaded by odek when running inside this repository.
It provides context about the project's architecture, conventions, and how to update/maintain it.

---

## Project Identity

- **Package:** `odek` (Go module: `github.com/BackendStack21/odek`)
- **What it is:** Minimal Go autonomous agent runtime — ReAct (Reasoning + Acting) loop with zero frameworks (stdlib + a few focused packages).
- **Binary:** `odek` — single static binary, <15 MB, instant startup.
- **Config:** Five-layer priority: `~/.odek/secrets.env` → `~/.odek/config.json` → `./odek.json` → `ODEK_*` env vars → CLI flags.
- **Extension contract:** `odek-extension/v1` (docs/EXTENSIONS.md) — MCP server limits, artifact refs, runtime events, external session refs, execution budgets.
- **Releases:** tag-driven (`git tag vX.Y.Z && git push --tags` builds binaries + release). See [GitHub Releases](https://github.com/BackendStack21/odek/releases).

## Source Layout

```
odek.go                       Compatibility facade for the public Go API (type aliases + forwarding functions)
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox setup, system prompt,
                              --events-jsonl/--external-ref/budget flag wiring, init config templates
  dispatch.go                 CLI subcommand dispatch (+ budget.Error → exit code 4 mapping)
  shell.go                    Built-in shell tool (local or docker exec; danger-gated; optional timeout_seconds)
  serve.go                    Web UI server (HTTP + WebSocket; @-resource completion; protocol v2: delta
                              streaming, ping/pong, WS cancel, session_switch, answer_superseded + done.verified)
  serve_api.go                REST management API (/api/health, sessions search/pagination/pin/export,
                              memory facts + episode promote/discard + consolidate, skills + promote, tools,
                              profiles, sanitized config view, MCP listing, shutdown)
  serve_runs.go               Headless REST runs (POST /api/prompt → run registry, remote approval
                              bridge, cancel) + events ring (/api/events), usage stats (/api/usage),
                              WS connection registry (/api/connections, kick)
  serve_jobs.go               Serve-side job registry API (bg jobs, /api/jobs) + wake-on-complete wiring
  serve_supervision.go        WebUI task supervision: sub-agent state frames, cancel, safe recovery
  serve_artifacts.go          Artifact registry + delivery over the serve surface
  serve_workspace.go          WebUI workspace/session-state access endpoints
  serve_diagnostics.go        Diagnostics endpoints (/api/diagnostics)
  serve_retention.go          Session/artifact retention enforcement on the serve surface
  serve_bg_sandbox.go         Sandbox follow-up handling for background jobs (pidfile kill)
  repl.go                     Interactive REPL with multi-turn session support
  repl_editor.go              Terminal raw-mode input editor
  telegram.go                 Telegram bot command — wires odek agent into Telegram poller
  telegram_voice.go           Telegram voice messages: STT transcription of audio notes
  telegram_vision.go          Telegram vision: photo/image input routing
  subagent.go                 Sub-agent command (--goal, --context, --task) + flag parsing/limits
  subagent_tool.go            delegate_tasks built-in tool (sub-agent spawning)
  subagent_key.go             FD-based API key handoff (parent → sub-agent, never via env)
  subagent_registry.go        Parent-side registry of in-flight sub-agent tasks (cancel, status)
  subagent_artifacts.go       Sub-agent result-artifact protocol (files auto-delivered to the parent)
  subagent_artifact_registry.go  Collation-time artifact-id registry backing artifact_read
  subagent_accounting.go      Sub-agent token/cost accounting rollup into the parent session
  artifact_read_tool.go       Parent-only artifact_read tool — resolves artifact ids to validated
                              content; the model supplies only the id, never a filesystem path
  browser_tool.go             Built-in browser tool (HTTP fetch + headless navigation)
  file_tool.go                Built-in file tools (read_file, write_file, search_files, patch, glob, file_info)
  perf_tools.go               Native utilities (math_eval, diff, json_query, tree, checksum, head_tail, base64)
  http_request_tool.go        Single-URL HTTP status/size checks with SSRF and redirect guards
  introspect.go               config_view + list_tools tools and shared sanitized view builders
                              (structural sanitization: secrets never enter the view map)
  profiles_tool.go            list_subagent_profiles tool (capability profile discovery)
  speak_tool.go               speak tool (TTS via configured speech provider)
  speech_provider.go          Speech provider resolution (provider-backed TTS/STT)
  proactive.go                Proactive engagement presentation: return-after-break summary injection
                              and follow-up suggestions (presentation-only, never persisted)
  iteration_progress.go       Iteration-progress signal plumbing for clients
  native_outcomes.go          Native-tool outcome recording (feeds plan-check evidence)
  bg_tools.go                 Background command tools (bg_*) + per-surface runtime
  bg_wake.go                  Wake-on-complete: system-initiated turns when jobs finish (serve surface)
  bg_telegram.go              Telegram background-job integration
  bg_telegram_wake.go         Wake-on-complete for idle Telegram chats (coalesced, rate-bounded)
  wsapprover.go               WebSocket interactive approval relay (with friction + class-trust gates)
  refs.go                     @-resource reference resolution (files, sessions)
  untrusted.go                <untrusted·content_<nonce>> wrapper + per-call ingest recorder
  unreadscan.go               Pre-exec injection-scan enrichment for unread-script approvals
  audit.go                    Per-turn audit + `odek audit` subcommand (divergence heuristic)
  sandbox_file.go             Sandbox-aware file-tool bridge
  sandbox_cleanup.go          Sandbox teardown/cleanup helpers
  ssrf_guard.go               URL / SSRF validation helpers
  vision_tool.go              Vision / image-input tool
  vision_provider.go          Provider-backed vision backend (reuses the providers.<id> registry)
  transcribe_tool.go          Whisper.cpp audio transcription (local mode)
  web_search_tool.go          Web search tool
  session_search_tool.go      Session search tool
  mcp.go                      MCP server implementation (stdio transport)
  mcp_approval.go             Per-tool MCP server approval UI and persistence (key hashes limits/artifact_roots)
  project_sandbox_approval.go Project-level sandbox config approval gate
  skill_promote.go            `odek skill promote` — clear NeedsReview on a tainted skill
  schedule.go                 `odek schedule` command and scheduler wiring
  schedule_telegram.go        Telegram surface for schedules
  memory_cmd.go               `odek memory` command
  cleanup.go                  `odek cleanup [--dry-run]` one-shot storage sweep + janitor wiring (telegram/serve/schedule daemon)
  upgrade.go                  `odek upgrade [--check]` self-upgrade from GitHub Releases (checksums.txt SHA-256 verified)
  external_ref.go             --external-ref flag parsing (run + continue) → session.ExternalRef
  native_tool_context.go      Invocation-scoped contexts for independent native tool calls
  toolctx.go                  Tool-call context plumbing
  logs.go / operational_logging.go / surface_logging.go   Structured operational logging per surface
  security_report_validation_test.go  Regression bar for every documented mitigation
  *_test.go                   Unit + E2E tests covering all tools
internal/
  agent/                      Public runtime implementation and its package-local tests
  llmclient/                  Adapter over go-llm-sdk (DTO mapping, temperature polarity, SimpleCall, SideCall)
  loop/                       ReAct engine: observe → think → parallel-act → repeat. plan.go + plan_*.go —
                              built-in plan tool and check lifecycle; verify.go — final-answer verification
                              pass; completion_checks.go — engine-outcome check recording (never claims
                              from tool output); effects.go + reconcile.go — effect evidence and
                              post-batch reconciliation; signal.go — SignalEvent observability
                              (context_trimmed, tool_recovery, tool_running heartbeat); budget enforcement
                              (budget.Checker) + odek.event/v1 emission.
  tool/                       Thread-safe tool registry, clarify.go, send_message.go
  danger/                     Command/URL classification + bypass-resistant tokenizer. Approver interface +
                              TTYApprover with friction mode (interactive approval system lives here).
    classifier.go               normalize entry point, RiskClass set (11 classes), DangerousConfig, ActionForCommand, verb/path classification,
                                package doc listing the layered design and its limitations
    analysis.go                 Analyze → per-effect result, shell state across segments, MaxCommandBytes + token budget
    command_effects.go          Per-tool exec/write adapters (tar, sed, git, ssh/rsync, kubectl/helm, …)
    normalize.go                Unicode folding (homoglyphs, invisibles, styled letters) + command spacing
    normalize_phases.go         Quote-aware phases on a shared lexer: line joins, comments, here-docs, ANSI-C, brace lists/sequences
    compound.go                 Shell compound-command parser (loops, if/case, groups, functions, [[ ]], (( ))): every simple
                                command inside is classified; unpaired constructs are unknown
    wrapper_grammar.go          One option grammar for wrappers (timeout/nice/sudo/flock/script/nix/mise/…) incl. command-string options
    denylist.go                 Denylist token-prefix matching at every command position
    secret_reads.go             Secret-shaped env-var references and credential-file reads/writes → system_write
    network_upload.go           network_upload class: bodies from files/stdin, credentials, mutating methods, uploads, listeners
    gh_adapter.go               gh classified by command and verb (reads egress, mutations system_write, deletes destructive)
    git_repo_arming.go          Repo-aware git escalation: ordinary verbs are code_execution only when the repo is armed
    readledger.go               Unread-script gate: fingerprinted, bounded read ledger; ForgetReadLedger per session key
    ledger_indirect.go          Scripts delivered indirectly (pipes, substitutions, eval, find -exec, program-file options)
    path_identity.go            Symlink-resolving path targets for classification (snapshot, not an execution boundary)
    injection.go                Injection scanner (ScanInjection) with Unicode folding
    approver.go                 Approver interface, TTY approver, friction
    display.go                  SanitizeForDisplay/SanitizeInline: control/bidi/invisible characters escaped in every prompt
    monotonicity_fuzz_test.go   Fuzz invariants (suffix wipe never hidden, pipe-into-shell ≥ code_execution, harmless prefix
                                never lowers a verdict, bounded time)
  bgproc/                     Session-scoped background process manager (bg_* tools): bounded output rings,
                              spawn-time danger classification parity, group-signal stop
  diagnostics/                Runtime diagnostics helpers
  embedding/                  Embedding + featurization for semantic session search
  eval/                       Plan-tool / behavioral evaluation harness
  guard/                      Content-scope injection scanner (guard.ScanContentWithScope) + PIGuard sidecar integration
  logquery/                   Structured query over runtime logs
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
  mcp/                        MCP server handler (stdio JSON-RPC: tools/list, tools/call)
  mcpclient/                  MCP client (connect to external MCP servers); per-server limits (timeout/response bytes/result
                              chars/artifact roots) + odek-extension/v1 contract (contract.go), artifact-ref enforcement in CallTool
  sandbox/                    Docker sandbox lifecycle
  flock/                      Advisory file-locking helpers
  fsatomic/                   Atomic file-write helpers
  pathutil/                   Path helpers
  resource/                   @-resource resolver (files, sessions) with size/symlink hardening
  runtimelog/                 Runtime log store (replaces the retired ~/.odek/serve.log) with retention
  schedule/                   Cron-style scheduler: cronexpr, store, scheduler (global-only surface)
  transport/                  Shared HTTP transport with connection pooling
  ws/                         RFC 6455 WebSocket framing
docs/                         Documentation (CLI, API, CONFIG, MCP, EXTENSIONS, MEMORY, PLANNING, TELEGRAM, SECURITY, etc.)
```

## How It Works

### Agent Loop (`internal/loop/loop.go`)
ReAct cycle: observe → think → act → repeat.
- LLM returns tool calls or a final answer.
- **Parallel tool execution** — independent tool calls run concurrently (`max_tool_parallel`, default: 4).
- **Plan tool** — built-in `plan` tool (docs/PLANNING.md) for multi-step work: steps, optional acceptance checks, revise/check_replace lifecycle. Sub-agents never own or mutate parent plans. Check recording (`completion_checks.go`) consumes engine outcomes only — a check passes when its declared tool actually ran and succeeded, never from claims inside tool output; a failed or unclassifiable effect invalidates earlier checks in the same batch.
- **Batch approval gate** — multiple risky tools shown in one prompt. `classifyToolCall` classifies each individual `shell` command, each `patch`/`write_file` target, and the `browser` tool; shows full commands; withholds blanket `SetTrustAll` when unclassifiable tools (incl. MCP tools, classified `unknown`) remain.
- **Tool-failure recovery** — retry transient errors, skip permanent failures, continue without crashing. Stall detection: 3 consecutive identical tool calls inject a corrective hint + fire `tool_recovery` — a hint, never aborts the run.
- **Context-limit protection** — graduated trimming near the context window: old large tool results replaced with markers (4 most recent kept intact), then oldest turn groups dropped atomically (tool messages stay grouped with their parent). The protected head (system prompt, memory block, compaction digest, original task) is never dropped. The digest (body capped at an eighth of the budget) and the trim warning are reserved against the budget before groups are dropped, so the post-trim history fits. Token estimator counts tool schemas + reasoning content; safety margin self-tightens when provider-reported tokens exceed estimates (`margin_calibrated` signal). `trimToSurvival` handles provider context-length errors. **Rolling compaction** (on by default; `compaction: false` / `ODEK_COMPACTION=false` / `--no-compaction`) sketches dropped groups extractively into a rolling digest immediately, then a thinking-off side call replaces that sketch with a model digest on a later iteration if it arrives.
- **Final-answer verification pass** (`internal/loop/verify.go`, optional) — after a candidate final answer, a side-call checks the answer against the run's evidence and can trigger corrective cycles. Config: `verify` section (`enabled`, `mode` hint|strict|off, optional cheaper `model`, `max_cycles` ≤ 3, `max_tokens`); default off, `hint` when enabled. Sub-agents are opted in only via an explicit `subagent.verify` section — `delegate_tasks` never enables it implicitly, so N children cannot multiply verify cost.
- **Interaction modes** — engaging (narrated), enhance (persistent), verbose (raw), off.
- Max 90 iterations by default. On iteration-budget exhaustion the engine makes one final tool-less LLM call for a partial-progress summary (30s bound), returned marked `[Iteration budget reached — partial summary]`.
- **Post-response async processing** — episode extraction and extended-memory extraction run in background goroutines; `Agent.Close` drains them (~15s bound). Pending memory episodes can be promoted or discarded from the WebUI and REST API.
- **Per-turn session persistence** — `SetMessagesPersistCallback` fires after each completed step with a fresh snapshot; CLI/REPL/serve/Telegram wire it to `Store.SaveNoIndex` (atomic, skips remote vector indexing). Interrupted runs resume via `odek continue` from the last completed step; error paths persist partial history minus dangling tool calls. The final save per turn still updates the semantic index once.
- **Storage maintenance janitor** — `maintenance.Start` sweeps `~/.odek` (retention, log rotation, media sweep) inside `odek telegram`/`serve`/`schedule daemon`; `odek cleanup [--dry-run]` runs it on demand. Session files are trimmed at write time when they would exceed `MaxSessionFileBytes`. Operator-only config. See docs/MAINTENANCE.md.
- **Artifact-aware file search** — `search_files` skip `node_modules`, `vendor`, `.git`, `__pycache__`, `.venv`, etc.
- **Semantic session search** — `session_search` tool: go-vector RandomProjections + k-NN, two-tier (vector index → exhaustive fallback).
- **Background commands** — `bg_start`/`bg_list`/`bg_status`/`bg_output`/`bg_stop` tools over a process-scoped, session-keyed process manager (`internal/bgproc`): session-scoped job lifetime, bounded in-memory output rings, spawn-time danger classification parity with `shell`, group-signal stop (sandbox mode via the pidfile follow-up). **Wake-on-complete** (default on; `background.wake_on_complete`, coalesced by `wake_coalesce_ms`, bounded by `max_wakes_per_hour`): when a job finishes while its session is idle, a system-initiated turn lets the model fetch `bg_output` and report unprompted. Config: `background` section (docs/CONFIG.md).

### Extension capabilities (odek-extension/v1, v1.24.0)
- **MCP per-server limits** — `timeout_seconds` (30s/3600s cap), `max_response_bytes` (10 MiB/64 MiB ceiling), `max_result_chars` (200k/1M cap, structured truncation notice), `artifact_roots` (empty ⇒ refs rejected). Resolved per client; approval keys hash all four fields. Per-server `enabled` toggle (nil/true = enabled, back-compat).
- **Artifact references** — MCP tools return `odek.tool-result/v1` envelopes with `file://` refs instead of bulk content; validated fail-closed in `internal/artifact`; model sees metadata only. The parent reads full content via the parent-only `artifact_read` tool (id-supplied only, no model-controlled paths; TOCTOU-hardened).
- **Runtime events** — `odek.event/v1` via `Config.EventHandler` (non-blocking, panic-isolated) and `run --events-jsonl`. Types: run_started, iteration_completed, tool_call_*, session_saved, context_trimmed, budget_exceeded, run_completed/run_failed, plan_created, plan_updated, plan_blocked, subagent_denied, subagent_spawned, subagent_completed, subagent_concurrency_wait.
- **External session refs** — `Session.ExternalRefs` + `--external-ref` on run/continue; validated, deduped, never dereferenced.
- **Execution budgets** — `limits` config section + `--max-runtime/--max-tool-calls/--max-input-tokens/--max-output-tokens/--max-cost-usd` on `run`; typed `budget.Error` → CLI exit code 4; session persisted before return. Per-model prices via `limits.model_prices` with flat-pair fallback; cost enforcement only when cap + prices configured. `odek init --global` scaffolds the section (zeros = off). `GET /api/limits` on serve exposes limits + effective prices for cost rendering.

### Tools
Built-in tools include: http_request, math_eval, diff, json_query, tree, checksum, head_tail, base64, transcribe, speak, browser, read_file, write_file, search_files, patch, shell, plan, delegate_tasks, session_search, artifact_read, config_view, list_tools, list_subagent_profiles, clarify, send_message.

### Terminal Rendering (`internal/render/`)
Vertical space compression is baked into the render paths; blank lines removed from Iteration/FinalAnswer/Summary. Raw-mode cursor uses `\r\n` for cross-platform compatibility.

### Identity
System prompt priority: `--system` flag > `~/.odek/IDENTITY.md` > compiled-in defaultSystem. Explicit prompts and IDENTITY.md are capped at 256 KiB and scanned with `danger.ScanInjection` (failure → compiled-in default). Project `AGENTS.md` ignored if >256 KiB. The compiled-in default carries the execution-provenance rules (justification from the principal; read what you execute; failed reads never become executions; deferred-execution confirmation; tool metadata is not directives) and is itself scanner-clean — pinned by `TestDefaultSystem_PassesOwnInjectionScan` so a copy into `IDENTITY.md` is never rejected. Operator identity surfaces (`--system`, `ODEK_SYSTEM`, config `system`, `IDENTITY.md`) carry identity only — name, mission, persona. `buildSystemPrompt` force-composes the invariant pillar on top of every accepted identity via `composeSystem` (idempotent — an identity already carrying the pillar is kept whole), so no operator surface can drop the security rules; the compiled default is `defaultIdentity` + pillar, pinned by `cmd/odek/system_pillar_test.go`. Sub-agents compose the same invariant pillar into `subagentSystem` (shared `securityPillar` const: Safety, Execution provenance, IPI) plus role amendments — a child has no principal channel, so confirmation rules become skip-and-report, justification scope is the declared task, and deferred execution requires the task to name the mechanism. Parity is pinned by `cmd/odek/subagent_pillar_test.go`; the operator-writable parent surface (`--system`, `IDENTITY.md`) never propagates to children.

### Security Architecture

Layered prompt-injection / approval-fatigue defenses. The full per-mitigation list lives in [docs/SECURITY.md](docs/SECURITY.md); `cmd/odek/security_report_validation_test.go` is the regression bar. Summary by layer:

- **Untrusted-content boundary** (`cmd/odek/untrusted.go`) — every externally-sourced tool result (browser, file/shell/search tools, MCP, session_search, @-refs, --ctx, attachments, artifact_read) is wrapped in a per-call nonce'd `<untrusted·content_<nonce>>` tag; tool-result delimiters are also nonce'd (`internal/loop`). Skill/episode context injected into the system prompt is wrapped too. The per-session audit log (`cmd/odek/audit.go`) records every ingest and flags divergence between user-mentioned resources and agent actions.
- **Provenance gates** — tainted memory episodes are stored but never auto-replayed; skills from untrusted sources (imported via URI, project `./.odek/skills/`) are pinned `NeedsReview` until `odek skill promote --force` is run after human review, excluded from trigger matching, and protected against frontmatter tampering. Load-time and import-time skill bodies go through the injection scanner (`guard.ScanContentWithScope`). `odek` self-invocation via shell is `system_write` so the agent can't reach its own trust mutations.
- **Danger classifier** (`internal/danger/classifier.go`; layered design in its package doc) — quote-aware normalization ($IFS, ANSI-C, brace lists/sequences, substitutions, heredocs, wrappers, backslashes, basenames); compound commands (`for`/`while`/`if`/`case`/groups/functions/`[[ ]]`) parsed so every simple command inside is classified, unterminated constructs and quotes classify `unknown`. Wrappers share one option grammar (`wrapper_grammar.go`) incl. command-string options (`env -S`, `script -c`, `flock -c`). Covers awk/sed/editor escapes, pipe-fed xargs composition, root-level mutation targets, git data-loss verbs, `git -c`/config code exec, find/rsync destructive flags, env dumps, exec-controlling `export`s, shell operand/redirect path classification (writes to shell rc files, ~/.ssh, ~/.odek escalate to system_write). `network_upload` (default prompt, ranked between `network_egress` and `code_execution`) splits uploads, credentialed/mutating requests, listeners and tunnels from plain egress; `gh` is classified by command and verb (`gh_adapter.go`). Ordinary git verbs escalate to `code_execution` only when the targeted repository is armed (hooks, fsmonitor, filters/drivers, editors, includes); an undeterminable repo, `GIT_*` overrides, sudo wrappers or a hook written earlier in the same command fail closed (`git_repo_arming.go`). Denylist entries are token prefixes matched at every command position, with tool global options stripped and known variables resolved — not raw string prefixes. Secret-shaped env vars (`$NAME`, `${!v}`, `printenv`, `os.environ`) and credential files (by basename, extension or directory) are `system_write` (`secret_reads.go`). Trust anchors under `~/.odek` are write-protected from generic file tools. The read ledger is fingerprinted (`WasReadFresh`: post-read mutation re-fires the unread-script gate), covers scripts delivered through pipes, substitutions, `eval`, `find -exec` and program-file options, is bounded (4096 paths per session, 1024 sessions, LRU eviction only ever removes a licence) and dropped per session with `danger.ForgetReadLedger` (serve session delete, Telegram reset, schedule run end); unread-script approvals carry a pre-exec injection-scan enrichment incl. single-layer base64/hex decode (`cmd/odek/unreadscan.go`, scan never populates the ledger). Quoted operator-shaped words (`';'`, `'|'`, a decoded `$'\n'`) are arguments in every segment/pipe split (literal-marked tokens), the denylist scan examines each distinct command line once (bounded on nested `eval`/`find -exec`) and also reads here-strings and static `echo`/`printf` pipes into a shell, and a home reached through a symlink counts as the same home. Fuzz invariants in `monotonicity_fuzz_test.go` (suffix wipe never hidden, pipe into shell at least `code_execution`, harmless prefix never lowers a verdict, bounded time).
- **Plan-check honesty** — plan acceptance checks record outcomes from actual engine observation only; failed or unclassifiable tool effects invalidate earlier checks in the same batch, so a check can never be satisfied by a claim inside tool output.
- **Approval friction** — TTY/WS/Telegram approvers engage friction after 3 same-class approvals in 60s (type `approve`, pause, trust shortcut hidden); `destructive`/`blocked`/`unknown` never get trust shortcuts. TTY prompts are process-wide serialized. Everything shown in an approval (TTY/WebSocket/Telegram prompts, batch cards, MCP/sandbox approval prompts, denial error strings) goes through `danger.SanitizeForDisplay`/`SanitizeInline`, which escape control, bidi and invisible characters so the human reads the bytes that run; the read_only non-interactive carve-out is keyed on the native tool name, never the model-supplied description.
- **Sub-agent caps** — `delegate_tasks` carries trust_level + max_risk enforced via the sub-agent's DangerousConfig; MCP tools withheld from untrusted sub-agents; API keys handed off via unlinked-tempfile FD, never env. Sub-agent results over ~2000 chars are delivered as registry-backed artifacts the parent reads via `artifact_read`.
- **MCP hardening** — subprocess env sanitization (secret-pattern stripping), tool-name/description/inputSchema validation + injection scans, per-tool approval for every server (keys hash command/args/env + schema hash + description text + all four limit fields), per-server limits with absolute ceilings, per-server `enabled` toggle, artifact-ref fail-closed validation.
- **Config trust split** — `./odek.json` is untrusted: sensitive sections (provider, providers, llm, base_url, api_key, system, dangerous, memory, telegram, web_search, embedding, sessions, skills.dirs, verify, profiles, guard, schedules) ignored with warnings; sandbox knobs gated behind explicit operator approval (incl. implicit `Dockerfile.odek` builds, content-hash keyed); project limits may only lower global budgets, project prices rejected outright. Global config/secrets permission-checked; config files size-capped.
- **Serve / network surface** — per-instance CSRF token on `/ws` and all `/api/*`, loopback Host checks, local-origin requirement for mutations, per-session auth tokens + rate limiting, clickjacking headers, WS message-size caps. SSRF dial guard (DNS-rebinding-safe, internal-IP refusal, proxy refusal) on browser/http_request/web_search. WebUI done-frame stats are markup-escape hardened; WebUI task supervision renders sub-agent state without trusting frame content.
- **Budgets, events, refs (v1.24.0)** — budget clamp merge (see above); event stream carries SHA-256 arg hashes + sizes only (never raw args), redact applied, JSONL sink 0600/no-symlink/fsync-per-event, drop-on-full dispatch; external refs validated and never dereferenced.
- **Resource bounds** — pervasive size caps (shell output 1 MiB/stream, perf-tool files 10 MiB, session files 32 MiB, skill files 1 MiB, browser snapshots/history/elements, tree width, search results, write_file content, patch expansion), classifier input (commands over `danger.MaxCommandBytes` = 64 KiB are `unknown`/denied; one analysis examines at most 4096 tokens; here-doc, substitution and brace scanning are budgeted; read ledger 4096 paths × 1024 sessions; prompt text capped by `danger.DisplayMaxBytes`/`InlineMaxBytes`) to keep hostile input from OOMing the process.
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
ODEK_E2E=true go test -v -count=1 ./cmd/odek/ -run "TestMCPClientE2E_"

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
# Classifier monotonicity targets (run with a non-root HOME, see below)
HOME=/home/user go test -fuzz=FuzzSeparatorThenWipe -fuzztime=30s ./internal/danger/
HOME=/home/user go test -fuzz=FuzzPipeIntoShell -fuzztime=30s ./internal/danger/
HOME=/home/user go test -fuzz=FuzzHarmlessPrefixKeepsRank -fuzztime=30s ./internal/danger/
HOME=/home/user go test -fuzz=FuzzAnalyzeBounded -fuzztime=30s ./internal/danger/
```

CI also runs `golangci-lint` (staticcheck) and `govulncheck` on every push/PR — run both locally before pushing. `golangci-lint` may fail to run on this module's Go version (it can crash while loading packages); then run `go vet` and `gofmt -l` on the changed packages and rely on CI for staticcheck.

`internal/danger` tests must run with a non-root `HOME` (`HOME=/home/user go test -count=1 ./internal/danger/`): `/root` is a system prefix and counts as the current user's home only when `HOME` resolves there, so the path-classification expectations assume a home elsewhere. The `internal/danger` fuzz targets seed from every string literal in the package's regression tests, so new regression cases extend the corpus automatically.

Note: MCP client E2E tests build the fakeserver from `internal/mcpclient/testdata/main.go` at test time (the extension mock is env-gated via `FAKE_ARTIFACT_MODE=1`). macOS temp dirs are classified as `LocalWrite` (not `SystemWrite`), and the Docker availability check verifies daemon reachability (5s timeout) before running sandbox tests.

Agent workflow notes:

- **Scope test runs.** Prefer `go test -count=1 <changed packages>` over `./...`; run `-race` only on changed packages. A full `go test -race ./...` takes several minutes (race-instrumented build + 5–10× runtime slowdown).
- **Sandbox test runs use `make test-sandbox`.** Inside the odek sandbox (`Dockerfile.odek` image), `/tmp` is mounted noexec and the workspace mount does not enforce write bits, so tests must run via `make test-sandbox` — it splits TempDir by need (exec-capable `GOTMPDIR`/`TMPDIR` for the cmd/odek mocks and the mcpclient fakeserver; the permission-enforcing default for flock/maintenance). Host runs keep `make test` / `make test-cmd`.
- **Comments name behavior, not plan tickets.** Do not put `Slice C`, `M1.2`, `P0-1`, `WP6`, `H-9`, or similar milestone/review IDs in comments or product docs. Describe what the code does. Runtime sequence labels (`Phase 1: execute tools`) are fine.
- **The `shell` tool is fully buffered.** Nothing is shown or returned until the command exits, and the default timeout is 30 minutes — a long command looks "stuck" even though it is running (a `tool_running` heartbeat signal fires every 60s). Set `timeout_seconds` explicitly for known long-running commands (builds, test suites) so a genuinely stuck command fails fast, and pass `go test -timeout` for test runs.
