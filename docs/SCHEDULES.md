# Scheduled Tasks (native cron)

odek can run agent tasks on a cron schedule and deliver each result somewhere —
a Telegram chat, stdout, or a log file. The scheduler is **native and
in-process**: it runs inside a long-lived odek process that has already
resolved its configuration (API key, model, bot token, default chat) into
memory. A scheduled task therefore sees exactly what an interactive `odek run`
does — no environment-inheritance problems, no external cron daemon, no
container-only behaviour.

```bash
# A weekday stand-up nudge delivered to Telegram
odek schedule add --cron "0 9 * * 1-5" --deliver telegram "Remind me: stand-up in 15 minutes"

# Run the scheduler (headless), or just start `odek telegram` — it hosts one too
odek schedule daemon
```

---

## Where it runs

The same engine runs in two places; pick whichever fits your deployment:

| | Use when |
|---|---|
| **Inside `odek telegram`** | You already run the bot. The scheduler starts automatically as part of the bot process — one process for chat + reminders. |
| **`odek schedule daemon`** | You don't run the bot (headless server, CI box). A dedicated foreground process that only schedules. |

A shared lock (`~/.odek/schedule.pid`) coordinates the two so jobs never fire
twice — but the two sides handle contention differently: if a daemon already
holds the lock, the bot's embedded scheduler **defers silently** (the bot keeps
running, just without scheduling); if the bot holds it, a standalone
`odek schedule daemon` **refuses to start** and exits non-zero. (Disable the
bot's embedded scheduler with `schedules.enabled = false` if you prefer to run
the daemon separately.)

---

## Managing jobs

```text
odek schedule list                          List jobs: id, on/off, cron, next fire, last status
odek schedule add --cron "<expr>" <task>    Add a job (flags below)
odek schedule rm <id>                       Remove a job
odek schedule enable  <id>                  Enable a job
odek schedule disable <id>                  Disable a job (kept, never fires)
odek schedule run  <id>                     Run a job once now and deliver (test it)
odek schedule next <id|"cron-expr">         Preview the next few fire times
odek schedule daemon                        Run the scheduler in the foreground
```

### `add` flags

| Flag | Meaning |
|---|---|
| `--cron "<expr>"` | 5-field cron or `@macro` (required) |
| `--name <label>` | Human label (defaults to the first words of the task) |
| `--deliver <dest>` | `stdout` (default), `log`, `telegram`, or `telegram:<chatID>` |
| `--tz <IANA>` | Timezone, e.g. `Europe/Berlin` (default UTC) |
| `--catchup` | If a fire was missed while the process was down, run once on startup |
| `--disabled` | Add without enabling |

Definitions are stored in `~/.odek/schedules.json` (mode `0600`); runtime state
(last run, status, next fire) lives in `~/.odek/schedule-state.json`. A running
scheduler picks up edits to the definitions file automatically (no restart).

---

## Managing from Telegram

When you run `odek telegram`, the same jobs can be managed from inside the chat
— no shell access needed. Two slash commands mirror the CLI:

```text
/schedules                                  List jobs (id, on/off, cron, next fire, last status)
/schedule add <cron> <task> [| opts]        Add a job (delivered to this chat by default)
/schedule view <id>                         Show a job's full detail + recent status
/schedule next <id|cron>                    Preview the next few fire times
/schedule run <id>                          Run a job once now, in this chat
/schedule enable | disable <id>             Toggle a job
/schedule rm <id>                           Remove a job
```

Because a cron expression has a fixed shape, `add` needs no quoting: an
`@macro` is one token and a classic expression is exactly **five** fields; the
rest of the line is the task. Options come after a literal `|`:

```text
/schedule add 0 9 * * 1-5 Summarize my unread email
/schedule add @daily Daily standup digest | tz=Europe/Berlin name=standup
/schedule add */15 9-17 * * 1-5 Check the build | deliver=telegram catchup
```

| Option (after `|`) | Meaning |
|---|---|
| `deliver=<dest>` | `stdout`, `log`, `telegram`, or `telegram:<chatID>`. **Default: this chat.** |
| `tz=<IANA>` | Per-job timezone, e.g. `Europe/Berlin` |
| `name=<label>` | Human label (single token; default: first words of the task) |
| `catchup` | Run a missed fire once on startup |
| `disabled` | Add without enabling |

Notes:

- **Delivery defaults to the current chat** (unlike the CLI, which defaults to
  `stdout`) — adding a job from a conversation sends its results back there.
- `/schedule run` executes the task **now, in this chat**, through the normal
  agent pipeline (you see progress and answer any approval prompts) — a safe way
  to test a job. It does not change the job's own schedule or delivery.
- Edits made from Telegram take effect **immediately** (the embedded scheduler
  reconciles on the spot, not on the ~30 s poll).
- Only chats/users on the bot's allowlist (`ODEK_TELEGRAM_ALLOWED_CHATS` /
  `ALLOWED_USERS`) reach these commands. To keep schedule **management**
  CLI-only while still allowing in-chat listing/preview, set
  `schedules.allow_telegram_management = false` (read-only verbs still work).

---

## Cron syntax

Standard 5-field Vixie cron:

```text
┌ minute        0-59
│ ┌ hour        0-23
│ │ ┌ day-of-month 1-31
│ │ │ ┌ month   1-12 or JAN-DEC
│ │ │ │ ┌ day-of-week 0-6 or SUN-SAT (0 and 7 are both Sunday)
* * * * *
```

Each field accepts `*`, a value, a range `a-b`, a step `*/n` / `a-b/n` / `a/n`,
and comma-separated lists. Macros: `@hourly`, `@daily` (`@midnight`),
`@weekly`, `@monthly`, `@yearly` (`@annually`).

Granularity is **one minute** (no seconds field). Times are in the job's `--tz`
or, failing that, the scheduler's default timezone (UTC unless configured).

**Day-of-month / day-of-week coupling** follows Vixie semantics: when *both*
fields are restricted, a day matches if *either* matches. So `0 0 13 * 5` fires
on the 13th **or** any Friday — not only Friday the 13th.

```bash
odek schedule next "0 9 * * 1-5"   # validate an expression and see upcoming fires
```

---

## Delivery

| Kind | Result goes to |
|---|---|
| `stdout` | the daemon's stdout (or the bot's container logs) |
| `log` | appended to `~/.odek/schedule.log` |
| `telegram` | the configured `telegram.default_chat_id` |
| `telegram:<chatID>` | a specific chat |

Telegram delivery needs `telegram.bot_token` and a chat ID
(`ODEK_TELEGRAM_BOT_TOKEN` + `ODEK_TELEGRAM_DEFAULT_CHAT_ID`, or per-job
`telegram:<chatID>`). When delivering from inside `odek telegram`, the live bot
client is reused (shared rate limiting).

---

## Safety: unattended tasks

A scheduled task runs with **no human present to approve actions**. It inherits
the process's existing danger policy (`dangerous` in config) exactly as a
non-interactive `odek run` would:

- **Restricted profile** → destructive / code-execution / network-write
  operations are denied; read/summarise/deliver tasks work.
- **Godmode profile** → full access, unattended. Only point scheduled jobs at
  godmode if you trust every task definition.

Task definitions in `schedules.json` are owner-authored (same trust level as
`config.json`); the file is written `0600`.

---

## Configuration

The `schedules` config section (in `~/.odek/config.json` or `./odek.json`) tunes
the engine. Every field also has an `ODEK_SCHEDULES_*` environment override.

```json
{
  "schedules": {
    "enabled": true,
    "max_concurrent": 2,
    "timezone": "UTC",
    "catchup": false,
    "allow_telegram_management": true
  }
}
```

| Field | Env | Default | Meaning |
|---|---|---|---|
| `enabled` | `ODEK_SCHEDULES_ENABLED` | `true` | Run the embedded scheduler inside `odek telegram` |
| `max_concurrent` | `ODEK_SCHEDULES_MAX_CONCURRENT` | `2` | Max jobs running at once |
| `timezone` | `ODEK_SCHEDULES_TIMEZONE` | `UTC` | Default timezone for jobs without `--tz` |
| `catchup` | `ODEK_SCHEDULES_CATCHUP` | `false` | Global default for the missed-run policy |
| `allow_telegram_management` | `ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT` | `true` | Allow the in-chat `/schedule` commands to add/remove/toggle/run jobs (read-only listing always works) |

---

## Missed runs

If the scheduler was down when a job was due, on startup it either **skips**
(default — reschedules forward and records a `skipped` status) or **runs once**
(when the job's `--catchup` or `schedules.catchup` is set). A burst of missed
ticks never stampedes: at most one catch-up fire per job.
