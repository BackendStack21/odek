# Extensions

This document is the **normative, versioned contract** for third-party
extension servers that interoperate with odek. An extension server is an MCP
server (see [MCP.md](MCP.md)) that additionally follows the conventions below.

- **Contract version:** `odek-extension/v1`
- **MCP protocol version:** `2025-03-26`

Everything in this document is additive over plain MCP. A server that only
implements MCP `2025-03-26` still works with odek; the extension contract only
adds optional capabilities (artifact references, bounded results, structured
events).

## Compatibility rule

**All schemas in this contract are additive.** Producers may add new fields in
future versions; **consumers MUST ignore fields they do not recognize** and
MUST NOT fail on them. Fields are never removed or re-typed within a `/v1`
schema. A breaking change ships as a new schema name (e.g.
`odek.artifact-ref/v2`).

---

## Transport and protocol

Extension servers use the standard odek MCP client transport: **JSON-RPC 2.0
over stdio**, one message per line:

- `initialize` — handshake; odek sends `protocolVersion: "2025-03-26"`, the
  server should answer with the same or a compatible version
- `tools/list` — discover tools
- `tools/call` — invoke a tool
- `ping` — health check

Logging goes to **stderr**; stdin/stdout are reserved for the protocol.

## Tool-name rules

Tool names returned by `tools/list` must be:

- 1–64 characters
- ASCII letters, digits, `_`, `-` only
- free of the substring `__` (reserved as odek's `<server>__<tool>` qualifier
  separator)

odek rejects a server whose `tools/list` contains an invalid name. Servers
must also tolerate unknown arguments in `tools/call` params (same additive
rule).

## Server configuration

Extension servers are declared in `mcp_servers` in `~/.odek/config.json`
(operator-trusted) or `./odek.json` (project-level; per-tool approval
required — see [MCP.md](MCP.md)):

```json
{
  "mcp_servers": {
    "log-analyzer": {
      "command": "/usr/local/bin/log-analyzer-mcp",
      "args": ["--serve"],
      "env": { "LOG_LEVEL": "info" },
      "timeout_seconds": 120,
      "max_response_bytes": 2097152,
      "max_result_chars": 100000,
      "artifact_roots": ["/var/ci-artifacts"]
    }
  }
}
```

| Field | Type | Default | Since | Meaning |
|-------|------|---------|-------|---------|
| `command` | string | — (required) | MCP base | Executable to spawn |
| `args` | string[] | `[]` | MCP base | Command-line arguments |
| `env` | object | `{}` | MCP base | Env overrides; secret-looking keys are stripped |
| `timeout_seconds` | int | `30` | **odek-extension/v1** | Per-request timeout; clamped to a hard cap of 3600 |
| `max_response_bytes` | int | `10485760` (10 MiB) | **odek-extension/v1** | Max bytes for a single JSON-RPC response line; absolute ceiling 64 MiB |
| `max_result_chars` | int | `200000` | **odek-extension/v1** | Max characters of tool result text forwarded to the model; hard cap 1000000 |
| `artifact_roots` | string[] | `[]` | **odek-extension/v1** | Directories under which `file://` artifact refs are accepted. **Empty ⇒ every artifact ref is rejected (fail closed).** |

Unknown fields in the server config are ignored (additive rule).

## Timeout and limit behavior

- A call that exceeds `timeout_seconds` fails with a deadline error; the server
  process is left running and may be reused.
- A single response line larger than `max_response_bytes` **fails closed**:
  the response is dropped and the connection is closed. odek never buffers an
  unbounded line in memory.
- Result text longer than `max_result_chars` is not silently mangled: malformed
  JSON is rejected outright, and valid-but-oversized structured results get a
  structured truncation notice that retains any artifact references. The notice
  has the exact form (intentionally free of angle brackets so it cannot be
  confused with the untrusted-content wrapper):

  ```
  [odek: result truncated — server "<server>" tool "<tool>" produced <observed> chars, exceeding the configured max_result_chars=<limit>; the full result is available via the retained artifact references, if any]
  ```

  For `odek.tool-result/v1` envelopes the notice applies to the envelope `text`;
  artifact refs are validated first and rendered as metadata lines regardless.
- Errors name the server, the tool, the configured limit, and the observed
  size.

## Tool-result envelope (`odek.tool-result/v1`)

A tool whose full output is too large or too binary for the model context
returns a compact text summary plus **artifact references** by emitting this
envelope as the JSON text of a single `content` item:

```json
{
  "schema": "odek.tool-result/v1",
  "text": "Analyzed 1284 test cases: 1280 passed, 4 failed. Full report attached as artifact report-1.",
  "artifacts": [
    {
      "schema": "odek.artifact-ref/v1",
      "id": "report-1",
      "uri": "file:///var/ci-artifacts/run-4821/junit.xml",
      "media_type": "application/xml",
      "sha256": "9f2c…",
      "size_bytes": 21453211,
      "summary": "Full CI test results (JUnit XML)"
    }
  ]
}
```

The envelope is detected by its `schema` field. Unknown fields are ignored.
A plain (non-envelope) text result is always valid and needs no schema field.

## Artifact-ref schema (`odek.artifact-ref/v1`)

| Field | Required | Meaning |
|-------|----------|---------|
| `schema` | yes | Exactly `odek.artifact-ref/v1` |
| `id` | yes | Ref identifier, unique within the envelope |
| `uri` | yes | `file://` URI only; absolute, clean path |
| `media_type` | yes | MIME type of the artifact |
| `sha256` | optional | Lowercase hex digest; verified when present |
| `size_bytes` | optional | Verified against the real file when present |
| `summary` | optional | One-line human description shown to the model |

Validation **fails closed** on any violation: the resolved path (after symlink
evaluation) must lie inside a configured `artifact_roots` entry; traversal
(`..`), symlink escapes, non-`file://` URIs, hash/size mismatches, and missing
files are all rejected. Artifact **content is never auto-read into the model
context** — the model sees only the compact text plus per-artifact metadata
lines (id, media type, size, short hash, summary).

## Event schema (`odek.event/v1`)

odek can emit a structured runtime event stream: **one JSON object per line
(JSONL)**, e.g. via `odek run --events-jsonl <path>`:

```json
{"schema":"odek.event/v1","type":"tool_call_completed","run_id":"…","session_id":"…","iteration":3,"tool":"log-analyzer__summarize","timestamp":"2026-08-07T12:00:00Z","data":{"duration_ms":412,"result_bytes":812,"artifact_count":1}}
```

- `type` is one of: `run_started`, `iteration_completed`,
  `tool_call_started`, `tool_call_completed`, `tool_call_failed`,
  `session_saved`, `context_trimmed`, `budget_exceeded`, `run_completed`,
  `run_failed`, `plan_created`, `plan_updated`, `subagent_denied`,
  `subagent_spawned`, `subagent_completed`, `subagent_concurrency_wait`.
- `run_id` is a random 128-bit hex identifier generated per agent run and
  stamped on every event of that run. `session_id` appears once the session
  is known; earlier events omit it. `iteration` is the 1-based loop
  iteration; `tool` is the tool name for tool-call events.
- `data` carries per-type fields (below). Tool arguments are **never** logged
  raw by default — only a SHA-256 hash, sizes, and a structured
  `args_summary`. Human-readable fields pass through odek's secret redaction.
  No environment variables or credentials are ever included. As an explicit
  opt-in for incident review, `odek run --events-include-args` (or Go API
  `Config.EventsIncludeArgs`) adds the raw, still-redacted `args` to
  `tool_call_started` events — without it, the stream stays secret-safe.
- Unknown `type` values and unknown fields must be ignored by consumers.

Per-type `data` fields:

| `type` | `data` fields |
|--------|---------------|
| `run_started` | `model`, `sandbox` (bool), `max_iterations` |
| `iteration_completed` | `input_tokens`, `output_tokens` (cumulative), `tools_called` |
| `tool_call_started` | `call_id`, `args_sha256`, `args_bytes`, `args_summary`, `args` (opt-in) |
| `tool_call_completed` | `call_id`, `duration_ms`, `result_bytes`, `artifact_count` |
| `tool_call_failed` | `call_id`, `duration_ms`, `error_class` |
| `session_saved` | `message_count` |
| `context_trimmed` | `mode` (`proactive`/`survival`), `dropped_groups`, `truncated_results` |
| `budget_exceeded` | `limit_name` (`runtime`/`tool_calls`/`input_tokens`/`output_tokens`/`cost_usd`), `observed`, `limit` |
| `run_completed` | `duration_ms`, `input_tokens`, `output_tokens` (run totals) |
| `run_failed` | `duration_ms`, `error_class` |
| `plan_created` | `steps` (total count), `version` |
| `plan_updated` | `steps`, `done`, `in_progress`, `blocked`, `pending`, `version` |
| `subagent_denied` | `task_index`, `class`, `reason` (emitted by `delegate_tasks` for each policy denial a child reports) |

`call_id` is the stable correlation key between a `tool_call_started` and
its matching `tool_call_completed`/`tool_call_failed` event: the provider's
tool-call ID when present, else a deterministic `it<iteration>-call<index>`
synthetic ID. Batched parallel calls MUST be paired via `call_id` — never
by positional order. `args_summary` is structured, low-cardinality audit
metadata extracted from the arguments — for shell tools the program name
(`argv0`, leading env assignments skipped) plus the danger `class`; for
path tools the target `path`/`path` list and `class`; for browser/http
tools the URL `host` only (full URLs can embed credentials). It is always
present for recognized tools; values pass through secret redaction like
every other string field.

`args_sha256` correlates a `tool_call_started` with the model call that
produced it; pair it with `call_id` to match a
completion. `error_class` is a stable low-cardinality string
(`context_canceled`, `deadline_exceeded`, `tool_error`, `error`) — raw error
text is never emitted. On budget exhaustion `budget_exceeded` is always
emitted before the closing `run_failed`. `session_saved` is emitted by
surfaces that persist sessions per completed step (currently the `odek run
--session` persist callback, with `message_count`). The plan events are
emitted once per effective version-bumping mutation of the built-in `plan`
tool (see [PLANNING.md](PLANNING.md)): `create` — including wholesale
replace — maps to `plan_created`, every other bumping mutation to
`plan_updated`. Payloads carry aggregate counts and the version ONLY — never
step titles or notes. A note-only update bumps the version and therefore
emits `plan_updated` with unchanged counts (the version stream stays
gapless). Plan events carry no `iteration`: mutations fire inside parallel
tool goroutines, so consumers correlate via the surrounding
`tool_call_started`/`tool_call_completed` pair for the `plan` tool.

Sink behavior (`--events-jsonl`): the file is created (and hardened) with
`0600` permissions, the parent directory must already exist, a symlink at the
target path is refused (and the subsequent open itself passes `O_NOFOLLOW`,
so a symlink swapped in between the check and the open cannot redirect the
stream), and every event is flushed to stable storage before the write
returns. Event dispatch from the agent loop is non-blocking: events flow over
a buffered channel drained by a dedicated goroutine — when the buffer fills,
new events are dropped (never blocking the loop), and a panicking consumer
cannot crash the agent.

## External-state-ref schema

Sessions may carry operator-supplied pointers to state that lives outside odek
(CI runs, dashboards, object stores). odek **stores and transports these refs
but never dereferences them**:

```json
{
  "kind": "ci-run",
  "uri": "https://ci.example.test/runs/4821",
  "created_by": "ci-orchestrator",
  "read_only": true,
  "created_at": "2026-08-07T12:00:00Z"
}
```

- `kind`: 1–64 chars, `[a-z0-9_-]`
- `uri`: 1–2048 chars, no control characters
- `created_by`: 1–128 chars
- Refs are deduplicated on `(kind, uri, created_by)`.

## Budget exit behavior

When a configured execution budget (runtime, tool calls, tokens, or cost) is
exhausted, odek emits a `budget_exceeded` event, persists the latest safe
session state, and exits with a distinguishable status:

| Exit code | Meaning |
|-----------|---------|
| `0` | success |
| `1` | task/model/tool error |
| `2` | overall timeout (currently used by `odek subagent`) |
| `3` | setup/contract error (currently used by `odek subagent`) |
| `4` | **execution budget exhausted** (odek-extension/v1; `odek run`, `odek subagent`) |

Budget errors are surfaced as typed errors naming the limit, the observed
value, and the configured maximum, so orchestrators can tell a budget stop
apart from a model or tool failure. Cost estimation uses operator-configured
per-million prices: an exact `model_prices` key match for the run's model ID
overrides the flat `input/output_cost_per_million_usd` pair (each missing
price in the entry falls back individually); unknown models use the flat
pair. Budget enforcement is wired into `odek run` and `odek subagent` — the
sub-agent enforces the operator limits (clamped by any parent-inherited
budget when `subagent.budget_inherit` is `"share"`) and reports
`status: "budget_exhausted"` with exit code 4. `continue`, REPL, `serve`, and
Telegram do not yet enforce limits, and there is no `ODEK_*` env-var layer
for limits — sources are the `limits` config section (project configs may
only *lower* global values, and project-set prices — flat or per-model — are
rejected outright) and the `--max-*` CLI flags.

## Building a compatible server

A minimal compatible server only needs: stdio JSON-RPC, `initialize` /
`tools/list` / `tools/call`, valid tool names, and text results. Everything
else in this document is opt-in. A reference mock implementing every fixture
tool (`echo`, `large_result`, `artifact_result`, `bad_artifact`, `slow`,
`error_result`) lives at `internal/mcpclient/testdata/artifact_server.go` and
is exercised by `internal/mcpclient/contract_test.go`.

## Sub-agent result artifacts

The same artifact schemas power the `delegate_tasks` result channel (no MCP
server involved). The task envelope carries an `artifact_root` naming the
per-task directory the parent created; the child runner relocates staged
workspace files there, measures `sha256`/`size_bytes` itself, and returns
`odek.artifact-ref/v1` references in `subagentResult.artifacts`. The parent
validates every ref fail-closed against the per-task root before rendering —
metadata-only lines in the model context, content inlined for text artifacts
≤ 32 KiB, everything else readable by the parent via the `artifact_read`
tool (id-keyed; paths never enter the model context). See
`docs/SUBAGENTS.md — Result artifacts` and `docs/SECURITY.md` for the
invariants; `SUBAGENT_RESULT_ARTIFACTS_PLAN.md` documents the design.
