# Task Decomposition & Sub-Agents

odek can **spawn focused sub-agent OS processes** for parallel, isolated work on independent sub-tasks. Each sub-agent is its own operating system process — not a goroutine, not a lightweight thread — with its own heap, its own config, and its own context window.

```bash
# Spawn a sub-agent directly
odek subagent --goal "Build JWT auth middleware in internal/middleware/auth.go" \
  --context "Uses gin, user model at internal/models/user.go"

# Machine-readable JSON on stdout, human-readable progress on stderr
# → {"status":"success","summary":"...","files_changed":[...],"tokens_used":4200,"iterations":3}
```

## Why OS processes?

| Approach | Isolation | Panic Safety | Memory | Testability |
|----------|-----------|-------------|--------|-------------|
| **Goroutine (in-process)** | Shared heap | One panic kills all | Shared | Easy |
| **OS process** | Full isolation | Independent | Separate | Via CLI |
| **Docker container** | Kernel isolation | Independent | Heavy | Slow |

Sub-agents use OS processes (`os/exec`) — real isolation without Docker overhead. A panic in a sub-agent exits only that sub-agent (exit code 3). The parent agent continues unaffected.

## Architecture

```
┌─────────────────────┐
│   Parent Agent       │
│   (ReAct loop)       │
└────────┬────────────┘
         │ delegate_tasks({ tasks: [...] })
         │
         ▼
┌─────────────────────────────────┐
│   delegateTasksTool              │
│   ────────────────────           │
│   • Writes each task to temp     │
│     file (avoids CLI arg limits) │
│   • Spawns odek subagent         │
│     per task via exec.Command    │
│   • Semaphore for concurrency    │
│   • Collects JSON from stdout    │
│   • Returns formatted summary    │
└──┬──────────┬──────────┬────────┘
   │          │          │
   ▼          ▼          ▼
┌──────┐ ┌──────┐ ┌──────┐
│ SA 1 │ │ SA 2 │ │ SA 3 │  ← OS processes (parallel)
└──────┘ └──────┘ └──────┘
```

## When to decompose

Decompose a task when it has **clear, independent sub-tasks** with minimal cross-cutting concerns:

- **Good**: "Build a user CRUD API" → { "Create user model", "Create handler", "Create routes" }
- **Bad**: "Refactor the auth module to use JWT" — a single goal with tight coupling, better done in one shot
- **Bad**: "Fix bug X" or "Review PR Y" — too small, overhead of spawning a sub-agent isn't worth it

Each sub-agent gets a **fresh context** — no parent history, no conversation state. It starts from the system prompt and its goal. Provide enough context so it doesn't need to re-discover the project structure.

## Tool: `delegate_tasks`

The `delegate_tasks` tool is available in all odek modes (CLI, REPL, Web UI). The agent calls it automatically when it identifies independent sub-tasks.

### Schema

```jsonc
{
  "type": "object",
  "properties": {
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "properties": {
          "goal":       { "type": "string" },  // Required. Specific goal for this sub-agent.
          "context":    { "type": "string" },  // Optional. Background: file paths, API contracts.
          "guidance":   { "type": "string" },  // Optional. How to approach the task — delivered in
                                               //   the request, NOT the system prompt (which is fixed).
          "trust_level": { "type": "string", "enum": ["trusted", "untrusted"] },
                                               // Optional. Trust level of the goal/context strings.
                                               //   "untrusted" when any portion derives from external
                                               //   content (fetched pages, files outside CWD, MCP output);
                                               //   untrusted tasks run with stricter approval defaults.
          "max_risk":   { "type": "string",
                          "enum": ["safe", "local_write", "system_write", "destructive",
                                   "code_execution", "network_egress", "install", "blocked"] },
                                               // Optional cap on the allowed risk class. Calls above the
                                               //   cap are denied without prompting — use for read-only
                                               //   fan-out tasks.
          "profile":    { "type": "string" }   // Optional. Operator-defined capability profile name
                                               //   (top-level `profiles` config). Its max_risk, allowlist,
                                               //   and tool filter OVERRIDE the operator's global config
                                               //   for this sub-agent. Unknown names fail the task.
        },
        "required": ["goal"]
      }
    },
    "description": { "type": "string" }    // Optional. Logged for debugging.
  },
  "required": ["tasks"]
}
```

### Output format

```jsonc
{
  "status": "success",            // "success" or "error"
  "summary": "Built JWT auth middleware with HS256 signing",
  "summary_truncated": true,      // present only when the headline was cut at 2048 runes
  "summary_runes": 3120,          // original headline length — present only when truncated
  "files_changed": ["internal/middleware/auth.go"],
  "tokens_used": 4200,
  "iterations": 3,
  "parent_session": "20260519-abc123"  // echoed back when --parent-session was passed
}
```

The `summary` is the headline channel: the child's final answer, capped at
2048 runes. When `summary_truncated` is set, the parent-side render appends
`headline truncated (2048 of N runes shown) — fetch artifacts via
artifact_read or re-run with a narrower goal` (the `artifact_read` half is
omitted in processes that do not have the tool, i.e. mid-tree parents).
Both fields are `omitempty`: results that fit the headline produce byte-identical
JSON to older versions.

The `parent_session` field is omitted when `--parent-session` was not supplied.
Use it to correlate sub-agent results back to the originating parent session
in logs, dashboards, or audit pipelines.

On failure:

```jsonc
{
  "status": "error",
  "error": "create agent: odek: no API key provided",
  "summary": "",
  "files_changed": [],
  "tokens_used": 0,
  "iterations": 0
}
```

### What the tool does

1. **Deserializes** the task array from the LLM's tool call
2. **Validates**: rejects empty, >8 tasks, or malformed JSON
3. **Writes** each task to a temp file (`odek-task-*.json`) — avoids CLI argument length limits (useful for 100KB+ context)
4. **Spawns** `odek subagent --task <file> --quiet` for each task
5. **Limits concurrency** via a **process-wide** buffered-channel semaphore (default: 3, max: configurable) — sibling delegate_tasks calls in one batch and concurrent `odek serve` sessions share the same bound, since provider plans are account-wide
6. **Collects** JSON result from each subprocess stdout
7. **Returns** a formatted summary with all sub-agent results tagged by task number

## CLI: `odek subagent`

Direct invocation for testing and debugging:

```bash
# Basic
odek subagent --goal "List files in /tmp"

# With context
odek subagent --goal "Build auth middleware" --context "Uses gin framework"

# From file (for large context)
odek subagent --task /path/to/task.json

# With timeout and iteration limits
odek subagent --goal "Refactor main.go" --timeout 60 --max-iter 10

# Silent mode (suppresses emoji progress on stderr)
odek subagent --goal "Run tests" --quiet

# With parent session ID (for cross-session context)
odek subagent --goal "Continue refactoring" --parent-session "20260519-abc123"
```

### Exit codes

| Code | Meaning | When |
|------|---------|------|
| `0` | Success | Task completed normally, `status: "success"` in JSON |
| `1` | Task error | Agent failed with a recoverable error, `status: "error"` in JSON |
| `2` | Timeout | Context deadline exceeded (controlled by `--timeout`) |
| `3` | Setup failure | Invalid flags, missing config, or internal panic |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--goal <string>` | — | **Required** unless `--task` specified. The sub-agent's goal. |
| `--context <string>` | `""` | Background context (file paths, design decisions) |
| `--task <file>` | — | JSON file with the task spec (see [Task file format](#task-file-format)). Mutually exclusive with `--goal`. |
| `--timeout <sec>` | 1800 | Max seconds the sub-agent may run before being killed (hard max 1800 = 30 min) |
| `--max-iter <n>` | 15 | Max think→act cycles (hard max 100) |
| `--quiet` | false | Suppress emoji progress on stderr |
| `--stream` | false | Emit tool events as JSON lines on stdout while the run progresses (the final JSON result is also printed on stdout at exit) |
| `--parent-session <id>` | — | Session ID from the parent (for context relay) |
| `--profile <name>` | — | Select an operator-defined capability profile (top-level `profiles` config). The profile's `max_risk`/`allowlist`/tool filter override the operator's global config; unknown names fail the task |

## Task file format

For large prompts that exceed CLI argument length limits, use the `--task` flag with a JSON file:

```json
{
  "goal": "Create a user registration endpoint in handlers/user.go",
  "context": "Uses gin. DB connection at internal/db/db.go. User struct in models/user.go: {ID, Email, Password, CreatedAt}. Password must be bcrypt-hashed. Returns 201 with user JSON on success.",
  "guidance": "",
  "trust_level": "trusted",
  "max_risk": "local_write",
  "profile": "",
  "parent_trust": "",
  "budget": {"max_runtime_seconds": 0, "max_tool_calls": 0, "max_input_tokens": 0, "max_output_tokens": 0, "max_cost_usd": 0}
}
```

All keys except `goal` are optional. `trust_level` / `max_risk` / `profile` mirror the `delegate_tasks` task fields; `parent_trust` records the spawning agent's trust; `budget` carries the parent's remaining budget when `subagent.budget_inherit` is `"share"` — the child enforces `min(operator limits, inherited values)` (zero values are ignored).

The `delegate_tasks` tool always uses this file-based approach internally.

## Output protocol

### stdout (machine-readable)

Pure JSON. Always parseable — even on errors. The parent `delegate_tasks` tool parses this stream line-by-line (`json.Unmarshal` per line):

```jsonc
// Success
{"status":"success","summary":"Created handlers/user.go with POST /users","files_changed":["handlers/user.go"],"tokens_used":3200,"iterations":5}

// Error
{"status":"error","error":"no API key provided","summary":"","tokens_used":0,"iterations":0}
```

### stderr (human-readable)

Emoji-prefixed progress for terminal users:

```
🔧 Sub-agent: Build JWT auth middleware
🧠 Need to understand the project structure...
🔧 shell: ls internal/
✅ Sub-agent complete: 4.2s, 3200 tokens, 5 iterations
```

Suppressed with `--quiet`.

- At iteration boundaries, when the run crosses 50% / 75% / 90% of its
  iteration or wall-clock budget, the engine appends a one-line budget hint
  to the corrections message and emits a `budget_warning` signal, so the
  model can pace itself and conclude cleanly instead of being cut off
  mid-work (`subagent.announce_budget`, default on).
- The wall-clock budget is two-stage: a finalization window (min(15s,
  timeout/8)) before the hard kill is reserved for a bounded
  partial-progress summary — a timed-out sub-agent returns
  `status: "partial"`, `partial_reason: "time_budget"` and a usable
  report instead of nothing. Iteration-budget exhaustion reports
  `partial_reason: "iteration_budget"`; a hard execution budget reports
  `status: "budget_exhausted"` (exit code 4).
- Delegation depth is capped (`subagent.max_depth`, default 2, tracked via
  `ODEK_SUBAGENT_DEPTH`): a sub-agent at the cap refuses to fan out
  further.
- With `subagent.budget_inherit: "share"`, the parent writes its remaining
  budget into each task file and the child enforces
  min(operator limits, parent remaining) — a near-exhausted parent can no
  longer spawn children with fresh headroom.

## System prompt & request (trust boundary)

A sub-agent's **system prompt is a fixed, code-defined constant** (`subagentSystem` in
`cmd/odek/subagent.go`). It composes three parts: a focused-task identity block, the
**same invariant security pillar the parent prompt carries** (shared `securityPillar`:
Safety, Execution provenance, and Indirect Prompt Injection sections), and sub-agent
amendments that adapt principal-facing rules to a child with no principal channel —
skip-and-report replaces confirmation, scope is the declared task, and injection
findings go into the final report. **Nothing the parent supplies is ever spliced into
it** — not the parent's system prompt, not `--system`, not `IDENTITY.md`.

All parent-supplied strings travel in the **user request** instead, assembled by
`buildSubagentRequest()`:

```text
Task: <goal>

Approach (guidance from the orchestrator):
<guidance>          # optional — how to tackle it, NOT an identity

Context:
<context>           # optional — file paths, API contracts, decisions
```

This separation is deliberate. The `goal`/`guidance`/`context` may contain text the
parent ingested from untrusted sources (fetched pages, MCP output, files). Keeping them
out of the system prompt means a prompt-injection payload can never rewrite the
sub-agent's identity or strip its safety rules — at worst it's a hostile *request*, which
the fixed SAFETY block tells the model to treat as data.

### Approvals, denials & trust inheritance

Sub-agents are autonomous by design and **never prompt for approvals** —
not even trusted ones. Prompt-class operations are denied (`non_interactive:
deny` is forced for every sub-agent); the operator `allowlist` (exact
pre-approved invocations) is the only path to prompt-class operations.

Denials are not silent: each child reports the policy denials it observed
in its result (`denials: [{tool, class, reason}]`, capped at 20 with
`denials_total` for the full count), and the parent emits one
`subagent_denied` runtime event per denial — so the parent model can adapt
("network denied → fetch it myself") and operators get uniform visibility.

Trust is **non-increasing downward**: the delegate tool stamps the parent's
effective trust into the task file (`parent_trust`), and the child runs at
`min(parent_trust, trust_level)`. A task tree rooted in untrusted content
cannot launder itself into trusted children.

Capability profiles let the operator define named permission envelopes
in the top-level `profiles` config (see [CONFIG.md](CONFIG.md)): a task
selects one via `profile: "name"` and the profile's `max_risk`/`allowlist`/
`tools` **override** the corresponding global config for that sub-agent.
Profiles are operator-authored (project config is stripped) and selecting
one cannot lift the non-interactive deny or the trust lockdown — policy,
not escalation. A curated starter set of 21 task profiles (builders,
reviewers, judges, researchers, orchestrators, …) ships in
[`profiles.template.json`](../profiles.template.json).

When a task selects **no** profile, the operator's default envelope applies:
a built-in `default` profile (`max_risk: "local_write"`) unless overridden via
`subagent.default_profile` (a defined profile name, or `"none"` to disable).
The precedence chain is `--profile` flag > task-file profile > default
envelope, and `"none"` is honored only from operator config — a delegating
parent can narrow its choice but never strip the operator's envelope. The
parent model discovers available profiles (including descriptions and the
effective default) via the built-in `list_subagent_profiles` tool.

A profile may be selected by the operator's direct `odek subagent --profile`
flag or by the parent via the task file (`delegate_tasks`'s `profile`
field); the flag outranks the task file. Unknown names fail the task twice
over: `delegate_tasks` validates before spawning a child, and the child
re-validates against its own resolved config (defense in depth — also
covering manual `--task` invocation).

### Untrusted tasks are fenced

When the parent sets `trust_level: "untrusted"`, the entire request body is wrapped in an
`<untrusted_input>` fence with a preamble telling the model to treat it as data, not
instructions — in addition to the permission clamp applied by `applySubagentTrust` (see
[SECURITY.md](SECURITY.md)).

### Steering the approach

To influence *how* a sub-agent works, pass `guidance` (not a system prompt):

```jsonc
{
  "tasks": [
    {
      "goal": "Review middleware/auth.go for security issues",
      "guidance": "Look for token-validation gaps, timing attacks, and secret exposure."
    },
    {
      "goal": "Fix the OOM in parser.js",
      "guidance": "Find the root cause before changing code; prove the fix with a test."
    }
  ]
}
```

There is **no** `system` field — it was removed precisely because it let parent-controlled
(and possibly injection-tainted) text become the sub-agent's identity. `ODEK_SYSTEM` /
config `system` also do **not** apply to sub-agents; the boundary is intentionally fixed.

### Task file format

The temp file written by `delegate_tasks` carries the request inputs, never a system prompt:

```json
{
  "goal": "Create a user registration endpoint in handlers/user.go",
  "context": "Uses gin. DB connection at internal/db/db.go.",
  "guidance": "Validate inputs; return structured errors.",
  "trust_level": "trusted",
  "max_risk": "local_write",
  "profile": "",
  "parent_trust": "trusted",
  "budget": {"max_runtime_seconds": 0, "max_tool_calls": 0, "max_input_tokens": 0, "max_output_tokens": 0, "max_cost_usd": 0}
}
```

## Configuration

Config in `odek.json`:

```json
{
  "subagent": {
    "max_concurrency": 3,
    "timeout_seconds": 1800,
    "max_iterations": 15
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `max_concurrency` | 3 (global default; falls back to it when unset here) | Max sub-agents running in parallel (max 8) |
| `timeout_seconds` | 1800 | Default timeout per sub-agent; hard max 1800 (values above are clamped) |
| `max_iterations` | 15 | Default max think→act cycles per sub-agent |
| `max_depth` | 2 | Delegation nesting cap (1 = a sub-agent may not delegate further) |
| `announce_budget` | `true` | Inject budget-awareness hints (50/75/90% of budget) and announce effective limits in the sub-agent system prompt |
| `budget_inherit` | `"operator"` | Budget inheritance for child runs: `"operator"` (full operator limits) or `"share"` (child enforces min of operator limits and the parent's remaining budget) |

> The `subagent` section is **operator-only**: a project-level `./odek.json` cannot set it (ignored with a warning), so a cloned repository cannot relax sub-agent guards.

## Security model

| Risk | Mitigation |
|------|------------|
| **Sub-agent hijacking** | Sub-agents are never prompted by the parent/user — they receive structured `goal`/`context` strings. No instruction injection path. |
| **Runaway processes** | Hard timeout (`--timeout`, default 1800s = 30 min, hard max 1800s). Context cancellation kills via `os.Process.Kill()`. |
| **Resource exhaustion** | Concurrency semaphore (max `max_concurrency`). Sequential spawning. No fork bomb. |
| **Panic propagation** | Each sub-agent is an OS process. Panic exits only that process with code 3 — parent sees the JSON error and continues. |
| **Temp file leakage** | Each task file is `defer os.Remove()`'d after subprocess exit. |

## Testing

The sub-agent system has three test layers:

| Layer | Runner | What's verified |
|-------|--------|-----------------|
| **Contract tests** | `go test ./cmd/odek/` | Flag parsing, JSON stdout protocol, exit codes, tool schema, config parsing, fixed system-prompt trust boundary (`buildSubagentRequest` carries goal/guidance/context; system prompt unaffected by parent input; untrusted fencing) |
| **E2E tests** | `ODEK_E2E=1 go test ./cmd/odek/ -run "TestE2E_"` | Real subprocess spawning, tool → binary pipeline, stderr protocol, concurrency, timeouts, fixed system-prompt boundary (no threading — parent input never reaches the system prompt) |
| **Full suite** | `go test -race ./...` | Every package, race-detector clean |

E2E tests:
- Build the `odek` binary once via `TestMain`
- Test the full pipeline: `tool.Call()` → `exec.Command("odek", "subagent", ...)` → JSON stdout → parse
- Require no LLM provider (sub-agent fails on setup, producing JSON error — which is the exact contract verified)
- Validate: binary exists, stderr emoji protocol, quiet mode, 100KB+ task files via temp files, missing binary graceful degradation

## Example: End-to-end flow

```
User: "Create a REST API for a todo app in Go with JWT auth"

Parent agent thinks:
- This has 3 independent parts: model, auth, handlers
- Each can be built in parallel
- Calls delegate_tasks

Tool call:
delegate_tasks({ tasks: [
  { goal: "Create Todo model with CRUD in models/todo.go",
    context: "Uses GORM, fields: {ID, Title, Done, CreatedAt}" },
  { goal: "Create JWT auth middleware in middleware/auth.go",
    context: "Uses gin, HS256 signing, secret from env JWT_SECRET" },
  { goal: "Create todo handlers in handlers/todo.go",
    context: "Uses gin, depends on models/todo.go. Routes: GET/POST /todos, PUT/DELETE /todos/:id" }
]})

3 sub-agents run in parallel:

  SA1: odek subagent --task /tmp/kode-task-001.json --quiet
  SA2: odek subagent --task /tmp/kode-task-002.json --quiet
  SA3: odek subagent --task /tmp/kode-task-003.json --quiet

All complete in ~5s (2 batches of 2, max_concurrency=3). The parent receives
each result rendered as fields — not raw JSON:

  SA1: status: success · ~4200 tokens
       summary: Created models/todo.go — GORM CRUD with all fields per spec.
       files changed: models/todo.go
  SA2: status: success · ~3800 tokens
       summary: JWT middleware done — HS256, secret from JWT_SECRET env.
       files changed: middleware/auth.go
  SA3: status: success · ~5100 tokens
       summary: Handlers wired for GET/POST/PUT/DELETE /todos.
       files changed: handlers/todo.go

Parent synthesizes: "Created 3 files:
  - models/todo.go — Todo model with CRUD
  - middleware/auth.go — JWT auth middleware with HS256
  - handlers/todo.go — REST handlers
  Total: 8 files changed, 13100 tokens, 5s parallel"
```

## Result artifacts

The result contract is two-channel: the headline (≤ 2048 runes) carries status;
the bulk rides files. The child is told this at request time: deliverables
larger than a headline go as FLAT files into its per-task staging directory
(`.odek-artifacts/<task_id>/` inside the workspace — nested directories are
discarded). The runner relocates them to `~/.odek/artifacts/<session>/<task>/`,
measures sha256/size, and returns `odek.artifact-ref/v1` references with the result.

The parent sees one metadata line per artifact — id, media type, size, short hash, owning task, first-line summary — plus inlined content for small text artifacts (≤ 32 KiB) within a 128 KiB per-call inline budget (largest first; the rest degrade to their metadata line, never dropped). Everything larger is readable on demand via `artifact_read`:

```
artifact_read({ "id": "report" })                # first 64 KiB
artifact_read({ "id": "report", "offset": 65536 })  # continue paging
```

`artifact_read` is a parent-side tool; the model passes an id, never a path — resolution goes through the session registry of validated refs, and each read reports the owning task. Ids derive from filename stems, and **first occurrence wins**: if two tasks stage files with the same stem (e.g. both write `report.md`), the first task keeps the plain id and later duplicates register under `<id>.t<task>` aliases (`report.t2`, `report.t3`, …) — probing past any live entry, including real stems that collide with the alias namespace. Rendered artifact lines always show the effective id plus the task number, so the parent copies an id `artifact_read` can actually resolve. Refs that fail validation (wrong hash, path escape) are dropped with an explicit flag and never rendered.

Lifecycle: deleting a session deletes its artifacts (all paths — CLI, API, Telegram, retention sweep); the storage janitor backstop sweeps orphans after `maintenance.artifacts_max_age_hours` (default 24 hours, `0` = keep forever).

## Wire contract (v2)

Clients (Web UI, bodek) consume sub-agent telemetry through three surfaces:
live `subagent_state` WS frames, `GET /api/subagents` (registry snapshot —
same fields as the frames), and the framed result envelope. All v2 fields are
`omitempty`: old clients ignore them, and absent means "unavailable", never
zero.

**Phases.** A task's `phase` moves `queued → started → active → finished`.
`queued` is emitted by the parent the moment `delegate_tasks` accepts a task —
before it acquires a concurrency slot — so an 8-task delegation on a 2-slot
operator reads `2 live · 6 queued`. Queued tasks count as neither active nor
finished in the stats.

**State frame fields.** In addition to the v1 fields (`task_id`, `task_idx`,
`run_key`, `phase`, `status`, `step`, `iterations`, `tool`,
`duration_seconds`, `tokens_used`):

| Field | Carries | Notes |
|---|---|---|
| `goal` | task goal text | redacted, clamped to 2048 chars server-side (model-controlled input) |
| `profile` | profile id | declared value while queued; the child's effective (post-clamp) value from `started` on |
| `max_risk` | effective risk ceiling | declared while queued; effective (post operator-profile resolution) from `started` |
| `budget_seconds`, `budget_iterations` | enforced caps | 0/absent = uncapped; present on `started` and `active` |
| `budget_cost_usd` | enforced cost cap | only when cost enforcement is active (cap + resolved prices) |
| `cost_usd` | server-side cost estimate | cumulative on live frames; final on `finished` |
| `artifacts` | `[{id, path, bytes}]` | terminal only — metadata from the result envelope; content never rides the wire |

The framed result envelope adds `cost_usd` (final) and `artifacts`
(the full `odek.artifact-ref/v1` refs — a superset of the frame metadata).

**Cost semantics (authoritative — do not re-derive).** `cost_usd` values are
computed server-side with the exact `/api/usage` math (per-million prices
resolved for the child's model over provider-reported token totals). Zero or
absent means "prices not configured" — clients must render cost as
unavailable, never `$0`. Totals are split, not folded: the serve-lifetime
`tokens_in`/`tokens_out`/`estimated_cost_usd` in `/api/usage` cover **parent
turns only** (children are separate odek processes with their own sessions);
sub-agent spend is broken out under `subagents.tokens_used` and, as of v2,
`subagents.cost_usd` (lifetime sum of final per-task estimates). A client
total including sub-agents is therefore `estimated_cost_usd +
subagents.cost_usd` — or, per batch, the sum of `cost_usd` over final
result envelopes only (cumulative state-frame values double-count on
replay).

**Blocking model — deny, never prompt.** Sub-agents can never park waiting
on an approval: the child runs non-interactive with `deny` forced for every
operation that would prompt, and denied operations are listed in the result's
`denials` array (capped, with a separate total). There is no
`waiting_approval` state and none is planned; a card showing `running` with
denied operations is working through them or will finish with them reported.
Clients should treat `error` / `timeout` / `cancelled` as the only sticky
outcomes.

## Tips

- **Keep goals small** — one file, one concern per sub-agent. If a goal spans 3 files, it's probably not a good decomposition boundary.
- **Provide file paths** in context — saves the sub-agent from crawling the project tree.
- **Check the trade-off** — spawning a sub-agent takes ~500ms. Don't delegate tasks that complete in 2 tool calls.
- **Observation**: sub-agents work best for **greenfield** work (creating new files). Refactoring existing code often has too many implicit dependencies.
