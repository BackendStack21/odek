# Storage Maintenance

odek accumulates local state under `~/.odek/` — session transcripts, prompt-
injection audit records, plans, and log files. The storage
**janitor** keeps that growth bounded: a background sweep that removes expired
entries and rotates oversized logs, plus an `odek cleanup` command for one-shot,
operator-invoked runs.

```bash
# Sweep expired storage once, right now
odek cleanup

# See what a sweep WOULD remove, without deleting anything
odek cleanup --dry-run
```

---

## Where the janitor runs

The background janitor starts automatically in every long-lived odek process
(when `maintenance.enabled` is true):

| Process | Notes |
|---|---|
| **`odek telegram`** | Starts with the bot, stops on shutdown. |
| **`odek serve`** | Starts with the Web UI server. |
| **`odek schedule daemon`** | Starts with the scheduler daemon. |

Each prints `odek: storage maintenance enabled (interval 60m)` at startup. The
janitor sweeps on a fixed interval and stops with the process. Short-lived
commands (`odek run`, `odek repl`, …) do not run the janitor — use
`odek cleanup` for those workflows.

## What is cleaned

| Category | Location | Retention key | Default |
|---|---|---|---|
| Sessions | `~/.odek/sessions/*.json` (by `updated_at`) | `sessions_max_age_days` | 30 days |
| Audit records | `~/.odek/sessions/audit/*.json` (by mtime) | `audit_max_age_days` | 14 days |
| Plans | `~/.odek/plans/**/*.md` (by mtime) | `plans_max_age_days` | 30 days |
| Sub-agent artifacts | `~/.odek/artifacts/<session>/<task>/` and `~/.odek/artifacts/unfiled/<task>/` (task dirs by their own mtime; aged session dirs also go wholesale) | `artifacts_max_age_hours` | 24 hours (backstop — live removal happens on session delete) |
| Telegram media | `~/.odek/media/` (by mtime) | fixed: 1 hour | freed bytes reported |
| Runtime log | Configured `logging.file` (default `~/.odek/runtime.log`) and numbered backups | `logging.max_age_hours`, `logging.max_file_mb`, `logging.max_files` | 168 hours, 25 MiB, 4 files total |

Age for sessions is measured from the session's `updated_at`; for audit
records, plans, and media from the file's modification time. Sub-agent
artifacts age per **task dir**: each expired `task-*` subtree is removed
individually inside its parent (session dir or the shared `unfiled` bucket),
and a parent left empty is pruned. Runtime log retention and rotation follow
the operator-only `logging` policy. Its maximum file count is enforced even
when age pruning and size rotation are disabled. An enabled CLI logger applies retention at
process startup; the janitor repeats that policy on its interval, and
`odek cleanup` uses the same policy on demand. If logging is disabled, an
ordinary CLI command does not prune the log. The active file and numbered
backups are age-pruned and rotated to the configured total file count.
Downloaded Telegram media is transient and expires after a fixed 1 hour.

## What is NEVER touched

The janitor only expires the categories above. It never touches:

- **Memory** — atoms, facts, episodes, buffers (`~/.odek/memory/`)
- **Skill files** — `SKILL.md` definitions
- **Schedules** — `schedules.json`, `schedule-state.json`
- **Trust anchors** — `config.json`, `secrets.env`, `IDENTITY.md`,
  approval stores, schedule/session lock files, and everything else under
  `~/.odek/` that is not in the "What is cleaned" table. The runtime logger's
  `.lock` sidecar coordinates writes and rotation; it is not a retention target.

## Configuration

The `[maintenance]` section (all keys optional — defaults shown):

```json
{
  "maintenance": {
    "enabled": true,
    "interval_minutes": 60,
    "sessions_max_age_days": 30,
    "audit_max_age_days": 14,
    "plans_max_age_days": 30,
    "artifacts_max_age_hours": 24
  }
}
```

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Run the background janitor in long-lived processes |
| `interval_minutes` | `60` | Minutes between automatic sweeps |
| `sessions_max_age_days` | `30` | Delete sessions older than this |
| `audit_max_age_days` | `14` | Delete prompt-injection audit records older than this |
| `plans_max_age_days` | `30` | Delete plans older than this |
| `artifacts_max_age_hours` | `24` | Sweep sub-agent artifact task dirs older than this, in any parent (including `unfiled`; emptied parent dirs are pruned; `0` keeps them forever; live removal still happens on session delete) |

The maintenance config is **operator-only**: like `provider` / `providers`,
and the `dangerous` section, it is honored from `~/.odek/config.json` (and process
environment) but **ignored from a project-level `./odek.json`**, so a checked-
out repository cannot disable the janitor or relax its own retention.

## `odek cleanup`

`odek cleanup` runs one sweep immediately, using the same resolved config as
the background janitor, and prints a per-category report:

```
$ odek cleanup
Cleanup complete:
  sessions removed:      12
  audit records removed: 34
  plans removed:         2
  artifacts removed:     3
  media freed:           48.2 MB
  runtime records removed: 12
```

When there is nothing to do it prints a single quiet line:

```
Storage is clean — nothing to remove.
```

`odek cleanup --dry-run` removes nothing and reports what a sweep would
remove:

```
$ odek cleanup --dry-run
Dry run — nothing removed. Would remove:
  sessions:            12
  audit records:       34
  plans:               2
  artifact subtree:    /home/you/.odek/artifacts/20260101-abc
  runtime records expired: 12
```

Like `odek session cleanup`, the command deletes data without a confirmation
prompt — it is a local, operator-invoked command. Use `--dry-run` first if
you want to inspect the candidate list.

## Related

- [CLI.md](CLI.md) — command reference
- [SCHEDULES.md](SCHEDULES.md) — the schedule daemon (hosts the janitor)
- [CONFIG.md](CONFIG.md) — full configuration reference
- `internal/session` — session files are also capped at 32 MiB **at write
  time**: an oversized transcript is trimmed (oldest turns first, keeping the
  system message and the most recent turns) so it never becomes unloadable.

See [Runtime logging](LOGGING.md) for runtime log expiration, correlation, and rotation semantics. Logging settings define retention and rotation; maintenance settings only schedule the janitor.
