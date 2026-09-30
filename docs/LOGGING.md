# Runtime logging

odek writes one local, structured operational log across CLI runs, the REPL,
Web UI, Telegram, scheduled jobs, and delegated tasks. The default file is
`~/.odek/runtime.log`; logging is enabled by default for CLI commands. The log
uses the `odek.log/v1` JSONL schema and keeps operational metadata, not
conversation content.

Configure it in the operator's `~/.odek/config.json`:

```json
{
  "logging": {
    "enabled": true,
    "level": "info",
    "file": "~/.odek/runtime.log",
    "max_file_mb": 25,
    "max_files": 4,
    "max_age_hours": 168
  }
}
```

The equivalent environment variables are `ODEK_LOGGING_ENABLED`,
`ODEK_LOGGING_LEVEL`, `ODEK_LOGGING_FILE`, `ODEK_LOGGING_MAX_FILE_MB`,
`ODEK_LOGGING_MAX_FILES`, and `ODEK_LOGGING_MAX_AGE_HOURS`. They override the
operator config. Project `odek.json` cannot change logging policy. `odek init
--global` includes the logging settings. `odek serve` no longer has a separate
log-file option, and the Telegram and scheduler surfaces do not create their
own application log files. Relative `logging.file` values are rooted under
`~/.odek/`; absolute paths and `~/...` paths are preserved.

The defaults retain the active file plus up to three numbered backups and
records from the last seven days. Rotation discards the oldest generation when
the configured file count is reached. The same policy applies to custom paths.
The configured file-count cap is enforced independently of age pruning and
size rotation, including by maintenance and explicit cleanup.
Set `max_files` to `1` to keep only the active file, or `max_age_hours` to `0`
to disable age-based pruning. `max_file_mb` set to `0` disables size rotation.
The runtime logger owns log retention and rotation; maintenance controls when
the janitor runs. An enabled CLI logger applies retention when it starts, and
`odek cleanup` runs it on demand. With `logging.enabled=false`, ordinary CLI
commands do not open, write, or prune the log; a maintenance-enabled long-lived
service still schedules retention, and cleanup remains available. Changing the
policy takes effect for the next process or cleanup sweep.

| Setting | Default | Environment override | Effect |
|---|---:|---|---|
| `logging.enabled` | `true` | `ODEK_LOGGING_ENABLED` | Enable operational log writes and startup pruning for CLI commands. |
| `logging.level` | `info` | `ODEK_LOGGING_LEVEL` | Minimum severity persisted. Set to `debug` to retain detailed tool and model-call lifecycle records. |
| `logging.file` | `~/.odek/runtime.log` | `ODEK_LOGGING_FILE` | Active JSONL path; numbered backups use the same path plus `.1`, `.2`, and so on. |
| `logging.max_file_mb` | `25` | `ODEK_LOGGING_MAX_FILE_MB` | Rotate the active file after it grows beyond this size. `0` disables size rotation. |
| `logging.max_files` | `4` | `ODEK_LOGGING_MAX_FILES` | Maximum total files, including the active file; allowed range is 1–32. |
| `logging.max_age_hours` | `168` | `ODEK_LOGGING_MAX_AGE_HOURS` | Remove records older than this many hours from configured generations. `0` disables age-based pruning. |

## Querying records

Use `odek logs` to query retained records. With no filters it prints the latest
100 matching records in compact local-time format. The default query threshold
comes from `logging.level`; `odek logs --level` changes which retained records
are displayed, while `logging.level` controls what the writer persists:

```sh
odek logs
odek logs --level warn --since 30m
odek logs --surface telegram --status failed --since 2h
odek logs --session SESSION_ID --json
odek logs --run RUN_ID --tree --json
odek logs --error-code provider.auth --limit 0
odek logs --follow --level warn
```

Supported filters and output options:

| Option | Meaning |
|---|---|
| `--level debug\|info\|warn\|error` | Minimum severity to display; defaults to `logging.level`. `warning` is accepted as an alias for `warn`. |
| `--since <duration\|time>` | Start time, as elapsed duration (for example `30m`) or RFC3339 timestamp. |
| `--until <time>` | End time as an RFC3339 timestamp. |
| `--surface <name>` | Exact surface, case-insensitive. |
| `--status <value>` | Exact status, case-insensitive. |
| `--session <id>` | Exact session ID. |
| `--turn <id>` | Exact turn ID. |
| `--run <id>` | Exact run ID. |
| `--tree` | Recursively include descendant runs; requires `--run`. An unfinished row is not evidence that a process is alive. |
| `--error-code <code>` | Exact error code, case-insensitive. |
| `--limit <n>` | Maximum initial matches; defaults to `100`, and `0` means all matches within the bounded query buffer. The buffer is capped at 16 MiB and 200,000 records; truncation is reported on stderr. |
| `--follow` | Print new matches after the initial retained tail, including across rotation. |
| `--json` | Emit matching records as JSONL, preserving the flat record fields. |
| `--file <path>` | Read a custom log path. |

IDs are exact-match filters. Use `--json` when piping into tools such as `jq`:

```sh
odek logs --session SESSION_ID --json | jq -c 'select(.event == "run.finished" and .status == "failed")'
odek logs --since 2026-09-30T10:00:00Z --until 2026-09-30T11:00:00Z --json
```

## Event fields and correlation

Each JSON line is a flat `odek.log/v1` record with a timestamp, lowercase
severity, dot-separated event name, and bounded metadata. Common fields include
`surface`, `process_id`, `pid`, `session_id`, `turn_id`, `run_id`,
`parent_run_id`, `root_run_id`, `task_id`, `call_id`, `status`, `duration_ms`,
`error`, and `metadata`. Delegation records may also include `parent_task_id`,
`parent_turn_id`, and `source_task_id`. Fields are omitted when not applicable.

The identifiers describe different scopes:

| Field | Meaning |
|---|---|
| `session_id` | Conversation shared across turns and delegated children. |
| `record_id` | Unique record identifier within the writing process; used by `--follow` to suppress replay when rotation or pruning replaces a file. It is not a conversation correlation ID. |
| `turn_id` | User-visible turn, including shared child work for that turn. |
| `run_id` | One agent invocation; each invocation gets a fresh ID. |
| `root_run_id` | Root invocation for a delegation tree. |
| `parent_run_id` | Immediate parent invocation for delegated work. |
| `task_id` | Delegated task identity, where applicable. |
| `parent_task_id` | Immediate delegated-task ancestor, when a task is nested. |
| `parent_turn_id` | Turn ID of the process that delegated this task. |
| `source_task_id` | Direct child task whose IPC stream supplied a relayed record. |
| `call_id` | Tool call identity shared by related lifecycle records. |
| `process_id`, `pid` | Random writer identity and operating-system process ID, including setup records before a session exists. `record_id` combines the writer identity with a sequence number. |

The schema is separate from the `odek.event/v1` event stream exposed through
`--events-jsonl` and the Go `EventHandler` API. Those interfaces remain
available for consumers that need loop events. Runtime logs are local
operational diagnostics and are queried with `odek logs`.

Run and turn boundaries use events such as `run.started`, `run.finished`, and
`turn.started`. Service lifecycle records use `service.started` and
`service.stopped`. Model, tool, budget, and delegated-task records carry only
correlation IDs, statuses, durations, and selected usage metadata. A delegation
tree can include both child task status and parent-observed process exit status;
they answer different questions. An unfinished record may simply mean the
process ended before its terminal event could be written, so it does not prove
that work remains active.

`run.finished` uses status `completed`, `failed`, or `cancelled`. A delegated
task's `subagent.finished` status describes its result; `metadata.exit_status`
and `metadata.exit_code` describe what the parent observed from the child
process. A partial task result can therefore accompany a nonzero process exit.

## Privacy and delivery

The writer allowlists fields. Prompts, model responses, tool arguments and
results, request bodies, arbitrary error strings, panic values, and stack traces
are excluded. Errors are represented by canonical error codes and, when
available, HTTP status and retry count. Metadata contains only selected,
bounded values. The `--events-include-args` option does not affect runtime logs.

The log is best-effort diagnostics, not an audit journal. A bounded queue keeps
disk I/O away from the agent loop; queue overflow, storage failures, or forced
process termination can lose records. Files and lock files are restricted to
the current user, symlink targets are rejected, and writers coordinate with
retention through a stable lock. `odek logs --follow` follows the active file
as rotation replaces generations.

Following uses record IDs from every valid retained record, including records
outside the displayed filters and initial `--limit`, to avoid replay across
rotation. It fails with a diagnostic if a retained record lacks a valid ID or
if the bounded dedup index would exceed 4,096 writer processes or 1,000,000
disjoint sequence ranges. IDs are limited to 128 bytes (96 bytes for the writer
token); these bounds keep follow state predictable instead of silently
forgetting IDs and replaying records.

The CLI initializes logging early enough to capture startup and configuration
diagnostics. If the runtime file cannot be opened or written, a concise
diagnostic goes to stderr. Version checks and `odek cleanup --dry-run` do not
write runtime records. Go library users opt in explicitly by setting
`Config.RuntimeLogPath`; the library does not write under the caller's home
directory on its own.

See [Configuration](CONFIG.md#storage-maintenance) and
[Storage maintenance](MAINTENANCE.md) for janitor scheduling and retention.
