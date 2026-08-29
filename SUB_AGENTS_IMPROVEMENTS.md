# odek Sub-Agents — Review & Improvement Plan

**Date:** 2026-08-28 · **Scope:** `cmd/odek/subagent.go`, `subagent_tool.go`, `subagent_key.go`,
`internal/budget`, `internal/loop` (budget + partial-summary machinery), `runtime.go`,
`main.go` (`builtinTools`), `docs/SUBAGENTS.md`, `docs/CONFIG.md`, `docs/CLI.md`.

---

## 1. Current architecture (what odek has today)

```
parent ReAct loop
  └─ delegate_tasks (in-process tool, ≤8 tasks/call)
       ├─ writes each task to an unlinked odek-temp JSON file
       ├─ spawns `odek subagent --task <file> --quiet --stream`  (os/exec, OS process)
       │    ├─ fixed system prompt (trust boundary; parent text never enters it)
       │    ├─ untrusted tasks: nonce'd <untrusted_input_<n>> fence + trust clamps
       │    ├─ API key via FD 3 (unlinked tempfile), secrets.env stripped from env
       │    ├─ context.WithTimeout(120s default) + os.Interrupt signal handling
       │    └─ budget.Checker inherited from operator limits (v1.24.0 audit fix)
       ├─ semaphore = global MaxConcurrency; drains ALL tasks before returning
       └─ collated JSON results → wrapped untrusted → returned to parent
```

Per-task controls today: `goal`, `context`, `guidance`, `trust_level`, `max_risk`.
Result contract: `{status, error, summary, files_changed, tokens_used, iterations, parent_session}`.

### What odek already does better than Claude Code

Worth stating plainly — the comparison is not one-sided:

- **Real process isolation.** Sub-agents are OS processes with separate heaps; a panic kills
  only that sub-agent. Claude Code sub-agents are in-process. odek's model is structurally
  more robust and we should keep it.
- **Explicit hard budgets.** odek has typed execution budgets (runtime, tool calls, tokens,
  cost) with a budget Checker consulted at enforcement points. Claude Code bounds sub-agent
  turns internally but exposes no explicit budget concept. The gap is not the existence of
  budgets — it's that odek never *tells the agent about them* (see M1).
- **Per-task trust plumbing.** `trust_level` / `max_risk` → forced deny classes, MCP servers
  withheld from untrusted sub-agents, results re-wrapped as untrusted for the parent, FD-based
  key handoff. Claude Code has permission modes per agent but nothing this granular per task.
- **Fail-closed defaults** throughout (unknown risk class = deny, untrusted ⇒ non-interactive).

### Defects and gaps found during review (evidence)

| # | Finding | Evidence |
|---|---------|----------|
| F1 | Sub-agent is never told its lifespan. It learns about the 120s/15-iteration limits only by dying. | `subagent.go:37-59` (fixed prompt has no limits block); `runtime.go:18` (RuntimeContext = host/cwd/date only) |
| F2 | Wall-clock timeout = SIGKILL mid-iteration. No finalization window, no partial summary. Contrast: iteration-budget exhaustion gets a bounded partial-summary side-call. | `subagent_tool.go:180` (`context.WithTimeout` → `CommandContext` kill), `subagent.go:406`; `internal/loop/loop.go:1174-1190` (iteration-budget summarizer exists) |
| F3 | The documented `subagent` config section (`max_concurrency`, `timeout_seconds`, `max_iterations`) is dead code — `parseSubagentConfig` has zero call sites. Tool timeout is hardcoded 120s; concurrency comes from the *global* MaxConcurrency. Docs admit it ("may be wired up fully in a future release"). | `subagent.go:136-163` (unreferenced), `main.go:2233-2238` (hardcoded), `docs/CONFIG.md:660` |
| F4 | Tool schema text misleads the model: "configurable via max_concurrency" (parent can't configure it) and "Each sub-agent has 120s" (hardcoded, not configurable per call). | `subagent_tool.go:47-63` |
| F5 | `--parent-session` / `ParentSession` correlation field exists but `delegate_tasks` never passes it. | `subagent.go:225-229`, `subagent_tool.go:205-210` |
| F6 | Result metrics are crude: `tokens_used = len(content)/4` (no tool results, no reasoning, no schema overhead); `files_changed` regex-scans assistant prose for "wrote/created/…" prefixes while the loop could report actual mutations; no cost, no duration. | `subagent.go:482-486, 554-577` |
| F7 | Delegation depth is unbounded — sub-agents receive `delegate_tasks` themselves and can fan out recursively. | `subagent.go:358` (full `builtinTools`), `subagent_tool.go:139-148` |
| F8 | Budget inheritance semantics: a child inherits the *operator's* caps (`resolved.Limits`), not the parent's *remaining* budget. A parent 5s from its own runtime cap can spawn children with full fresh budgets. | `subagent.go:424-428, 447` |
| F9 | Blocking batch semantics: parent waits for all ≤8 tasks; no background mode, no mid-run steering, no progress relay to the parent loop (NDJSON goes to Web UI only), no resume — sub-agent sessions are not persisted, so a timeout loses everything for a retry. | `subagent_tool.go:150-153, 39-43`; no persist callback in `subagent.go:429-448` |
| F10 | Budget-exhausted sub-agent reports a generic `status:"error"` — indistinguishable from a model failure in the result contract. | `subagent.go:497-507` |

---

## 2. Claude Code comparison (feature by feature)

Features marked ⚠ are version-specific / evolving in Claude Code and should be re-verified
against the installed build before being treated as commitments.

| Feature | odek today | Claude Code | Verdict |
|---|---|---|---|
| Isolation model | OS process, separate heap | In-process | **odek leads** — keep processes |
| Budgets | Explicit, typed, enforced (but unannounced, F1/F2) | Implicit turn bounds only | **odek leads** on mechanism; M1 closes the awareness gap |
| Lifespan awareness | None (agent discovers limits by dying) | Context-window notices ("context low", auto-compact warnings) keep the agent aware of *context* budget | **Claude Code leads** in awareness UX; M1 ports the pattern to time/iterations |
| Fresh context + single report back | Yes | Yes | Parity |
| Per-task controls | goal/context/guidance/trust/risk-cap | `description`, `prompt`, `subagent_type`, `model` (per-agent), `run_in_background`, ⚠ `isolation: "worktree"`, ⚠ `name` | **Claude Code leads** — M2 adds per-task lifespan/model/tools |
| Custom agent definitions | None (one fixed prompt) | `.claude/agents/*.md` + user-level; frontmatter: name/description/tools/model; auto-delegation by description | **Claude Code leads** — M3 adds `.odek/agents/` (explicit selection first) |
| Background execution | None (blocking batch) | `run_in_background` → TaskOutput polling, TaskStop | **Claude Code leads** — M3 |
| Mid-run steering | None | ⚠ message-to-running-agent (evolving) | **Claude Code leads** — M3 (design gated on background mode) |
| Progress visibility | NDJSON → Web UI only; parent sees nothing until batch drains | Live tool streaming (foreground); TaskOutput (background) | **Claude Code leads** — M3 |
| Parallel edits | None — concurrent sub-agents share the working tree | ⚠ `isolation: "worktree"` (git worktree per agent) | **Claude Code leads** — M4 (exploratory) |
| Resume / persistence | None for sub-agents | Background agents observable while running; ⚠ resume semantics evolving | Partial gap — M3 |
| Trust/provenance per task | trust_level, max_risk, MCP withholding, untrusted fences, FD key handoff | Permission modes per agent; hooks | **odek leads** |
| Delegation depth control | Unbounded (F7) | Bounded in practice by turn limits | Gap — M1 (hardening, cheap) |

**Net assessment:** odek's *foundation* (process isolation + typed budgets + per-task trust) is
stronger; Claude Code's *orchestration ergonomics* (per-agent specialization, background
execution, steering, progress, worktrees) are far ahead. The plan below closes the awareness
gap first (the principal's explicit priority), then the ergonomics — without importing
Claude Code's in-process model or its auto-delegation magic (explicit selection is auditable;
auto-delegation can wait).

---

## 3. Improvement plan

Milestones are strictly sequential (M1 → M2 → M3 → M4), matching the established working
convention. Every item: failing test first, then implementation, then docs in the same commit.

### M1 — Lifespan awareness & graceful end-of-life *(the core ask)*

#### M1.1 Announce lifespan at spawn (static awareness)

Every sub-agent learns its budget in the system prompt, before its first token.

- Build a **Runtime Constraints block** in `subagentCmd` from *numeric* values only:
  - `--timeout` / config timeout (wall clock, hard kill time)
  - `--max-iter` (think→act cycles)
  - inherited `Limits` when set (runtime, tool calls, input/output tokens, cost)
- Policy text steers behavior: *"Budget: 240s, 20 iterations. Plan to conclude within it.
  When roughly 25% remains, stop starting new work — consolidate and write your final
  report."*
- **Security invariant (pin with a test):** the block is assembled from ints/durations
  computed by code. Goal/context/guidance strings are NEVER interpolated into it (the fixed
  prompt stays a trust boundary). Test: build the block with a hostile goal string, assert the
  goal text never appears.
- Implementation: parameterize the prompt assembly in `subagent.go` (append the block after
  the fixed constant); do not touch `subagentSystem`'s trust rules.
- Effort: small. Files: `cmd/odek/subagent.go`, `cmd/odek/subagent_test.go` (or contract test).

#### M1.2 In-loop budget telemetry (dynamic awareness)

The engine tells the agent how much life remains as it spends it.

- At iteration boundaries, when the remaining fraction crosses **50% / 75% / 90%** of
  `max_iterations` (or remaining wall clock crosses the same fractions of the soft deadline —
  75% used = the "~25% remaining" conclude-policy), append a one-line engine hint to the next
  tool result: *"⚠ 12 of 15 iterations used — wrap up and deliver."*
- Reuse the existing corrective-hint injection pathway (stall detection already appends
  engine hints to tool results) and `SignalEvent` plumbing — fire a new `budget_warning`
  signal for observability. Hints are engine-trusted; do NOT wrap them in the untrusted
  wrapper (they are instructions *from* the runtime, same trust as stall hints).
- Config: `subagent.announce_budget` (default true). Also announce when `Limits` are active.
- Effort: medium. Files: `internal/loop/loop.go` (hint injection + thresholds),
  `internal/loop/signal.go` (new signal), tests in `internal/loop`.

#### M1.3 Graceful finalization window on timeout (kill → conclude)

Convert the current dead-loss SIGKILL into a planned conclusion.

- Two-stage deadline inside `subagentCmd`:
  - **soft** = `timeout − finalizationWindow`, `finalizationWindow = min(15s, timeout/8)`
  - **hard** = `timeout` (SIGKILL backstop, unchanged)
- On soft deadline: the engine stops starting new tool batches and makes one bounded,
  tool-less LLM call to produce the final report — mechanically reuse the iteration-budget
  summarizer (`loop.go:1174-1190`, 30s cap clamped to the remaining window), marker
  `[Time budget reached — partial summary]`.
- Result contract: `status: "partial"` + `partial_reason: "time_budget" | "iteration_budget" |
  "execution_budget"`; exit code stays `2` for timeout (callers unaffected; additive JSON).
- Effort: medium. Files: `cmd/odek/subagent.go` (two ctxs), `internal/loop` (expose a
  `RequestFinalization(ctx)` or reuse the budget-exhausted path with a synthetic error),
  `subagent_tool.go` (surface `status: partial` distinctly in the parent summary).

#### M1.4 Wire the `subagent` config section (close documented gap F3)

- `parseSubagentConfig` already exists — call it from `LoadConfig`-derived state where tools
  are built; thread `timeout_seconds`, `max_iterations`, `max_concurrency` into
  `delegateTasksTool` (`main.go:2233-2238` replaces the hardcoded 120s).
- Keep the documented clamp ceiling: per-task values ≤3600s / ≤100 iters (`subagent.go:236-248`).
- Delete or explicitly ignore the obsolete `subagentConfig.SystemPrompt` field — the system
  prompt is now a fixed trust boundary and must not be configurable; leave a comment saying why.
- Update `docs/CONFIG.md:660` (remove the "not wired" note).

#### M1.5 Budget sharing: pass the parent's *remaining* budget down (F8)

Sub-agents should not outlive their parent's affordability.

- The delegate tool snapshots the parent engine's budget Checker (expose
  `agent.RemainingBudget()` or a snapshot via the existing toolctx plumbing) and writes a
  `budget` block into the task file: remaining runtime, tool calls, cost.
- The child enforces `min(operator cap, parent-remaining)` and **announces the effective
  numbers in its lifespan block (M1.1)** — awareness and enforcement stay consistent.
- Modes: `subagent.budget_inherit: "operator" (default today) | "share"`. Default flips to
  `"share"` after a release of soak time.

#### M1.6 Delegation depth cap (F7 — cheap hardening)

- `ODEK_SUBAGENT_DEPTH` env counter incremented per spawn; refuse delegation at
  `subagent.max_depth` (default 2). Sub-agent result gets
  `"error":"delegation depth limit reached"`. The env counter is the single canonical
  mechanism (the Section-5 task-file `depth` sketch field is dropped — the child's own
  depth is process state, not parent-declared data).
- Files: `subagent_tool.go` (read+increment via env at spawn), `subagent.go` (export next
  depth), tests in `subagent_contract_test.go`.

**M1 acceptance:** a sub-agent can answer "how much time/iterations do I have left?" at any
point without a tool call; a timeout produces a partial report instead of nothing; config
values actually configure something; a child can never spend more than its parent could.

### M2 — Delegation controls & result contract v2

#### M2.1 Per-task lifespan knobs
`delegate_tasks` task objects gain `timeout_seconds` and `max_iterations`, clamped by the
operator ceilings (≤3600s / ≤100) and by the wired config defaults (M1.4). They flow through
the task file → CLI flags → lifespan announcement. The parent can now right-size each task,
and M1.1 makes the child aware of its allotment. Files: `subagent_tool.go` (schema + task
file), `subagent.go` (flags already exist — just wire per-task values into them).

#### M2.2 Per-task model override
Task field `model` (profile name from `KnownProfiles` or a raw model ID, validated). Add a
`--model` flag to `odek subagent` overriding `resolved.Model` in `aCfg`. Use case: fan out
cheap research tasks on a small model, keep synthesis on the strong one. Claude Code parity:
per-agent `model`.

#### M2.3 Per-task tool restrictions
Task field `tools: {enabled?: [...], disabled?: [...]}` → `filterBuiltinTools` in
`subagentCmd` (the filter already supports both lists). Read-only research agents become
expressible (`disabled: ["shell","write_file","patch",...]`) without touching trust classes.

#### M2.4 Result contract v2 (fixes F6, F10)
- `files_changed`: report real mutations — the loop/registry knows actual `write_file`/`patch`
  targets; plumb via the agent handle (e.g. collect from tool-call records) instead of
  regex-scanning prose. Keep the regex only as fallback for external `shell` writes.
- `tokens_used`: use budget Checker actuals (provider-reported usage) — no more `len/4`.
- Add `cost_usd` (when prices configured), `duration_seconds`, `status: "partial"` +
  `partial_reason` (from M1.3), `budget_exhausted` as a distinct status for typed budget
  errors (F10).
- Fix the schema Description() text (F4) in the same commit: state the *actual* defaults,
  remove "configurable via max_concurrency" until M2.1 exists.

#### M2.5 Wire `--parent-session` (F5)
`delegate_tasks` passes a correlation ID (parent session ID + task index) →
`subagent --parent-session` → echoed in the result. Prerequisite for M3 progress/steering.

#### M2.6 Batch policy controls
Batch-level `on_error: "continue" (default) | "cancel"`: on a critical child failure, cancel
siblings via the existing parent-ctx cancellation path instead of burning the full timeout on
doomed work.

### M3 — Execution model: background, steering, resume, roles

#### M3.1 Background mode (`mode: "background"`)
- Task field `mode: "foreground" (default) | "background"`. Background tasks return
  immediately with task IDs; the parent keeps its loop and polls later — Claude Code's
  `run_in_background` / TaskOutput parity.
- Registry: `~/.odek/subagents/<parent-session>/<task-id>.json` — status machine
  `running → done | partial | error`, result payload, PID, timestamps. Conventions already in
  repo: `fsatomic` writes, `0600`, no-symlink checks, `flock` for updates.
- New built-in tool `subagent_status` (poll/await IDs, returns wrapped-untrusted payloads —
  identical wrapping to the foreground path).
- Cancellation: parent exit kills tracked PIDs (defer); orphan sweep joins the maintenance
  janitor rather than a new daemon.

#### M3.2 Mid-run steering
- `subagent steer <task-id> "message"` (CLI) or a `subagent_send` built-in; delivery via a
  control file the child polls at iteration boundaries → injected as a user-turn
  ("Message from orchestrator: …", wrapped untrusted when applicable).
- Design constraint: injection only at iteration boundaries (never mid-tool-call), bounded
  queue (last N=3), and the child's lifespan is *not* extended by steering.
- Gated on M3.1 (you steer what runs in the background).

#### M3.3 Progress relay (events, not just Web UI)
- Forward child NDJSON (`--stream` already emits tool events) into `odek.event/v1` as
  `subagent_progress` (args hashed/sized only, redaction applied — the event contract already
  mandates this). Uniform visibility across CLI/Web/Telegram; parents no longer blind until
  the batch drains.

#### M3.4 Sub-agent session persistence & resume
- Set the per-turn persist callback + a session directory for `odek subagent` runs; return
  `session_id` in the result. A timed-out/partial sub-agent becomes `odek continue <session>`
  with fresh budget instead of a from-scratch retry. Watch: session file caps + retention
  interplay with the maintenance janitor; sub-agent sessions should inherit a shorter
  retention class.

#### M3.5 Named role agents (`.odek/agents/*.md`, project scope first)
- Frontmatter: `{description, tools?, model?, max_iterations?, timeout_seconds?, max_risk?,
  trust_level?}`; body = role guidance. `delegate_tasks` gains `agent: "<name>"`.
- **Trust boundary preserved:** body text is injected as *guidance in the user request*, never
  spliced into the fixed system prompt. Loader mirrors the skills loader: provenance gate,
  `danger.ScanInjection` on content, 256 KiB cap, NeedsReview until promoted, project-local
  first (project-specific knowledge stays in the project; promote to global only for generic
  knowledge — established convention).
- Explicit selection only; auto-delegation by description matching is deliberately deferred.

### M4 — Exploratory (post-M3 discussion before any implementation)

- **Worktree isolation** (Claude Code ⚠ `isolation: "worktree"` analog): optional per-task git
  worktree + merge-back guidance so parallel sub-agents don't stomp the same files. Requires
  git; heavy interaction with the danger classifier (worktree ops are git verbs). Lighter
  interim: per-task scratch subdir convention documented in SUBAGENTS.md.
- **Auto-delegation**: LLM-selected agent types by description. Only after M3.5 has real usage.
- **Peer teams / mesh topology**: out of scope; parent-child with explicit budgets is the
  model that fits odek's security posture.

---

## 4. Suggested test plan (TDD, per milestone)

| Milestone | Tests |
|---|---|
| M1.1 | Lifespan block contains timeout/iter numbers; hostile goal string never appears in the block; absent limits render "unlimited" honestly |
| M1.2 | Hint injected exactly at 50/80/100% thresholds (loop unit tests w/ fake LLM); hints are not untrusted-wrapped; `budget_warning` signal emitted |
| M1.3 | Timeout with soft window reached → `status:"partial"`, marker present, summary non-empty; hard-kill path unchanged; finalization LLM call bounded by min(30s, remaining) |
| M1.4 | Config values reach the tool (timeout/concurrency/iterations); clamp ceiling still enforced; project config cannot raise ceilings |
| M1.5 | Child checker = min(operator, parent-remaining); parent near cap → child denied early with typed error |
| M1.6 | Depth N+1 refused; env counter propagates through nested spawns |
| M2 | Schema round-trip tests per new field; model/tool validation rejects bad values; contract test pins result-v2 fields |
| M3 | Registry file conventions (0600, no symlink, atomic write); poll/await semantics; steering delivers at iteration boundary only; resume continues a partial session |

Regression bar: extend `cmd/odek/security_report_validation_test.go` for the new trust-relevant
surfaces (steering messages, agent-file loading) and keep
`TestDefaultSystem_PassesOwnInjectionScan`-style scanner pinning for any new prompt text.

---

## 5. Contract sketches

Task file v2 (additive; all new fields optional):

```json
{
  "goal": "…", "context": "…", "guidance": "…",
  "trust_level": "untrusted", "max_risk": "local_write",
  "timeout_seconds": 240,
  "max_iterations": 20,
  "model": "gpt-5-mini",
  "tools": { "disabled": ["shell", "write_file", "patch"] },
  "budget": { "max_runtime_seconds": 200, "max_tool_calls": 40 },
  "mode": "foreground",
  "parent_session": "20260828-…",
  "depth": 1
}
```

Result v2 (additive):

```json
{
  "status": "success | partial | budget_exhausted | error",
  "partial_reason": "time_budget | iteration_budget | execution_budget",
  "summary": "…",
  "files_changed": ["…"],
  "tokens_used": 4200,
  "cost_usd": 0.012,
  "iterations": 3,
  "duration_seconds": 41.2,
  "session_id": "20260828-…",
  "parent_session": "20260828-…"
}
```

Lifespan block (system prompt, code-computed numbers only):

```
## Runtime Constraints (enforced by the runtime, not negotiable)
- Wall-clock budget: 240s (hard stop; a finalization window is reserved near the end)
- Iteration budget: 20 think→act cycles
- Budget policy: conclude within budget. At ~25% remaining, stop starting new work,
  consolidate findings, and write your final report.
```

---

## 6. What we deliberately do NOT copy from Claude Code

- **In-process sub-agents** — process isolation is a odek security asset; keep it.
- **Auto-delegation magic** — explicit `agent:` selection is auditable; magic delegation can
  wait until role files (M3.5) prove out.
- **Unbounded nesting** — Claude Code leans on turn limits; we add an explicit depth cap (M1.6)
  because our budgets are explicit and should compose predictably.
