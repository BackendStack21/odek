# Storage Maintenance

odek accumulates local state under `~/.odek/` — session transcripts, prompt-
injection audit records, plans, skill skip lists, and log files. The storage
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
| Skill skip entries | `~/.odek/skills/.skipped.json` | `skills_skip_max_age_days` | 90 days |
| Telegram media | `~/.odek/media/` (by mtime) | fixed: 1 hour | freed bytes reported |
| Logs | `~/.odek/telegram.log`, `~/.odek/schedule.log` | `log_max_mb` | 50 MB (rotated) |

Age for sessions is measured from the session's `updated_at`; for audit
records, plans, and media from the file's modification time. Oversized logs
are **rotated**, not deleted — the current log is renamed to `<name>.1`
(one backup generation) and a fresh log is started. The sweep report includes
the rotated paths. Downloaded Telegram media is transient and expires after a
fixed 1 hour.

## What is NEVER touched

The janitor only expires the categories above. It never touches:

- **Memory** — atoms, facts, episodes, buffers (`~/.odek/memory/`)
- **Skill files** — `SKILL.md` definitions (only stale `.skipped.json`
  entries age out)
- **Schedules** — `schedules.json`, `schedule-state.json`
- **Trust anchors** — `config.json`, `secrets.env`, `IDENTITY.md`,
  approval stores, lock files, and everything else under `~/.odek/` that is
  not in the "What is cleaned" table

## Configuration

The `[maintenance]` section (all keys optional — defaults shown):

```json
{
  "maintenance": {
    "enabled": true,
    "interval_minutes": 60,
    "sessions_max_age_days": 30,
    "audit_max_age_days": 14,
    "log_max_mb": 50,
    "plans_max_age_days": 30,
    "skills_skip_max_age_days": 90
  }
}
```

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Run the background janitor in long-lived processes |
| `interval_minutes` | `60` | Minutes between automatic sweeps |
| `sessions_max_age_days` | `30` | Delete sessions older than this |
| `audit_max_age_days` | `14` | Delete prompt-injection audit records older than this |
| `log_max_mb` | `50` | Rotate logs larger than this |
| `plans_max_age_days` | `30` | Delete plans older than this |
| `skills_skip_max_age_days` | `90` | Drop skill skip entries older than this |

The maintenance config is **operator-only**: like `base_url`, `api_key`, and
the `dangerous` section, it is honored from `~/.odek/config.json` (and process
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
  skip entries removed:  5
  media freed:           48.2 MB
  log rotated:           /home/you/.odek/schedule.log
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
  skip entries:        5
  log rotated:         /home/you/.odek/schedule.log
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
