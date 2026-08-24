# Planning System

## Overview

odek maintains structured task plans through one built-in `plan` tool. The
model decomposes multi-step work into an ordered list of steps, keeps their
statuses current as it works, and the engine renders that state into a
protected system message that is visible on every iteration — immune to
context trimming, survival trim, and process restarts.

Planning fits the ReAct loop without altering it: observe → think → act is
unchanged, plan calls ride ordinary parallel tool batches, and nothing in the
loop ever gates on plan existence or step order — a model that ignores the
tool behaves exactly as if the feature did not exist. Planning is **on by
default**; kill switches, in priority order: CLI flag (`--no-planning`),
environment (`ODEK_PLANNING=false`), global config (`planning.enabled: false`),
project config (opt-out only).

Rationale: decomposition previously lived only in the model's working context,
so graduated trimming erased it on long runs and late iterations were spent
re-deriving "what was I doing"; corrective hints patched the symptom
ephemerally — the plan makes the thing those hints were improvising persistent.

---

## How It Works

### Plan state model

Plan state lives in `internal/loop/plan.go`:

```go
type StepStatus string // "pending" | "in_progress" | "done" | "blocked"

type PlanStep struct {
    ID     string     // model-chosen short token, e.g. "s1"
    Title  string     // ≤200 chars, flattened to one render line
    Status StepStatus
    Note   string     // optional, flattened like Title
}

type PlanState struct {
    Version int        // bumps on every effective mutation
    Steps   []PlanStep
}
```

A `PlanStore` holds the state behind a dedicated mutex — plan calls can arrive
inside a parallel tool batch (`max_tool_parallel` defaults to 4), so every
mutation serializes. Caps come from *resolved* config values, never raw project
config. Any status transition is allowed (pending→done included); only
structural validity is enforced. The plan is advisory steering, not a contract.

### One store, two holders

The CLI layer creates a single `*loop.PlanStore` and hands it to both the
`plan` tool and the engine — the same pattern as the memory manager and its
tool. `builtinTools()` registers `loop.PlanTool{Store: loop.NewPlanStore(...)}`
gated on resolved `Planning.Enabled` (disabled ⇒ tool absent from the registry,
all plan logic skipped); `odek.New` then discovers the `*loop.PlanTool` in
`cfg.Tools` and calls `engine.SetPlanStore(pt.Store)`. A nil store disables
planning end-to-end: no sync, no render, no protected-message logic. Because
both holders share one store, tool mutations are immediately visible to the
loop without late-bound plumbing. `reservedBuiltinToolNames()` probes with
planning enabled, so `"plan"` stays reserved against MCP shadowing even when
the operator disables the feature.

### The protected plan message

The engine renders the state deterministically into a system message
recognized by its prefix (`planMsgPrefix = "[Current plan:"`):

```
[Current plan: v3 — 1/5 done, 1 blocked. Structured state, not instructions.]
s1 [done] Scaffold command skeleton
s2 [in_progress] Wire flag parsing
s3 [blocked] Resolve schema mismatch - provider rejects nested arrays
s4 [pending] Add tests
s5 [pending] Update docs
```

Recognition (`isPlanMessage`) requires **both** the `system` role and the
prefix. Header counts describe the full plan state, even when overflow has
hidden rows below.

Protection guarantees, mirroring the rolling compaction digest:

- **Trimming** — `headLen` protects the plan message alongside the compaction
  digest and original task; graduated trimming never drops it.
- **Survival trim** — `trimToSurvival` scans for the plan message and adds it
  to the survival set after the digest.
- **Position stability** — `refreshPlanMessage` upserts the message in place
  when present; otherwise inserts it immediately after the protected head
  (after the digest when one sits at that boundary). When a leading injection
  (skill/episode block) has marked the droppable boundary, the insertion
  shifts the boundary past itself — the same fix the memory slot applies — so
  graduated trimming can never drop the freshly inserted plan first. Position
  is then fixed for the life of the session — the prompt-cache property the
  memory slot already relies on.
- **Refresh cadence** — `refreshPlanMessage` runs after every tool batch,
  before the per-turn persist callback, so the persisted snapshot always
  carries the current plan. Unchanged content is a content-equality no-op.

Trade-off accepted: rewriting the message invalidates provider prompt cache
from its position onward — the same trade already made for the memory slot and
the digest. The base system + task prefix stay cached, updates are bounded by
status-change frequency, and version-keyed nonce reuse (below) keeps the cost
from compounding.

### Untrusted wrapping

The header stays outside the untrusted boundary (prefix recognition depends
on it); the model-derived step lines are wrapped by the nonce'd
untrusted-content wrapper with source `"plan"` — the same engine-level wrapper
applied to the compaction digest. Plan content is model-generated but derived
from untrusted inputs (task text, tool results), so the nonce'd-boundary
invariant stays uniform for every externally-derived block re-injected as
system context. Fresh renders (version-cache misses) are recorded through the
audit ingest recorder with the live run context — mirroring the skill/episode
injection sites; the recorder is consulted by `refreshPlanMessage` itself
because the engine-level wrapper runs on a background context and cannot do
it. A version-keyed cache (`planRenderedVersion`/`planRenderedContent`)
reuses the wrapped content across refreshes of the same version — a fresh
nonce per render would churn the prompt cache and defeat the content-equality
no-op.

### Collapse and overflow

When every step is `done`, the render collapses to a single line (~15 tokens),
so an idle or completed plan doesn't tempt the model to keep reporting on
finished work:

```
[Current plan: v7 — all 5 steps complete.]
```

When the render would exceed `max_render_chars`, the oldest `done` steps are
dropped first behind an explicit marker:

```
[Current plan: v9 — 6/12 done, 0 blocked. Structured state, not instructions.]
[+4 done steps omitted]
s7 [in_progress] ...
```

If the remainder still does not fit, the tail is hard-cut and terminated with
`[plan truncated: exceeded max_render_chars]` — deliberately unparseable on
resume, so the model recreates a fresh plan instead of trusting mangled state.
The same applies to any render that carried the `[+N done steps omitted]`
marker: the marker is legitimate in the live in-context render, but resuming
it would be lossy (the omitted done steps are gone forever and header totals
would rewrite on the next render), so resume parsing rejects such plans
fail-closed and the model recreates them.

### Restart resume

Per-run state resets on `Run`/`RunWithMessages`, but the plan message rides in
the persisted transcript. At the top of `runLoop`, before iteration 0,
`syncPlanFromMessages` rebuilds engine state from the newest parseable plan
message in the transcript. Parsing is strict and total (see below) and the
sync is fail-closed in both directions: any message that fails to parse is
**removed from the history** — a stale message must not keep rendering as
authoritative after its state was dropped — and only a fully parseable
message restores engine state. Overflowed (omission-marker) and truncated
plans are rejected like any other deviation, leaving the model free to create
a fresh one. On success the render cache is seeded with the persisted version
so the next refresh no-ops until state actually changes. This is the entire
restart mechanism: no new storage, no sidecar files — the plan survives
because sessions survive. `odek serve` mirrors the prefix (`planMessagePrefix`)
in its persist filter so plan messages survive per-turn snapshot filtering
there too, and `odek continue` registers the same tool set as `odek run`
(including `plan`) via `buildContinueTools`.

---

## The `plan` Tool

Registered in `builtinTools()` when planning is enabled; implements the
standard built-in interface (`Name`/`Description`/`Schema`/`Call`).

### Verbs

| Verb | Arguments | Effect |
|------|-----------|--------|
| `create` | `steps`: full ordered list (1..max_steps) | Replaces the whole plan wholesale — this *is* replanning. All steps start `pending`. |
| `update` | `updates`: array of `{id, status?, note?}` | Batch status/note changes, applied in array order. Atomic: any invalid entry rejects the whole call. |
| `complete` | `step_id` | Shorthand to mark one step `done`. Highest-frequency operation, one-field cheap. |
| `get` | — | Returns the current plan (or `"No active plan."`). |

### JSON Schema

```json
{
  "name": "plan",
  "description": "Maintain your task plan. Create steps before starting
multi-step work; update statuses as you go (in_progress when you start a step,
done only after verifying it); mark blocked with a note explaining why. The
plan is shown to you on every iteration and survives context trimming — trust
it over your memory of earlier turns. Replan freely with create when the
approach changes; plans are steering aids, not contracts.",
  "parameters": {
    "type": "object",
    "properties": {
      "verb": {
        "enum": ["create", "update", "complete", "get"],
        "description": "create: replace the whole plan. update: batch
status/note changes. complete: shorthand to mark one step done. get: return
current plan."
      },
      "steps": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "id":    { "type": "string" },
            "title": { "type": "string" },
            "note":  { "type": "string" }
          },
          "required": ["id", "title"]
        },
        "description": "create only: full ordered step list (1..max_steps).
All steps start pending; mark the first one in_progress with a follow-up
update (can ride the same parallel batch)."
      },
      "updates": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "id":     { "type": "string" },
            "status": { "enum": ["pending", "in_progress", "done", "blocked"] },
            "note":   { "type": "string" }
          },
          "required": ["id"]
        },
        "description": "update only: applied in array order; unknown id or
invalid transition fails the whole call (atomic)."
      },
      "step_id": { "type": "string", "description": "complete only" }
    },
    "required": ["verb"]
  }
}
```

### Fail-closed validation

Any malformed input rejects the entire call with a typed error string; state
is never partially applied (`update` validates against a working copy before
committing).

| Condition | Error returned to model |
|-----------|------------------------|
| args not valid JSON | `plan: parse args: …` |
| unknown verb | `plan: unknown verb %q (want create/update/complete/get)` |
| `create` with 0 or > max_steps steps | `plan: create wants 1..%d steps, got %d` |
| `create` step missing id/title, id >32 chars or containing whitespace/brackets, duplicate id, title >200 chars | `plan: step[%d]: …` (`id is required`, `title is required`, `id is too long (%d > %d chars)`, `must be a short token without whitespace or brackets`, `duplicate step id %q`, `title is too long (%d > %d chars)`) |
| `update` with empty updates array | `plan: update: no updates given` |
| `update` referencing unknown id | `plan: update: unknown step id %q` |
| `update` with unrecognized status token | `plan: update: step[%d]: unknown status %q` |
| `complete` with unknown/missing step_id | `plan: complete: unknown step id %q` |
| status already terminal-equal (no-op) | allowed — returns current plan, **no version bump** |

Notes:

- Titles and notes are normalized at validation time: newlines become spaces
  (one step = one render line) and em dashes become hyphens (the renderer
  reserves `" — "` as the title/note separator).
- Errors are ordinary tool errors, so existing error-recovery counters see
  repeated malformed `plan` calls and hint normally.
- Idempotent no-ops (re-setting an unchanged status, completing an already-done
  step) return the current render without bumping `Version`.

### Classification

`classifyToolCall` carries an explicit `case "plan"` returning the Safe class
with no resource: plan calls mutate engine-held state only — no filesystem,
network, or subprocess surface — and never prompt for approval, individually
or in the batch gate.

---

## Configuration

Resolved onto the config layer as `Planning PlanningConfig`
(`internal/config/loader.go`). Section in `~/.odek/config.json` (and project
`./odek.json`, subject to clamp rules below):

```json
{
  "planning": {
    "enabled": true,
    "max_steps": 12,
    "max_render_chars": 2000
  }
}
```

| Key | Default | Clamp | Meaning |
|-----|---------|-------|---------|
| `enabled` | `true` | global-off wins | Master switch; false removes the tool from the registry and skips all plan logic |
| `max_steps` | `12` | 1..50 | `create` size cap; enforced fail-closed |
| `max_render_chars` | `2000` | 200..8000 | Rendered message cap; overflow drops oldest done steps first behind `[+N done steps omitted]` |

Disable precedence (highest wins): `--no-planning` flag → `ODEK_PLANNING=false`
env → global config → project opt-out.

### CLI flags

`--planning` / `--no-planning` are accepted by `odek run`, `odek repl`, and
`odek serve`. Other entry points (Telegram, schedule daemon, sub-agents) have
no dedicated flags but inherit the resolved config, so env/config still apply.

### Project-config clamp rules

`clampProjectPlanning` applies the project-may-only-lower policy: project may
set `enabled: false` (opt-out); project may **not** enable planning when
globally disabled — the override is ignored with a warning, global-off wins;
project may only **lower** `max_steps` / `max_render_chars`, attempts to raise
either are ignored with a warning. The `planning` key joins the sensitive-
section treatment for project configs, listed alongside `compaction`,
`dangerous`, `memory`, etc. in the project-ignore list pinned by
`cmd/odek/main_test.go`.

### System-prompt guidance

One sentence in the compiled-in default system prompt (wording pinned by
`TestDefaultSystem_MentionsPlanTool`):

> For multi-step work, maintain a plan with the plan tool: create steps up
> front, keep statuses current, replan when blocked. The plan survives context
> trimming — trust it over your memory of earlier turns.

---

## Observability

Two additive `odek.event/v1` types expose plan activity on the runtime event
stream (`Config.EventHandler`, `odek run --events-jsonl`, `/api/events`):

| Event | Fired when | `data` fields |
|-------|------------|---------------|
| `plan_created` | `create` — including wholesale replace over an existing plan | `steps`, `version` |
| `plan_updated` | every other version-bumping mutation (`update`, `complete`) | `steps`, `done`, `in_progress`, `blocked`, `pending`, `version` |

Emission: `PlanStore.SetOnChange` wires the engine's emitter at
`SetPlanStore` time; the store fires exactly once per effective mutation
under its mutex, so event order always matches version order even inside
parallel tool batches. Idempotent no-ops, the read-only `get` verb, and
resume-path `Restore`/`Reset` never fire. Payloads carry aggregate
counts and the version ONLY — never step titles or notes (the same
minimality invariant as the args-digest rule on tool-call events). A
note-only update bumps the version and therefore emits `plan_updated` with
unchanged counts — deliberate, so the version stream stays gapless for
consumers correlating versions. There is no `iteration` field: mutations
fire inside parallel tool goroutines with no iteration context; consumers
correlate via the surrounding `tool_call_started`/`tool_call_completed`
pair for the `plan` tool.

---

## Surface Integration

### REST — `GET /api/sessions/{id}/plan`

Read-only structured plan view on `odek serve`, reached through
`handleSessionByID`, so rate limiting and session-token auth match the
sibling session endpoints exactly. The server parses the newest parseable
`[Current plan:` system message with the shared extractor `loop.ExtractPlan`
— the same strict parser the restart-resume path uses — and never mutates
anything. One deliberate divergence: extraction validates against the
absolute step ceiling (50), not the operator's current `max_steps` — an old
over-cap plan may still display on surfaces even when resume would drop it.

```jsonc
{
  "session_id": "2026…", "version": 3, "found": true,
  "steps": [
    { "id": "s1", "title": "Scaffold command skeleton", "status": "done" },
    { "id": "s2", "title": "Wire flag parsing", "status": "in_progress", "note": "…" }
  ]
}
```

- `found:false` (still HTTP 200) when the transcript carries no parseable
  plan message; `version`/`steps` are then zero/empty. A collapsed all-done
  plan parses to a version with no rows — `steps` is `[]`, not null.
- **404** for an unknown session id; `note` is omitted when empty.
- GET-only by contract: a non-GET request to `…/plan` cannot fall through
  to the base-session mutators (POST would otherwise rename the session
  through the plan URL).

### Telegram — `/plan_status`

Renders the current chat-scoped session's structured plan: a header line
(`📋 Plan — vN · X/Y done[ · Z blocked]`) plus one line per step with a
status glyph (✅ done, 🔄 in progress, ⬜ pending, ⛔ blocked), id, title,
and optional truncated note. Output is bounded at 3800 chars (Telegram caps
one message at 4096); overflow drops whole step lines behind an explicit
`_N more step(s) omitted_` trailer instead of cutting mid-line. Absent or
unparseable plans reply "No active plan in this session." This reads the
**structured** loop plan (engine `plan` tool state) and coexists with the
markdown-file `/plan` family (`~/.odek/plans/`) — different concepts,
uncoupled by design.

### WebUI — plan panel

A `plan` tab in the management drawer (Alt+M) shows the active session's
structured plan: a summary header plus glyph/id/title/note rows mirroring
the Telegram renderer. Strictly read-only — no mutation controls — and
model-derived step text reaches the DOM exclusively via `textContent`. The
panel polls `GET /api/sessions/{id}/plan` every 5 s while visible (drawer
open + plan tab active + document visible), refreshes instantly on tab
activation, session switch, and `visibilitychange`, and stops otherwise.
Polling — not WebSocket push — because runtime events currently land only
in the `/api/events` ring; nothing relays them over WS today. Polling is
the documented transport until such a relay lands; responses are tiny, so
the cadence is cheap.

---

## Security Model

| Concern | Mechanism |
|---------|-----------|
| Approval fatigue | None. Explicit Safe class in `classifyToolCall` — plan calls never surface in approval prompts or the batch gate. State changes are confined to engine memory. Pinned end-to-end by `TestReport_PlanToolClassifiedSafe`. |
| Untrusted-content boundary | Step-line bodies derive from task/tool content and are re-injected as system context every iteration. Wrapped via the engine's `SetUntrustedWrapper` with source `"plan"`, matching the compaction-digest precedent; header stays outside the wrapper so recognition survives. Audit ingest recorder records the injection where active. |
| Forgery via tool output | A hostile tool result containing a literal `[Current plan:` line cannot become *the* plan message: recognition requires role `system`, and only `refreshPlanMessage` writes that role/content pair. Rendered plan text inside a tool result stays inside the nonce'd tool-result delimiters. `TestIsPlanMessage` pins rejection of tool/assistant roles and mid-text mentions. |
| Secret leakage | Plan titles/notes can echo secrets from task context. Sessions redact every message at save time (`internal/redact`) — the plan message is covered because it *is* a message riding the normal session save. |
| Resume parsing | Strict and total: bad header, over-cap steps, unknown status token, count mismatch, duplicate ids, omission marker in any position (overflowed plans are not resumable), unterminated wrapper, content after the wrapper close tag — any deviation drops the whole plan (and removes the failed message from the history) instead of approximating. 19 rejection cases pinned by `TestPlan_ParseStrictRejections`. |
| Config trust split | See project clamp rules above: project config cannot raise caps or flip a globally-disabled feature on. The tool reads resolved values only. |
| Provenance | Completed plans are not promoted to memory facts and carry no new provenance class: episode extraction reads them as ordinary transcript content, so existing taint rules apply unchanged. |

---

## Tests

`internal/loop/plan_test.go`:

- Validation matrix (`TestPlan_Validate_CreateCaps`, `_UpdateAtomicity`,
  `_CompleteUnknownID`, `_BadArgsAndVerb`) — asserts state unchanged after
  every rejection.
- Render↔parse round-trips (`TestPlan_RenderParseRoundTrip`,
  `TestPlan_RoundTripThroughValidation`) including hostile notes (colons,
  brackets, em dashes, newlines); collapse (`TestPlan_CollapseWhenDone`);
  overflow (`TestPlan_OverflowDropsDoneFirst`, `TestPlan_RenderRespectsCapAlways`).
- Strict parser: 19 rejection cases (`TestPlan_ParseStrictRejections`);
  untrusted-wrapper unwrapping (`TestPlan_ParseUnwrapsUntrustedBody`).
- Recognition/forgery pins (`TestIsPlanMessage`); classification unit pin
  (`TestClassifyToolCall_PlanSafe`).

Engine integration — additions to `internal/loop/loop_test.go`:
`TestEngine_Run_PlanLifecycle` (scripted fake-LLM run: create → work →
complete → final answer, plan message asserted in the protected head region),
`TestTrimContext_PlanProtected` and
`TestTrimContext_PlanProtectedAfterLeadingInjection` (trimming with a plan;
the second covers the droppable-boundary shift when a leading injection is
present), `TestTrimToSurvival_KeepsPlan`,
`TestTrimToSurvival_NoPlanGroupAbsorption` (no duplicate copies from group
absorption), `TestEngine_Resume_RestoresPlanFromMessages` (transcript-fed
resume restores state; next refresh upserts in place), and
`TestEngine_Resume_DropsCorruptPlanMessage` (unparseable plan messages are
removed from the history; a valid older message still restores).

Security regression bar — `cmd/odek/security_report_validation_test.go`:
`TestReport_PlanToolClassifiedSafe` (Safe classification through a real engine
run incl. batch-gate behavior), `TestReport_PlanMessageWrappedUntrusted`
(nonce'd wrapper on the rendered body), `TestReport_PlanningProjectClamp`
(project config cannot raise caps or enable a globally-disabled feature).

Config and wiring — `internal/config/loader_test.go` (`TestPlanning_Defaults`,
`_EnvDisable`, `_CLIFlagDisable`, `_RangeClamps`, `_ProjectClamp`), flag
parsing (`TestParseRunFlags_PlanningFlags`, `TestParseReplFlags_NoPlanning`),
the continue-path wiring pin (`TestContinueCmd_WiresPlanTool`), and the
system-prompt pin (`TestDefaultSystem_MentionsPlanTool`).

Parallel-batch mutation is mutex-serialized in `PlanStore` and covered by the
package's `-race` runs (`go test -race ./internal/loop/`).

Surfaces and events — `internal/loop/plan_events_test.go` (change-callback
semantics, payload minimality, `ExtractPlan`), `cmd/odek/serve_plan_test.go`
(endpoint shape, `found:false`, 404, POST-is-read-only),
`cmd/odek/telegram_plan_status_test.go`, and the WebUI
`cmd/odek/ui/js/plan.test.js` (panel rendering + polling lifecycle).

---

## Status & Roadmap

### Shipped (Phase 1 — implemented)

- `internal/loop/plan.go`: types, mutex-serialized `PlanStore`, fail-closed
  validation, deterministic renderer, strict total parser.
- Built-in `plan` tool (create/update/complete/get); registration gated on
  `Planning.Enabled`; shared-store wiring via `odek.New` discovery.
- Protected `[Current plan:` system message: trimming/survival-trim protection,
  upsert-in-place position stability, nonce'd untrusted wrapping with
  version-keyed nonce reuse, collapse-when-done, overflow truncation.
- Restart resume via strict transcript parsing at the top of `runLoop`; serve
  persistence filter keeps plan messages.
- Config plumbing: `planning` section, clamps, project clamp policy,
  `ODEK_PLANNING`, `--planning`/`--no-planning` on run/repl/serve; system-prompt
  guidance sentence; test coverage as listed above.

### Shipped (Phase 2/3 — events & surfaces)

- `odek.event/v1` types `plan_created` / `plan_updated` — once per effective
  version-bumping mutation via `PlanStore.SetOnChange` → the engine emit
  path; counts + version only, never titles/notes (see Observability);
  `docs/EXTENSIONS.md` rows added.
- Exported extractor `loop.ExtractPlan([]llm.Message) (*PlanState, bool)` —
  newest-parseable-wins, fail-closed corrupt-drop, unwraps the nonce'd
  wrapper; shared by REST and Telegram.
- Read-only REST view `GET /api/sessions/{id}/plan` (`found:false` when
  absent; GET-only; see Surface Integration).
- Telegram `/plan_status` — structured plan for the chat-scoped session,
  coexisting with the markdown-file `/plan` family.
- WebUI plan panel — session-drawer tab polling `GET …/plan` every 5 s while
  visible.
- Emoji mapping `plan` → 📋 in `internal/render/render.go` and the WebUI
  mirror `cmd/odek/ui/js/render.js`; the vestigial `todo` special-case was
  retired in both (falls through to the default 🔧).

### Planned (not yet implemented)

**Loop integrations:** plan-aware stall-recovery hint suffix naming
the current step and next pending step; blocked-step streak trigger
(consecutive `blocked` transitions fire a decompose-or-reorder hint and a
`plan_blocked` signal); remaining-steps blocks appended locally on
iteration-budget and execution-budget exhaustion paths.

**Telemetry:** adoption/overhead aggregation surfaced via
`/api/usage`.

### Non-goals

- **Not waterfall.** No gating anywhere: the loop never blocks on plan
  existence, coverage, or step order.
- **No DAG/dependency graph.** An ordered flat list suffices; ordering is
  advisory.
- **No Telegram markdown migration.** `/plan` continues to manage operator-
  facing markdown files (`~/.odek/plans/`, `internal/telegram/plan.go`);
  the structured plan is loop-internal state — distinct by design, uncoupled
  in code.
- **No plan-to-memory auto-promotion.** Completed plans are not written into
  facts.
- **No sub-agent inheritance.** Sub-agents get no parent plan state by default
  — injecting it would leak parent task context across trust-level boundaries.
  The supported pattern: pass relevant step ID/title/constraints explicitly
  through `delegate_tasks`' existing `context` field, mark the step
  `in_progress` before delegating, and evaluate results against the step
  afterward.

---

## Design Notes

**One tool, four verbs — not many tools.** Tool schemas count against the
context budget on every request; splitting into `plan_create`/`plan_update`/…
would cost 4× schema tokens and reserve 4× names for marginal clarity.
`complete` exists as a verb because it is the highest-frequency, lowest-
argument operation — making it cheap encourages actually closing steps.

**Message-channel persistence — not files.** Persisting through the existing
per-step session save provides atomic writes, redact-at-save, and restart
survival for free; a file-backed store under `~/.odek/plans/` would add a
filesystem surface, maintenance-janitor scope, cross-session conflict rules,
and a second write path to keep consistent.

**Not the injectable-context hook pattern.** Blocks inserted via the leading-
injection mechanism are deliberately droppable by trimming — exactly wrong for
state that must survive the whole run. The plan uses the compaction-digest
pattern instead: recognized-by-prefix, `headLen`-protected, upsert-in-place.

**Bias to action.** The plan is a steering instrument, never a gate: no
enforcement path exists anywhere in the loop, `create` replaces wholesale so
replanning is one call, and the prompt frames plans as "steering aids, not
contracts."
