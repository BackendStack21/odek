# Runtime logging

Enable metadata-only operational logging in `~/.odek/config.json`:

```json
{
  "logging": { "enabled": true },
  "maintenance": {
    "enabled": true,
    "interval_minutes": 60,
    "log_max_mb": 50,
    "runtime_log_max_age_hours": 168
  }
}
```

Logging is off by default. `ODEK_LOGGING_ENABLED=true` overrides the operator
file. Project `odek.json` cannot enable or disable logging or change retention.
`odek init --global` includes these settings. No external service is required.

CLI run/continue, REPL, serve, Telegram, scheduled executions, and standalone
subagents use `~/.odek/runtime.log`. Delegated children forward metadata through
stdout to their parent; the top-level parent writes their records to its log.
Existing surface logs and `run --events-jsonl` continue to work independently.
`serve.log` remains the web server's startup/turn/failure log, including its
provider-failure summaries. `runtime.log` is the first place to investigate
application failures, with structured service diagnostics and model/tool/subagent
activity across execution surfaces. Serve turn boundaries consequently
appear in both files; retaining them preserves existing log consumers.
In particular, `--events-include-args` never enables arguments in runtime.log.

## Reading the log

Every line is JSON with the additive `odek.event/v1` envelope, `level`, and the
originating top-level `surface`. Use `tail -F` to follow across rotation:

```sh
tail -F ~/.odek/runtime.log | jq --unbuffered .
tail -F ~/.odek/runtime.log | jq --unbuffered 'select(.session_id == "SESSION_ID")'
tail -F ~/.odek/runtime.log | jq --unbuffered 'select(.task_id == "TASK_ID")'
```

The correlation fields have distinct meanings:

| Field | Meaning |
| --- | --- |
| `session_id` | Session or transient audit-session ID. Inherited by children and grandchildren. Omitted on setup records emitted before a session is known. |
| `run_id` | Agent instance ID. It remains stable when the same Agent handles multiple turns. |
| `turn_id` | Fresh ID per `Run` / `RunWithMessages` invocation. |
| `root_run_id` | Root Agent of the delegation tree. |
| `task_id`, `parent_task_id` | Delegated task and its immediate task ancestor. |
| `parent_run_id`, `parent_turn_id` | Parent invocation for a child's own events. |
| `source_task_id` | Parent-verified direct child that relayed a record. Nested task metadata is child-reported; this field identifies the actual IPC source. |
| `data.call_id` | Correlates tool lifecycle records within a turn. |

`run_started` describes Agent setup, `turn_started` begins an invocation, and
`run_completed` / `run_failed` ends that invocation. Model calls emit
`llm_call_started`, `llm_call_completed`, or `llm_call_failed`, with durations and
available usage. Budget warnings and tool-recovery signals carry bounded
metadata without raw detail text. Detailed SDK-internal retries are not
currently logged.

Tool records distinguish a requested call (`tool_call_started`) from actual
execution (`tool_call_executing`). `tool_execution_completed` is emitted when
that worker finishes, even while other parallel tools remain active. Existing
`tool_call_completed` / `tool_call_failed` records describe ordered result
processing after the batch finishes. Do not count both as separate executions.

Subagents emit queue/slot/start/completion records, including effective profile
and budgets when available. Every 60 seconds a parent emits `subagent_running`
with `activity`, `active_calls`, `pending_calls`, elapsed time, and the age of the
last observed child runtime event. This says the parent has not yet observed
process completion; it is not a health guarantee or proof of useful progress.
`tools_pending` may mean approval or concurrency waiting. Children never wait
for interactive approval: denied operations are reported separately.

Terminal records include available usage, estimated cost, artifact count,
parent-observed exit code/status, and stderr byte count. Child-reported task
`status` is separate from process `exit_status`; a partial result may accompany
a nonzero exit. Cost is an operator-price estimate, not a bill, and is omitted
when prices are unavailable. Run and task terminal records overlap: do not sum
costs or tokens across all event types. Use the root terminal record for a tree
whose child usage was settled through shared budgets. In operator-budget mode
child usage can be separate: inspect
task completion records. Ancestor and descendant totals must not be added
blindly; these records follow the existing budget-accounting semantics.

## Investigating failures

Start with warnings and errors, then follow the matching process or session:

```sh
# Follow new warnings/errors across rotation and janitor replacements.
tail -F ~/.odek/runtime.log | jq --unbuffered 'select(.level == "WARN" or .level == "ERROR")'

# Include the rotated backup when investigating an earlier failure.
jq -c 'select(.session_id == "SESSION_ID")' ~/.odek/runtime.log.1 ~/.odek/runtime.log

# Startup and service-wide failures have no session: correlate by process_id.
jq -c 'select(.process_id == "PROCESS_ID")' ~/.odek/runtime.log
```

The backup exists only after the first rotation; omit it from the command if it
is absent. `process_id` is a random identifier for the writing process and `pid`
is its OS process ID. Every runtime log record includes both. They join CLI
startup diagnostics to agent records without inventing a session or run ID for
work that happens outside an agent. Relayed child records carry the parent's
writer identity; use `source_task_id` and delegation ancestry to identify the
child, and subagent start metadata for its PID.

Application diagnostics use these event types:

| Event | Severity | Meaning |
| --- | --- | --- |
| `operation_failed` | ERROR | An application operation failed. |
| `operation_warning` | WARN | A configuration fallback, rejected request, or other degraded state. |
| `panic_recovered` | ERROR | A panic reached an instrumented boundary. Service boundaries keep their existing recovery behavior; the main command and HTTP boundaries rethrow after reporting. |

`data.component` identifies the subsystem and `data.operation` identifies the
step. `data.error_class` categorizes the cause; `data.error_type` preserves the
Go error type where an error is available, and `data.http_status` carries a
provider/API/HTTP response status when known. Messages, error strings, provider
response bodies, panic values, and stack traces are not copied into this file.
Unknown error types retain the `error` category; a record does not always contain
enough detail to reconstruct the underlying failure.

| Component | Recorded failures and warnings |
| --- | --- |
| `cli`, `agent` | Command errors (including parsing/setup), unknown commands, agent initialization, and main-command panics. |
| `config` | Unreadable, oversized, or malformed configuration; unsafe permissions; invalid environment values; secrets-file permissions/read failures. |
| `session`, `audit` | Session save/load and vector-index failures; audit writes. Missing sessions encountered during ordinary store probing are excluded. |
| `maintenance` | Each failed sweep category: sessions, audit, runtime retention, rotation, plans, artifacts, media. All failed categories are logged even though the command returns only the first error. |
| `serve` | Listener/shutdown failures, surface-log initialization, HTTP 4xx/5xx responses, WebSocket error responses, and contained turn/run panics. HTTP operations use registered route patterns, never request URLs, query strings, headers, or bodies. |
| `schedule` | Store reads/writes, failed job execution, and delivery failures. |
| `telegram` | API requests/uploads after retries finish and instrumented handler panics. |
| `mcp`, `sandbox` | MCP process setup/discovery and sandbox setup failures, including a failed sandbox setup followed by unsandboxed fallback. |
| `llm` | Auxiliary model calls used by memory, titles, compaction, and similar helpers. Main-loop failures retain `llm_call_failed` and `run_failed`. |

Common causes and checks:

| `error_class` | What to check |
| --- | --- |
| `provider_auth`, `provider_config` | Operator credentials, provider selection, and model configuration. |
| `rate_limited`, `provider_unavailable`, `provider_request` | Provider quota/availability and HTTP status. |
| `dns_failure`, `connection_refused`, `network_timeout`, `tls_certificate` | Endpoint, connectivity, DNS, and certificate trust. |
| `permission_denied`, `read_only_filesystem`, `invalid_file_type`, `symlink_rejected` | Ownership, permissions, and expected regular files/directories. |
| `disk_full`, `file_descriptors_exhausted` | Free space or process/system file-descriptor limits. |
| `address_in_use` | Another process already using the configured listening address. |
| `executable_not_found`, `process_exit` | Required executable availability or subprocess exit diagnostics. |
| `invalid_json` | Configuration or persisted data syntax/schema. |
| `context_canceled`, `deadline_exceeded`, `stream_idle_timeout`, `execution_budget` | Cancellation, timeouts, and configured budgets. |

A single incident can produce several records: for example, a provider failure,
a failed run, and a failed command. These describe different boundaries, not
three independent incidents. Use process/session/turn correlation when counting
or investigating failures.

### Startup and fallback behavior

The CLI initializes operational logging before parsing the command or constructing
an agent. It reads only operator logging policy; project configuration cannot
turn it on. Diagnostics generated while reading that policy are buffered (up to
128 records) and flushed when logging opens. Version queries and
`cleanup --dry-run` bypass operational logging so previews remain read-only.
If the operator config itself is unreadable or invalid, use
`ODEK_LOGGING_ENABLED=true` to explicitly enable startup diagnostics:

```sh
ODEK_LOGGING_ENABLED=true odek serve
```

Logging remains off by default. Existing stderr and surface diagnostics remain
available. If the runtime log cannot open or storage fails, stderr is the
fallback; the failed log cannot reliably report its own failure. The logger
also reports queue losses and shutdown timeouts there.

Delegated children route operational diagnostics through their serialized parent
telemetry channel once initialized. Earlier child setup failures remain visible
through parent-observed subagent failure/completion records. Standalone subagent
commands initialize operational logging like other CLI commands.

This is best-effort diagnostics, not a crash recorder: SIGKILL, OOM termination,
fatal runtime errors, and panics outside instrumented boundaries can lose the
last records. SDK-internal retry attempts and arbitrary third-party stderr are
not captured. Go library users get agent events via `RuntimeLogPath`; the
process-wide application diagnostic sink is installed by the CLI.

## Content and delivery

Fields are explicitly allowlisted. Goals, prompts, answers, raw arguments,
argument summaries/paths, tool output, denial text, stderr text, and artifact
content are excluded. Selected identifiers and metadata strings are bounded and
secret-redacted. Tool and model names remain visible. Files and lock files use
0600 permissions; log targets must be regular files and may not be symlinks.

A bounded background queue isolates execution and child stdout readers from
disk I/O. Queue overflow and write failures produce throttled stderr diagnostics;
event-emitter losses produce `logging_dropped` records on shutdown when possible.
Ordinary lock contention retries the pending batch. Shutdown drains for at most
two seconds before reporting possible loss. Logging is best effort: crashes,
forced termination, a full queue, or failing storage can lose records. Routine
writes do not fsync each event; `--events-jsonl` retains its separate durability
contract.

## Rotation and expiration

Runtime log expiration is integrated into the existing storage janitor. The
shared maintenance sweep prunes expired runtime records before rotating logs;
the background services and `odek cleanup` call that same sweep. No separate
logging daemon or expiration command is needed.

### Settings

All settings below belong in the operator's `~/.odek/config.json`. Environment
variables take precedence over the file; project configuration cannot change
these settings.

| Setting | Default | Environment override | Effect |
| --- | --- | --- | --- |
| `logging.enabled` | `false` | `ODEK_LOGGING_ENABLED` | Write new runtime records. Turning it off does not prevent cleanup of existing logs. |
| `maintenance.enabled` | `true` | `ODEK_MAINTENANCE_ENABLED` | Start the background janitor in supported long-lived services. Manual cleanup remains available when false. |
| `maintenance.interval_minutes` | `60` | `ODEK_MAINTENANCE_INTERVAL_MINUTES` | Time between background sweeps. Non-positive values use the 60-minute default. |
| `maintenance.runtime_log_max_age_hours` | `168` | `ODEK_MAINTENANCE_RUNTIME_LOG_MAX_AGE_HOURS` | Expire runtime records older than this many hours. `0` disables age-based expiration. |
| `maintenance.log_max_mb` | `50` | `ODEK_MAINTENANCE_LOG_MAX_MB` | Rotate oversized logs, in MiB. `0` disables size rotation. This existing setting also applies to other surface logs. |

Runtime retention hours clamp to the range 0–36,500. Negative values therefore
disable age-based expiration. Restart a long-lived service after changing its
janitor configuration: the running janitor uses the policy loaded at startup.

### When cleanup runs

| Execution mode | Background expiration |
| --- | --- |
| `odek serve` | Runs while the server is active and maintenance is enabled. |
| `odek telegram` | Runs while the bot is active and maintenance is enabled. |
| `odek schedule daemon` | Runs while the scheduler daemon is active and maintenance is enabled. |
| CLI run/continue, REPL, standalone subagent | Does not start a background janitor. Use manual cleanup or a running service above. |
| `odek cleanup` | Performs one sweep immediately, even if `maintenance.enabled` is false. |
| `odek cleanup --dry-run` | Reports candidates without changing logs or creating a log lock file. |

The first background sweep occurs **after one interval**, not immediately at
startup. With the default settings, a record becomes eligible after seven days
and is removed at a subsequent hourly sweep. Expiration is not an exact deletion
deadline: the service must be running and the sweep must succeed. Restarting a
service starts a new interval; it does not perform an immediate catch-up sweep.

### What expiration removes

The janitor removes records whose `timestamp` is older than the cutoff from
both `~/.odek/runtime.log` and `~/.odek/runtime.log.1`. The cutoff is the sweep
time minus the configured number of hours. Eligibility uses each record's
timestamp, not the file modification time, session age, or task completion
status. An old record in an active session can expire while newer records for
that same session remain.

Recent records, malformed records, and records without a usable timestamp are
retained. Expiration does not delete sessions or subagent artifacts, and it
does not age-prune `serve.log`, `telegram.log`, `schedule.log`, custom
`--events-jsonl` destinations, or custom Go API runtime-log paths. Those other
storage categories retain their own maintenance rules. See
[Storage maintenance](MAINTENANCE.md).

Each file replacement is atomic. If scanning a file fails, that file is left
intact; changes already completed to the other file are not rolled back. The
background janitor reports failures in `runtime.log` when operational logging is
enabled, also reports the first error on stderr, and tries again at its next
scheduled sweep. A successful background sweep is quiet.

### Size rotation and concurrent writers

Runtime writers check size before appending each batch. When the current file
exceeds `maintenance.log_max_mb`, it becomes `runtime.log.1`, replacing the
previous backup. There is one backup generation. The janitor also checks size
during its sweep. A batch can take the file above the threshold before the next
check, so the setting is a rotation threshold rather than a strict byte cap.

Writers and the janitor share a stable `runtime.log.lock` and reopen the log for
each batch. Rotation and expiration therefore cannot leave a cooperating writer
appending to an obsolete file. Writers retain and retry their pending batch
while cleanup holds the lock; their bounded queues and shutdown limits still
apply. Use `tail -F` to follow the log when rotation or pruning replaces it.

Disabling the background janitor delays expiration until manual cleanup; size
rotation still runs in the writer. Size rotation may discard records sooner
than the age limit. To retain records without either automatic size eviction or
age expiration, set **both** `log_max_mb` and `runtime_log_max_age_hours` to `0`.

### Previewing and running cleanup

Use the policy currently configured for the operator:

```sh
odek cleanup --dry-run
odek cleanup
```

An expiration-only preview includes a line such as:

```text
  runtime records expired: 42
```

The completed sweep reports:

```text
  runtime records removed: 42
```

These counts cover both current and backup runtime logs. Preview and execution
can differ if records arrive, rotate, or cross the age cutoff between commands.
`odek cleanup` also applies the configured retention rules for sessions, audit
records, plans, artifacts, and media; it is not a log-only command.

To preview a different runtime retention period for one invocation:

```sh
ODEK_MAINTENANCE_RUNTIME_LOG_MAX_AGE_HOURS=24 odek cleanup --dry-run
```

This environment override does not edit the global configuration or change the
policy of services already running. Remove `--dry-run` to perform that sweep.

### Troubleshooting retention

| Observation | Check |
| --- | --- |
| Old records remain just after starting serve or Telegram | Wait for the first maintenance interval, or run `odek cleanup` immediately. |
| Old records remain after CLI-only usage | CLI runs write logs but do not host a janitor. Use manual cleanup or a long-lived service. |
| Records never expire by age | Check `runtime_log_max_age_hours`, environment overrides, and whether the background janitor is enabled and running. A value of `0` disables expiration. |
| A record survives an otherwise successful sweep | Check its timestamp. Missing, malformed, and future timestamps do not qualify for expiration at the current cutoff. |
| Logs disappear sooner than seven days | Size rotation can replace the single backup before the age limit is reached. |
| A sweep reports an error | Inspect stderr for `odek: maintenance sweep:` or the manual cleanup error. Check permissions, regular-file targets, available disk space for replacement files, and lock contention. |
| An old `serve.log` entry remains | Runtime record expiration applies only to `runtime.log` and its backup. `serve.log` follows the existing size-rotation policy. |
