# FIX_PLAN.md

Remediation plan for bugs and security vulnerabilities identified in the
security/correctness audit (2026-06-10). Findings are ordered by priority.
Each entry lists the location, the flaw, the concrete fix, and verification
steps. Check items off as they land.

## Priority order (fix these first)

1. **#1** — Telegram document path traversal (remote arbitrary-write/RCE). ✅ Fixed
2. **#3** — Fail-open Telegram authorization (amplifies #1). ✅ Fixed
3. **#2** — `trustAll` never resets within a run (one click unlocks the turn). ✅ Fixed
4. **#4 / #5** — WS auth + serve-mode approval deadlock.

> Status: #1, #2, and #3 are implemented and tested in this PR. The remaining
> items are open. #4 and #6 involve a behavior/default change (WS token) and are
> left for maintainer direction.

---

## 1. Path traversal → arbitrary file write via Telegram document filename
- **Status:** ✅ Fixed — `sanitizeDocName` strips directory components; tests in
  `download_test.go` (`TestSanitizeDocName`, `TestDownloadDocument_NoTraversal`).
- **Severity:** Critical
- **Location:** `internal/telegram/download.go:156-164`
- **Flaw:** `DownloadDocument` uses the sender-controlled `Document.FileName`
  directly: `safeName := fileName; localPath := filepath.Join(dir, safeName)`.
  `filepath.Join` collapses `../`, so a document named
  `../../../home/<user>/.odek/config.json` (or `../../.ssh/authorized_keys`)
  writes outside `~/.odek/media/`. Both path and contents are attacker-
  controlled → RCE/persistence. Photo/voice paths are safe (hashed names).
- **Fix:**
  - `safeName = filepath.Base(filepath.Clean(fileName))`.
  - Reject if the result is empty, `.`, `..`, or still contains a path
    separator; fall back to the generated `doc_<fileID>` name.
  - Confirm the final `localPath` is still within `dir` (defense in depth):
    resolve both with `filepath.Abs` and check `strings.HasPrefix`.
- **Verify:** unit test in `internal/telegram` sending a `FileName` of
  `../../evil.txt` and asserting the write stays under `~/.odek/media/`.

## 2. Approval bypass: `trustAll` never resets within a run
- **Status:** ✅ Fixed — grant is now reset at the end of each iteration's tool
  phase (not via `defer`). Regression test:
  `TestBatchApprovalTrustAllNotLeakedAcrossIterations` in `loop_test.go`.
- **Severity:** High
- **Location:** `internal/loop/loop.go:885-891`
- **Flaw:** `defer ta.SetTrustAll(false)` sits inside the
  `for i := 0; i < e.maxIter` loop, so it fires at function return, not at
  iteration end. After one approved batch, all later dangerous ops auto-approve
  for the rest of the turn (incl. destructive/blocked classes).
- **Fix:** scope the reset to the iteration — set `trustAll` true before tool
  execution and `false` immediately after (e.g. wrap the phase in a closure, or
  add an explicit `ta.SetTrustAll(false)` at the end of the iteration body).
  Do not rely on `defer` inside the loop.
- **Verify:** test that a second dangerous tool call in a later iteration of the
  same run still triggers `PromptCommand`.

## 3. Fail-open Telegram authorization
- **Status:** ✅ Fixed — `ValidateConfig` now refuses to start with no allowlist
  unless `ODEK_TELEGRAM_ALLOW_ALL=true` is set; `isAllowed` is fail-closed
  (empty lists + no opt-in → deny); startup logs a loud warning when running
  open. **Also closed a callback-query authorization bypass** found while
  challenging the fix: `handleCallback` (inline-button presses) did not call
  `isAllowed` — it now does, so callbacks are gated like messages. Tests:
  `TestValidateConfig_noAllowlistFailsClosed`, `TestValidateConfig_allowAllOptIn`,
  `TestConfigFromEnv_allowAll`, `TestIsAllowed_EmptyAllowlist` (now asserts deny),
  `TestIsAllowed_EmptyAllowlistWithAllowAll`, `TestHandleCallback_RespectsAllowlist`,
  `TestHandleUpdate_CallbackQueryNotAllowed` (now asserts deny).
- **Note (not fixed — pre-existing, lower priority):** in a *group* chat where
  only `AllowedChats` is set, any group member can press another user's
  approval/clarify buttons (callback passes the chat-level allowlist). Tightening
  this requires binding an approval to the specific user who triggered the run —
  out of scope for this PR.
- **Severity:** High
- **Location:** `internal/telegram/handler.go:505-533`; defaults in
  `internal/telegram/config.go`
- **Flaw:** `isAllowed` returns `true` when both `AllowedChats` and
  `AllowedUsers` are empty, and the default config leaves both empty with no
  warning. Anyone who finds the bot gets full (possibly godmode) access.
- **Fix:** make the bot fail-closed — if no allow-list is configured, either
  refuse to start (log a fatal/clear error) or run with tools disabled.
  Emit a loud startup warning when the allow-list is empty.
- **Verify:** start the bot with no `ODEK_TELEGRAM_ALLOWED_*` env vars and
  confirm it refuses / restricts rather than serving all users.

## 4. WebSocket `/ws` (and `/api/*`) has no authentication
- **Severity:** High
- **Location:** `cmd/odek/serve.go:148,874-888` (and the `/api/*` handlers
  ~904-1057)
- **Flaw:** the only gate is an Origin check that returns `nil` for empty
  Origin, so any non-browser client (no Origin header) gets full agent control.
  `/api/*` handlers have no Origin check at all. Safety depends solely on the
  loopback bind; `--addr 0.0.0.0` removes it.
- **Fix:**
  - Add a per-process bearer/session token required on the WS handshake and all
    `/api/*` requests (generate on startup, print to the operator).
  - Reject non-loopback `RemoteAddr` unless a token is explicitly configured.
  - Apply the same origin/token gate to every `/api/*` handler.
- **Verify:** connect with `websocat` (no Origin) and confirm rejection without
  a token; confirm `--addr 0.0.0.0` still requires the token.

## 5. Web UI approvals deadlock and always time out
- **Severity:** High
- **Location:** `cmd/odek/serve.go:494-590`; `cmd/odek/wsapprover.go:153-184`
- **Flaw:** `handleWS` is the only goroutine reading the socket and calls
  `RunWithMessages` synchronously. The approver blocks on `HandleResponse`,
  which is only called from that same (now-blocked) receive loop, so
  `approval_response` is never read → 60s timeout → denial. Approval UI in serve
  mode is dead.
- **Fix:** read the WebSocket on a dedicated goroutine (channel-feed the receive
  loop), or run the prompt on a separate goroutine from the socket reader so
  responses can be processed while a prompt is in flight.
- **Verify:** in serve mode, trigger a dangerous tool and confirm the browser
  approval is received and honored without timing out.

## 6. `write_file`/`patch` `~/.odek/` carve-out enables privilege escalation
- **Severity:** High
- **Location:** `cmd/odek/file_tool.go:760-768`;
  `internal/danger/classifier.go:177-186`; `patch` wiring in `cmd/odek/main.go`
- **Flaw:** `confineToCWD` allows any path under `~/.odek/`, and
  `~/.odek/config.json` classifies as auto-allowed `LocalWrite`. A confined/
  untrusted sub-agent can rewrite its own config to disable the sandbox / enable
  YOLO on the next run, or drop an auto-loaded `SKILL.md`. Shell rc files
  (`~/.bashrc`, `~/.zshrc`, `~/.profile`) also fall through to `LocalWrite`.
  `patch` is created without `restrictToCWD`.
- **Fix:**
  - Classify `~/.odek/config.json`, `~/.odek/skills/`, and the shell rc/profile
    files (and crontab paths) as `SystemWrite` (prompt/deny).
  - Exclude those paths from the `confineToCWD` carve-out.
  - Give `patch` the same `restrictToCWD` confinement as `write_file`.
- **Verify:** confined agent attempt to write `~/.odek/config.json` and
  `~/.bashrc` must hit an approval/deny path, not auto-allow.

## 7. Data race in `wsApprover` can fatally crash serve
- **Severity:** Medium
- **Location:** `cmd/odek/wsapprover.go:117,175`
- **Flaw:** `a.approveAll[cls] || a.trustAll` read unlocked at :117 while
  `a.approveAll[cls] = true` writes unlocked at :175. Parallel tool goroutines
  share one approver → concurrent map read/write is a fatal Go runtime error.
- **Fix:** guard all `approveAll`/`trustAll` access with the existing mutex
  (mirror `internal/telegram/approver.go`, which locks correctly).
- **Verify:** run the serve approval path under `go test -race` with parallel
  tool calls.

## 8. SSRF: URL gate is string-only, no dial-time IP check
- **Severity:** Medium
- **Location:** `internal/danger/classifier.go:195-244`;
  `cmd/odek/browser_tool.go`; `cmd/odek/perf_tools.go`
- **Flaw:** `ClassifyURL` inspects only the literal hostname. A domain whose A
  record resolves to `169.254.169.254` / `10.x` / `192.168.x` classifies as
  plain `NetworkEgress`, and the HTTP clients use a default transport with no
  post-resolution IP guard → SSRF / DNS-rebinding to cloud metadata & internal
  services when egress is allowed.
- **Fix:** install a custom `DialContext` on the browser/http_batch clients that
  re-checks the resolved `net.IP` against loopback/private/link-local/ULA ranges
  and refuses them. This also closes the redirect-hop and rebinding window.
- **Verify:** request a host that resolves to `169.254.169.254` and confirm the
  dial is refused.

## 9. Sub-agent results lost on >64KB NDJSON line, then full-timeout hang
- **Severity:** Medium
- **Location:** `cmd/odek/subagent_tool.go:247-285`
- **Flaw:** `runTask` reads child stdout with a default `bufio.Scanner` (64KB
  token cap). Streamed `tool_call` events embedding full tool args (e.g. a large
  `write_file`) exceed 64KB → `ErrTooLong` → reader stops → child blocks on a
  full pipe → `cmd.Wait` hangs until the 120s timeout kills a task that actually
  succeeded.
- **Fix:** call `scanner.Buffer(make([]byte, 0, 64*1024), maxCap)` with a large
  cap, or switch to a `bufio.Reader` with `ReadString('\n')`.
- **Verify:** sub-agent task whose streamed event line exceeds 64KB returns its
  result instead of timing out.

## 10. `/new` deletes the per-chat mutex while a run holds it
- **Severity:** High (correctness/data-integrity)
- **Location:** `cmd/odek/telegram.go:263`
- **Flaw:** `/new` runs `chatMu.Delete(chatID)` on the update loop,
  unsynchronized with in-flight `handleChatMessage` goroutines. The next message
  `LoadOrStore`s a fresh mutex and `TryLock`s it → two concurrent runs for the
  same chat corrupt interleaved `sessionManager.Save` writes and clobber each
  other's approver.
- **Fix:** do not delete the mutex on `/new` (a small per-chat leak is
  acceptable), or only delete it while holding the lock and with no active run.
- **Verify:** send `/new` during an active run, then a message; confirm the two
  runs do not execute concurrently.

---

## Secondary findings (track, lower priority)

- **Telegram bot token leaked into logs** — `internal/telegram/bot.go:117-120`.
  `*url.Error` includes the token-bearing URL. Strip credentials from logged
  URLs (log method only).
- **`currentPromptCancel` single global slot** — `cmd/odek/serve.go:37,581`.
  `/api/cancel` cancels the wrong prompt with multiple tabs; A finishing clobbers
  B's cancel. Track per-connection / per-prompt-ID.
- **`/stop` cannot interrupt a pending `clarify`** — `cmd/odek/telegram.go:1353`.
  Circular wait until the 10-minute timeout. Make clarify observe `agentCtx`.
- **Sub-agent / panic edge cases in loop** —
  `internal/loop/loop.go:927-945`: a panicked tool returns an empty result
  (early `return` before `results[idx]` is set); the model gets no error.
- **UTF-8 byte-slicing in Telegram previews** —
  `cmd/odek/telegram.go:305-327`. `taskPreview[:50]`/`[:80]` slice by byte and
  can emit invalid UTF-8 (Telegram 400). Also `Messages[0]` can index an empty
  slice and previews the system prompt. Use rune-aware truncation (see
  `truncate()` in `subagent.go`).
- **`activeTaskWG.Add` races `gracefulRestart`'s `Wait`** —
  `cmd/odek/telegram.go:1063-1076` vs `:879-961`. Invert to Add-first,
  re-check-flag, Done+return on bail.
- **`serve --open` never opens a browser** —
  `cmd/odek/serve.go:1104-1113`. `os.StartProcess` with a bare name does no PATH
  lookup. Use `exec.LookPath` / `exec.Command`.
- **SearXNG `secret_key` weak sentinel** —
  `docker/searxng/settings.yml` ships `ultrasecretkey` with a matching compose
  default. Mitigated (no host port) but should hard-fail on the sentinel.
- **Sessions dir created `0755`** — `internal/session/session.go:71`. Tighten to
  `0700` (files are already `0600`).
- **`ffprobe` leading-dash arg injection** — `cmd/odek/vision_tool.go:101-106`.
  Low impact; add a `--` separator before positional paths.

## Operational follow-up

- **Rotate the credentials** in the working-tree `docker/.env`
  (`ODEK_API_KEY`, `ODEK_TELEGRAM_BOT_TOKEN`) if this tree was ever shared,
  imaged, or backed up. (`docker/.env` is gitignored and was never committed —
  only `.env.example` is tracked.)

## Confirmed NOT vulnerable (do not re-investigate)

- `shell.go` — argv-form `exec.Command` + token-based fail-closed danger
  classifier.
- File tools — `O_NOFOLLOW` + atomic temp-write/rename (symlink TOCTOU closed).
- `subagent_key.go` — API key handed off via an unlinked FD, not env/args.
- `mcp.go` — MCP servers come from local resolved config, not remote-supplied
  definitions; tool descriptions are injection-scanned and outputs wrapped.
- `docker/.env` — gitignored, never committed (see operational follow-up for the
  on-disk copy).
