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
- **Releases:** see [GitHub Releases](https://github.com/BackendStack21/odek/releases) for the current version and changelog.

## Source Layout

```
odek.go                       Public API (Config, New, Run, Close, ModelProfile, KnownProfiles, Tool interface)
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox setup, system prompt
  dispatch.go                 CLI subcommand dispatch
  shell.go                    Built-in shell tool (local or docker exec; danger-gated)
  serve.go                    Web UI server (HTTP + WebSocket; @-resource completion)
  repl.go                     Interactive REPL with multi-turn session support
  repl_editor.go              Terminal raw-mode input editor
  telegram.go                 Telegram bot command — wires odek agent into Telegram poller
  subagent.go                 Sub-agent command (--goal, --context, --task) + flag parsing/limits
  subagent_tool.go            delegate_tasks built-in tool (sub-agent spawning)
  subagent_key.go             FD-based API key handoff (parent → sub-agent, never via env)
  browser_tool.go             Built-in browser tool (HTTP fetch + headless navigation)
  file_tool.go                Built-in file tools (read_file, write_file, search_files, patch, batch_read, glob, file_info)
  perf_tools.go               Performance/parallelism tools (batch_patch, parallel_shell, http_batch, math_eval, diff, count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count)
  mcp.go                      MCP server implementation (stdio + SSE transport)
  mcp_approval.go             Per-tool MCP server approval UI and persistence
  transcribe_tool.go          Whisper.cpp audio transcription
  vision_tool.go              Vision / image-input tool
  web_search_tool.go          Web search tool
  session_search_tool.go      Session search tool
  wsapprover.go               WebSocket interactive approval relay (with friction + class-trust gates)
  refs.go                     @-resource reference resolution (files, sessions)
  untrusted.go                <untrusted_content_<nonce>> wrapper + per-call ingest recorder
  audit.go                    Per-turn audit + `odek audit` subcommand (divergence heuristic)
  sandbox_file.go             Sandbox-aware file-tool bridge
  ssrf_guard.go               URL / SSRF validation helpers
  skill_promote.go            `odek skill promote` — clear NeedsReview on a tainted skill
  schedule.go                 `odek schedule` command and scheduler wiring
  memory_cmd.go               `odek memory` command
  parallel.go                 Parallelism helpers
  toolctx.go                  Tool-call context plumbing
  security_report_validation_test.go  Regression bar for every documented mitigation
  *_test.go                   250+ unit + E2E tests covering all tools
internal/
  llm/                        OpenAI-compatible HTTP client with reasoning_content support
  loop/                       ReAct engine: observe → think → parallel-act → repeat. signal.go — SignalEvent observability (context_trimmed, tool_recovery).
  tool/                       Thread-safe tool registry, clarify.go, send_message.go
  danger/                     Command/URL classification + bypass-resistant tokenizer. TTYApprover with friction mode.
  auth/                       Interactive approval system
  memory/                     MemoryManager (facts, buffer, episodes, merge, scan). EpisodeProvenance — tainted episodes never auto-replayed.
  session/                    Session store (CRUD, trim, cleanup, compact JSON). AuditStore + divergence heuristic.
  skills/                     Skill system (types, loader, triggers, self-improve, curator, import, cache). SkillProvenance gate.
  config/                     Config file loading, env vars, secrets.env, priority merge
  telegram/                   Telegram bot: bot.go, poller.go, handler.go, commands.go, session.go, health.go, plan.go, media_path.go
  render/                     Terminal output and narrator support
  narrate/                    LLM-powered emoji-rich progress messages
  redact/                     Secret redaction (20+ patterns)
  mcp/                        MCP server handler (tools/list, tools/call, SSE streaming)
  mcpclient/                  MCP client (connect to external MCP servers)
  sandbox/                    Docker sandbox lifecycle
  flock/                      Advisory file-locking helpers
  fsatomic/                   Atomic file-write helpers
  pathutil/                   Path helpers
  resource/                   @-resource resolver (files, sessions) with size/symlink hardening
  transport/                  Shared HTTP transport with connection pooling
  ws/                         RFC 6455 WebSocket framing
docs/                         Documentation (CLI, API, CONFIG, MCP, MEMORY, TELEGRAM, SECURITY, etc.)
benchmark/                    AIEB v2.0 benchmark suite (9 tasks, 4 tiers, automated scoring)
```

## How It Works

### Agent Loop (`internal/loop/loop.go`)
ReAct cycle: observe → think → act → repeat.
- LLM returns tool calls or a final answer.
- **Parallel tool execution** — multiple independent tool calls run concurrently (`max_tool_parallel`, default: 4).
- **Batch approval gate** (`internal/loop/loop.go`) — multiple risky tools shown at once in a single prompt. `classifyToolCall` now classifies every command inside `parallel_shell`, every path inside `batch_patch`, and the modern `browser` tool, shows full (untruncated) commands, and withholds blanket `SetTrustAll` when unclassifiable tools remain in the iteration.
- **Tool-failure recovery** — systematic recovery from tool call failures: retry transient errors, skip permanently failed tools, and continue the loop without crashing.
- **Context-limit protection** — `trimToSurvival` drops oldest messages when approaching the model's context window, keeping the agent functional under extended sessions. Tool messages stay grouped with their parent assistant message.
- **Interaction modes** — engaging (narrated), enhance (persistent), verbose (raw), off.
- Max 300 iterations by default.
- **Post-response async processing** — skill learning and episode extraction run in background goroutines, eliminating the hang after every `odek run`.
- **Artifact-aware file search** — `search_files` and `multi_grep` skip build/artifact directories (`node_modules`, `vendor`, `.git`, `__pycache__`, `.venv`, etc.) automatically, reducing noise and speeding scans.
- **Semantic session search** — the `session_search` tool uses go-vector RandomProjections + k-NN for semantic similarity search through session content, with a two-tier pipeline: vector index (fast, ~1ms) → deepSearch fallback (exhaustive, slower).
- **Security-first defaults** — the latest hardening closes the high/medium/low findings tracked in `sec_findings.md`: default `non_interactive` is `deny`, project-level `odek.json` cannot redirect backends or hijack delivery, `~/.odek` trust anchors are protected, WebSocket upgrades require a per-instance CSRF token, and all untrusted content is wrapped before reaching the model. See Security Architecture below for the full list.

### Tools
All built-in tools with zero subprocess forks: batch_read, batch_patch, parallel_shell, http_batch, math_eval, diff, count_lines, multi_grep, json_query, tree, checksum, sort, head_tail, base64, tr, word_count, transcribe, browser, read_file, write_file, search_files, patch, shell, delegate_tasks, session_search.

### Terminal Rendering (`internal/render/`)
Vertical space compression — `Start()` is a no-op; blank lines removed from Iteration/FinalAnswer/Summary. Raw-mode cursor uses `\r\n` instead of bare `\n` for cross-platform compatibility.

### Identity
System prompt is loaded by priority: `--system` flag > `~/.odek/IDENTITY.md` > compiled-in defaultSystem. The default is a concise identity focused on TDD workflow, tool discipline, and safety rules.

### Security Architecture
Layered prompt-injection / approval-fatigue defenses. Full reference: [docs/SECURITY.md](docs/SECURITY.md).

- **Untrusted-content wrapper** (`cmd/odek/untrusted.go`) — every tool whose output sources from outside the trust boundary (`browser`, `read_file`, `shell`, `search_files`, `multi_grep`, `transcribe`, `head_tail`, `diff`, `tr`, `sort`, `json_query`, `batch_patch`, `glob`, `file_info`, `tree`, `base64` file mode, `session_search`, `@-resources`, `--ctx` files, any MCP tool) wraps results in `<untrusted_content_<nonce> source="...">…</untrusted_content_<nonce>>`. Browser page title and interactive-element text are wrapped in addition to the main content. Per-call nonce defeats wrapper-escape via literal close tag.
- **Audit log** (`cmd/odek/audit.go` + `internal/session/audit.go`) — every `wrapUntrusted` call records source + content-hash + turn into `<sessions>/audit/<id>.json`. After each turn a divergence heuristic flags `suspicious_divergence=true` when the agent ingested untrusted content AND its actions or final response reference resources that either did not appear in the user's message or were introduced by the untrusted content itself (closing response-only exfiltration and reused-resource injection bypasses). Inspect with `odek audit <session-id>` / `odek audit --list`.
- **Memory taint** (`internal/memory/provenance.go`) — `EpisodeProvenance` tracks Untrusted/Sources/UserApproved. Tainted episodes are stored but `Search()` filters them out, so a one-shot injection cannot persist via the episode pipeline. User must explicitly promote.
- **Skill provenance gate** (`internal/skills/loader.go` + `cache.go` + `tools.go`) — `Skill.Provenance{Untrusted, Sources, NeedsReview}`. NeedsReview skills pin to Lazy regardless of `auto_load`. The auto-save path declines tainted suggestions by default. Agent-created skills via `skill_save` and patched skills via `skill_patch` are forced to `Untrusted` + `NeedsReview`, and `skill_patch` refuses edits that touch the YAML frontmatter, blocking an injected agent from flipping `auto_load` or clearing `needs_review`. `odek skill promote <name> --force` clears the flag after explicit user review.
- **Sub-agent damage cap** (`cmd/odek/subagent.go::applySubagentTrust`) — `delegate_tasks` carries `trust_level` + `max_risk`. Untrusted ⇒ NonInteractive=deny, Destructive/CodeExec/Install/SystemWrite/NetworkEgress all forced to Deny. `max_risk` ⇒ everything above cap forced to Deny.
- **FD-based API key handoff** (`cmd/odek/subagent_key.go`) — parent writes key to a 0600 tempfile, immediately `unlink()`s, passes the FD via `cmd.ExtraFiles`. Sub-agent reads from `$ODEK_API_KEY_FD` and closes. Key never in `/proc/<pid>/environ`.
- **Approver friction** (`internal/danger/approver.go`, `cmd/odek/wsapprover.go`) — both TTYApprover and WSApprover engage friction mode after 3 approvals of the same class in 60s: require typing literal `approve`, 1.5s pause. Trust-class shortcut disabled for `destructive` + `blocked` regardless.
- **Danger classifier bypass resistance** (`internal/danger/classifier.go`) — `normalize()` pre-processes: expand `$IFS` / `${IFS}`, extract `$(...)` / `` `...` `` substitutions, strip `command` / `exec` / `builtin` wrappers, collapse unquoted backslashes, basename absolute paths. `awk`/`gawk`, `sed` (`e` command / `-f`), and editors (`vi`/`vim`/`nvim`/`emacs`/`ed`/`ex`) are classified as `code_execution` when given a script or file operand, closing `awk 'BEGIN{system(...)}'`, `sed 's///e'`, and editor `!` shell escapes. Regression suite in `classifier_bypass_test.go`.
- **WebSocket CSRF token** (`cmd/odek/serve.go`) — `odek serve` issues a random 256-bit token at startup and requires it on `/ws` via cookie, `X-Odek-Ws-Token` header, or `odek.<token>` subprotocol. The token is no longer embedded in every `GET /` response; it is only delivered when the request includes the correct `?token=` query parameter (Jupyter-style), and a non-loopback bind prints a loud warning. The localhost origin check remains as defense-in-depth.
- **SSRF / DNS-rebinding dial guard** (`cmd/odek/ssrf_guard.go` + `internal/danger/classifier.go`) — `browser`, `http_batch`, and `web_search` resolve hostnames at dial time and refuse internal IPs (loopback, RFC1918, RFC4193, link-local, RFC 6598 CGNAT `100.64.0.0/10`, RFC 2544 benchmark `198.18.0.0/15`, and unspecified), then pin the dial to the validated IP. Operator-configured backends (e.g. `web_search.base_url`) are added to an allowlist so container-internal services such as SearXNG remain reachable.
- **REST API CSRF protection** (`cmd/odek/serve.go::requireLocalOrigin`) — state-changing HTTP endpoints (POST/PUT/PATCH/DELETE) require a localhost origin or no Origin header, and static responses set `X-Frame-Options: DENY` + `Content-Security-Policy: frame-ancestors 'none'` to block clickjacking.
- **Browser history cap** (`cmd/odek/browser_tool.go`) — navigation history is capped at 50 snapshots to prevent memory DoS from repeated `browser_navigate` calls.
- **Browser element cap** (`cmd/odek/browser_tool.go`) — the number of interactive elements extracted per page is capped at 500 so a hostile page cannot OOM the agent with thousands of links or buttons.
- **Search path classification** (`cmd/odek/file_tool.go`, `cmd/odek/perf_tools.go`) — `search_files` and `multi_grep` classify every descended directory and every discovered file the same way the search root is classified. Sensitive paths that would require approval (or are denied) are skipped and reported in the `skipped` field rather than returned silently, closing the gap where a broad search root auto-approved sensitive files.
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
- **Session vector index hardening** (`internal/session/vector_index.go`) — `rebuildLocked` validates every session filename with `ValidateSessionID` and skips symlinks via `DirEntry.Type()` and `os.Lstat`, preventing a planted symlink from embedding arbitrary files into the semantic search corpus.
- **@-resource / --ctx prompt wrapping** (`cmd/odek/refs.go`, `cmd/odek/serve.go`) — content resolved from `@file` references and `--ctx` files is wrapped as untrusted before being inserted into the prompt.
- **Config file size cap** (`internal/config/loader.go`) — `~/.odek/config.json` and `./odek.json` are rejected if larger than 5 MiB to prevent OOM from a malicious or broken config at startup.
- **Resource resolver size cap** (`internal/resource/resource.go`) — `@-resource` file loads are capped at 1 MiB to prevent OOM from `@hugefile` references.
- **Resource resolver search hardening** (`internal/resource/resource.go`) — `FileResolver.Search` rejects queries containing `..`, path separators, or absolute components before joining them with the workspace root, and uses `filepath.WalkDir` so directory symlinks are not followed during recursive autocomplete. `os.Lstat` (not `os.Stat`) is used for search-result metadata, so symlinks cannot leak the size of arbitrary targets outside the workspace.
- **Sub-agent summary cap + wrapping** (`cmd/odek/subagent_tool.go`) — each sub-agent result included in the `delegate_tasks` summary is truncated to 100 KiB to prevent memory DoS, and the final aggregated summary is wrapped as untrusted content so a compromised sub-agent cannot inject instructions into the parent context.
- **Tree path wrapping** (`cmd/odek/perf_tools.go`) — the `tree` tool wraps every filesystem-derived path as untrusted content.
- **head_tail output cap** (`cmd/odek/perf_tools.go`) — `head_tail` truncates returned lines so total content stays within 1 MiB, preventing multi-file/multi-line memory DoS.
- **search_files symlink hardening** (`cmd/odek/file_tool.go`) — the `files` target uses `Lstat` (not `Stat`) and skips symlinks in the glob branch, closing metadata disclosure via symlinked paths.
- **AGENTS.md size cap** (`odek.go`) — project-level `AGENTS.md` is ignored if larger than 256 KiB to prevent OOM/prompt stuffing from a malicious repo.
- **System prompt injection scan** (`cmd/odek/main.go`) — explicit `--system` / `ODEK_SYSTEM` / `~/.odek/config.json` system prompts, as well as `~/.odek/IDENTITY.md`, are capped at 256 KiB and scanned with `danger.ScanInjection`; a failed scan falls back to the compiled-in default identity.
- **patch / batch_patch output expansion cap** (`cmd/odek/file_tool.go`, `cmd/odek/perf_tools.go`) — the post-replacement result is capped at 10 MiB so `ReplaceAll` cannot explode memory.
- **write_file content cap** (`cmd/odek/file_tool.go`) — the `content` argument is capped at 1 MiB to prevent disk exhaustion and memory pressure from a single enormous tool call.
- **file_info confinement + wrapping** (`cmd/odek/file_tool.go`) — `file_info` respects the same `restrictToCWD` path confinement as `write_file`/`patch`, and the returned path is wrapped as untrusted content.
- **WebSocket message-size cap** (`cmd/odek/serve.go`) — `odek serve` sets `MaxPayloadBytes` on every WebSocket connection so a local client cannot OOM the server with a huge frame.
- **Session file size cap** (`internal/session/session.go`) — session files larger than 32 MiB are rejected by `Load()` to prevent OOM from tampered or corrupted transcripts.
- **Skill file size cap** (`internal/skills/loader.go`) — `SKILL.md` files larger than 1 MiB are skipped so a malicious project cannot OOM the process at startup or bloat the system prompt.
- **Serve sandbox default-on** — `odek serve` enables `--sandbox` automatically unless `--no-sandbox` is passed.
- **Sandbox volume confinement** (`internal/sandbox/sandbox.go`) — extra `--sandbox-volume` host paths must resolve to a location under the working directory, cannot contain `..` or symlink escapes, and cannot match sensitive prefixes such as `/etc`, `/proc`, `/sys`, `/dev`, `/root`, `/home`, `/var`, `/run`, or `/var/run/docker.sock`.
- **Sandbox read-only enforcement** (`cmd/odek/sandbox_file.go` + `cmd/odek/file_tool.go` + `cmd/odek/perf_tools.go`) — when a sandbox container is active, `write_file`, `patch`, and `batch_patch` translate host paths to `/workspace/...` and copy data into the container with `docker cp`, so a read-only workspace mount (`--sandbox-readonly`) is enforced for the agent's own file tools.
- **Project config sensitive-field rejection** (`internal/config/loader.go`) — `./odek.json` is untrusted, so `base_url`, `api_key`, `system`, the `dangerous` section, `embedding`, `memory`, `sessions`, `skills.dirs`/`skills.embedding`, `telegram`, and `web_search` set there are ignored (with stderr warnings). These can only be configured from operator-controlled sources: `~/.odek/config.json`, `ODEK_*` env vars, or CLI flags.
- **MCP subprocess environment sanitisation** (`internal/mcpclient/client.go`) — MCP server children receive only a minimal allowlist of safe environment variables plus explicit `env` overrides. Keys matching secret patterns (`*_API_KEY`, `*_TOKEN`, `*_SECRET`, `*_PASSWORD`, etc.) are stripped, preventing a compromised or malicious MCP server from reading parent secrets.
- **MCP `tools/list` metadata hardening** (`internal/mcpclient/client.go`, `cmd/odek/mcp_approval.go`, `cmd/odek/main.go`) — tool names from MCP servers are validated (ASCII letters/digits/`-`/`_`, ≤ 64 chars) and descriptions are scanned for injection patterns. Tools whose raw names collide with odek built-ins are rejected. Each tool from a project-level server requires per-tool approval (interactive, `ODEK_APPROVE_MCP=1`, or persisted in `~/.odek/mcp_tool_approvals.json`); global servers from `~/.odek/config.json` are operator-trusted. Approval keys hash the server's `env` map (sorted key/value pairs), and the interactive prompt prints each env variable with its value, so a later `env` change cannot silently reuse an old approval.
- **Read-only perf-tool file-size cap** (`cmd/odek/perf_tools.go`) — `count_lines`, `checksum`, `head_tail`, and `word_count` reject files larger than 10 MiB before scanning/hashing, consistent with other perf tools.
- **Inline content size cap** (`cmd/odek/perf_tools.go`) — `base64` and `tr` reject inline `string`/`content` arguments larger than 10 MiB, preventing prompt-injected multi-hundred-megabyte payloads from OOMing the process.
- **Schedule atomic-write hardening** (`internal/schedule/store.go` + `internal/fsatomic`) — schedule file writes now use `fsatomic.WriteFile`, which creates a random temp file with `O_EXCL`, fsyncs data and directory, and renames over the target. A swapped-in symlink is replaced rather than followed, closing the symlink-override attack on `schedules.json` / `schedule-state.json`.
- **Schedule cross-process lock hard error** (`internal/schedule/store.go`) — `fileLock` now returns an error instead of silently falling back to a no-op releaser when `~/.odek/schedules.lock` cannot be opened or locked. Mutating schedule operations abort rather than risk two concurrent processes loading the same baseline and overwriting each other.
- **Schedule JSON file-size cap** (`internal/schedule/store.go`) — `schedules.json` and `schedule-state.json` are rejected if larger than 10 MiB before being read into memory, preventing a tampered multi-gigabyte file from OOMing the scheduler.
- **Default non-interactive policy is deny** (`internal/danger/classifier.go`, `cmd/odek/main.go`) — headless/CI/piped runs no longer auto-approve dangerous operations. Operators must explicitly set `non_interactive: "allow"` for unattended execution.
- **`~/.odek` trust-anchor protection** (`internal/danger/classifier.go`, `cmd/odek/file_tool.go`) — generic file tools reject writes to `~/.odek/config.json`, `secrets.env`, `IDENTITY.md`, `skills/`, `sessions/`, `audit/`, `plans/`, `schedules.json`, `schedule-state.json`, `mcp_approvals.json`, `mcp_tool_approvals.json`, `restart.json`, `telegram.lock`, and related state files. These paths classify as `system_write` and must be modified through their dedicated subsystems. Matching is case-insensitive so variants such as `CONFIG.JSON` or `SECRETS.ENV` are also blocked on case-insensitive filesystems (e.g. macOS APFS). Shell reads of these trust anchors are also escalated to `system_write`.
- **Nonce'd tool-result delimiter** (`internal/loop/loop.go`) — the static `┌── TOOL RESULT: ... └── END TOOL RESULT: ...` delimiter is now unique per tool call via a random hex nonce embedded in both the opening and closing lines. A tool or MCP server can no longer forge the closing delimiter to break out of the data framing and inject instructions.
- **`parallel_shell` context + process-group kill** (`cmd/odek/perf_tools.go`) — commands now run via `exec.CommandContext` bound to the agent context, in their own process group. Cancellation or timeout kills the whole group (negative PID), so `sh -c 'sleep 3600 &'` cannot leave orphaned children. Per-command timeouts are also capped at 30 minutes.
- **`batch_patch` trusted-class propagation** (`cmd/odek/perf_tools.go`) — `batch_patch` now passes its cached `trustedClasses` to `CheckOperation`, matching `write_file` and `patch`. A trusted `local_write` class is honored across all patches in the batch instead of re-prompting per patch.
- **Browser link URL wrapping** (`cmd/odek/browser_tool.go`) — interactive element text was already wrapped as untrusted, but link URLs in `clickableRef.URL` were returned raw. They are now wrapped too, while an unexported `rawURL` is kept for internal click resolution.
- **Telegram message length by UTF-16 code units** (`internal/telegram/handler.go`) — `MaxMsgLength` is enforced using UTF-16 code-unit counting, matching Telegram's own limits. Multi-byte UTF-8 characters (e.g. emoji) no longer pass the local check while being rejected by Telegram.
- **Telegram restart marker permissions** (`cmd/odek/telegram.go`) — `~/.odek/restart.json` is now written with `0600` instead of `0644`, preventing local users from reading the list of active chat IDs.
- **Telegram singleton flock lock** (`cmd/odek/telegram.go` + `internal/flock`) — the Telegram bot now uses an advisory `flock` on `~/.odek/telegram.lock` instead of a PID file probed with signals. This removes the non-Linux path where a planted PID could cause odek to kill an arbitrary process.
- **Telegram photo caption wrapping** (`cmd/odek/telegram.go`) — photo captions cross the Telegram trust boundary, so they are wrapped as untrusted content both when passed to the local vision model and when injected into the main agent's user message.
- **`send_message` callback prefix restriction** (`internal/tool/send_message.go` + `cmd/odek/telegram.go`) — the `send_message` tool rejects any button whose `callback_data` starts with a reserved internal prefix (`apr:`, `den:`, `trs:`, `clarify:`, `skill_save:`, `skill_skip:`); only user-facing `cb:` callbacks are allowed. The Telegram sender closure validates again as defense-in-depth, preventing a forged approval or skill button.
- **Telegram class-trust guard + friction** (`internal/telegram/approver.go`) — the "Trust Session" button is hidden for `destructive`, `blocked`, `unknown`, and the synthetic `tool_batch` class, and a trust callback for those classes is treated as a denial. After 3 approvals of the same class in 60 seconds, friction mode hides the Trust Session shortcut and adds a warning, forcing per-call approval and breaking reflexive tap-through.
- **Telegram outbound media hardening** (`internal/telegram/media_path.go` + `internal/telegram/approver.go` + `internal/telegram/handler.go` + `internal/tool/send_message.go` + `cmd/odek/telegram.go`) — paths supplied to `MEDIA:...` prefixes or `send_message(file=...)` are resolved to an absolute path and verified against an allowlist (cwd, `~/.odek/media/`, system temp dir). On Unix the final component is opened with `O_NOFOLLOW` and `fstat`'d to avoid a symlink TOCTOU race; `filepath.EvalSymlinks` ensures the resolved path does not escape the allowlist. Additionally, well-known secret subtrees (`~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.odek` trust anchors, etc.) and any file whose basename starts with `.env` are rejected outright, and every outbound media upload now requires explicit user approval via `TelegramApprover.PromptMedia`, with an extra warning when the bot was launched from `$HOME` or `/`.
- **Telegram plan file size cap** (`internal/telegram/plan.go`) — plan files larger than 1 MiB are rejected by `ReadPlan` and `MostRecentPlan`, and `ListPlans` only reads the first 8 KiB for preview. This prevents a prompt-injected agent from causing an OOM via a multi-hundred-megabyte plan file.
- **Telegram log file permissions** (`internal/telegram/log.go`) — Telegram log files are created with `0600`, and `os.Chmod` hardens existing files. Chat IDs and task snippets are no longer world-readable by default.
- **Telegram chat-scoped sessions and plans** (`internal/telegram/session.go`, `internal/telegram/plan.go`, `cmd/odek/telegram.go`) — `/sessions`, `/resume`, `/prune`, `/plans`, `/plan_view`, `/plan_delete`, and `/plan_resume` are scoped to the requesting chat. Sessions are filtered by the `tg-<chatID>` prefix, `ResumeSession` rejects cross-chat IDs, and plans live under `~/.odek/plans/chat<chatID>/` so one chat cannot list, read, delete, or resume another chat's sessions/plans.
- **odek self-invocation gate** (`internal/danger/classifier.go`) — any shell stage whose program basename is `odek` is classified as `system_write`. This prevents a prompt-injected agent from reaching the human-gated trust mutations (`odek memory promote`, `odek memory extended confirm`, `odek skill promote --force`) through the shell tool and flipping its own taint gates.
- **MCP inputSchema hardening** (`cmd/odek/mcp_approval.go`) — every string in an MCP tool's `inputSchema` is recursively guard-scanned for injection patterns; schemas larger than 256 KiB are rejected; the interactive approval prompt shows a SHA-256 hash and byte size of the schema so operators can detect changes.
- **MCP tool batch classification** (`internal/loop/loop.go`) — MCP tools (`<server>__<tool>`) are classified as `unknown` by `classifyToolCall`, so the batch approval gate shows them and untrusted sub-agents force them to `deny`.
- **Sub-agent trust defaults + delegate_tasks gate** (`cmd/odek/subagent.go`, `internal/loop/loop.go`) — a missing `trust_level` in `delegate_tasks` defaults to `untrusted`; `delegate_tasks` itself is classified as `system_write` so it requires explicit approval before spawning child processes.
- **Memory add/replace pipe-to-shell filter** (`internal/memory/memory.go`) — `AddFact` and `ReplaceFact` now run `FactLooksUnsafe` in addition to the general guard scan, blocking agent-driven planting of download-and-execute facts.
- **Skill learn-loop provenance propagation** (`internal/skills/learnloop.go`) — conversation-extracted suggestions and LLM-enhanced suggestions both retain the session's `SkillProvenance`, so tainted sessions cannot produce clean-looking auto-saved skills.
- **Audit ingest recording for @-refs, `--ctx`, and Web-UI attachments** (`cmd/odek/refs.go`, `cmd/odek/serve.go`, `cmd/odek/audit.go`) — the per-session ingest recorder is attached before `@`-reference/`--ctx`/attachment resolution, `recordTurnAudit` scans `user` messages for untrusted wrappers in addition to `tool` messages, and the divergence heuristic compares agent actions against the original pre-enrichment prompt so attacker-injected resources are not treated as user-mentioned.
- **Session ID entropy + session-scoped auth tokens** (`internal/session/session.go`, `cmd/odek/serve.go`) — session IDs now carry 128 bits of randomness (16 bytes / 32 hex chars); each session stores a 256-bit `AuthToken` required by `GET/DELETE/POST /api/sessions/<id>`, `POST /api/cancel`, and WebSocket session-resume messages via `X-Session-Token` header, `session_token` cookie, or `auth_token` WS field. Per-IP rate limiting (60/min) on session lookups adds a brute-force backstop.
- **Skill/episode untrusted wrapper** (`internal/loop/loop.go` + `odek.go`) — skill context and retrieved session-episode context are passed through the caller-provided untrusted wrapper (the same nonce'd `<untrusted_content_*>` boundary used for tool output) before being injected into the model's system context. This prevents a compromised or tainted skill/episode from being treated as trusted system instructions.
- **`env` / `printenv` environment-dump gate** (`internal/danger/classifier.go`) — bare `env` and `printenv` invocations are classified as `system_write` because they can leak process-environment secrets that the redaction scanner does not recognise. `env VAR=value <cmd>` still classifies `<cmd>` normally.
- **Web UI attachment wrapping** (`cmd/odek/serve.go` + `cmd/odek/ui/app.js`) — files attached through the browser are sent separately from the user's text and wrapped with the nonce'd `<untrusted_content_*>` boundary (`source="attachment:<filename>"`) before injection into the prompt.
- **Episode index session ID validation** (`internal/memory/episode_index.go` + `internal/session/session.go`) — `readAllSummaries` treats `index.json` as untrusted input and validates every `session_id` with `session.ValidateSessionID` before building the `filepath.Join(dir, sessionID+".md")` path. Invalid / traversal / separator-containing IDs are skipped with a warning, preventing a tampered episode index from pulling arbitrary files (e.g. `~/.odek/config.json`, `IDENTITY.md`) into the embedding space.
- **Telegram health bind warning** (`internal/telegram/health.go`) — warns when the health server binds to a non-loopback address, so operators notice accidental network exposure.
- **Web UI prompt/model validation** (`cmd/odek/serve.go`) — server-side cap on WebSocket prompt size (1 MiB) and validation of model ID length/characters, preventing oversized or unusual payloads from reaching the LLM.
- **Sub-agent runaway limits** (`cmd/odek/subagent.go`) — `--timeout` is capped at 3600s and `--max-iter` at 100, so a single `odek subagent` invocation cannot run indefinitely.
- **Secrets.env permission gate** (`internal/config/loader.go`) — refuses to load `~/.odek/secrets.env` when it is group/world-readable, preventing local users from reading API keys injected into the environment.
- **git config code execution** (`internal/danger/classifier.go`) — `git -c alias.*=!<cmd>`, `git -c core.pager=...`, `git -c core.fsmonitor=...`, `git -c credential.helper=...`, and the `git config` subcommand are classified as `code_execution` because they can inject arbitrary shell commands.
- **find / rsync destructive flags** (`internal/danger/classifier.go`) — `find -delete` and `rsync --delete` / `--del` / `--remove-source-files` are classified as `destructive`; `find -fprint` / `-fprintf` are classified as `local_write`.
- **Shell operand path classification** (`internal/danger/classifier.go`) — file operands and redirect targets of shell commands are checked with `ClassifyPath`, so writes to `~/.bashrc`, `~/.zshrc`, `~/.profile`, `~/.ssh`, `~/.odek`, etc. are escalated to `system_write` instead of auto-allowed `local_write`.
- **Secret redaction** (`internal/redact/redact.go`) — 20+ patterns: OpenAI, Anthropic, GitHub PAT, AWS, PEM, JWT, Vault, Google OAuth, SendGrid, Discord, DB URLs, etc.

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
```

Note: MCP client E2E tests build the fakeserver from `internal/mcpclient/testdata/main.go` at test time (no pre-compiled binary). macOS temp dirs are classified as `LocalWrite` (not `SystemWrite`), and the Docker availability check verifies daemon reachability before running sandbox tests.
