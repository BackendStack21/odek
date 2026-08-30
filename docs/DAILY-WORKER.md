# odek as a Headless Daily Worker

Patterns for using odek as an unattended coding worker — from the terminal,
from another agent's tool loop, or on a schedule.

## What odek gives a headless run

- **OS-process isolation**: sub-agents via `delegate_tasks` run as real
  `odek subagent` processes with their own config and termination timeout.
- **Docker sandbox**: shell commands execute in a container by default
  (see [SANDBOXING.md](SANDBOXING.md)).
- **Three-tier memory** (facts, buffer, episodes) plus the opt-in Extended
  Memory atom store (see [MEMORY.md](MEMORY.md)).
- **Native cron**: `odek schedule` for recurring jobs with delivery
  (see [SCHEDULES.md](SCHEDULES.md)).
- **Browser, vision, and audio tools**: `browser`, `vision`, and `transcribe`
  are available to headless runs like any other tool.

## Pattern 1 — Direct CLI delegation

Best for: focused coding work (refactor, implement, debug).

```bash
# One-shot: result to stdout, nothing persisted as a session
odek run "Refactor auth to context-based middleware"

# Multi-turn session: saves the conversation for later `odek continue`
odek run --session "Refactor auth to context-based middleware"

# Sandbox is on by default; make Docker unavailability fatal with:
odek run --sandbox "Run the full test suite"
```

`--session` is a **boolean flag** — the task text is the positional argument.
Sessions persist per completed step, so an interrupted run resumes with
`odek continue`.

**Pros:** zero infrastructure, full tool surface, session persistence.
**Cons:** the caller has no tool access while the run is in flight.

## Pattern 2 — MCP bridge (bidirectional tool sharing)

Best for: letting another MCP client (an orchestrating agent, an IDE) call
odek's tools — sandboxed shell, sub-agents, perf tools — mid-task.

```
      ┌──────────────┐     MCP stdio      ┌──────────────┐
      │  MCP client  │◄──────────────────►│    odek      │
      │ (orchestrator│                    │              │
      │  IDE, agent) │                    │  shell/files │
      └──────────────┘                    │  sub-agents  │
                                          │  sandbox     │
                                          │  skills      │
                                          └──────────────┘
```

```bash
# Register odek as an MCP server in the client's config:
# (e.g. Claude Code / Cursor / any MCP client)
mcp_servers:
  odek:
    command: odek
    args: [mcp]
```

The client then calls tools under the `<server>__<tool_name>` convention —
`odek__shell`, `odek__read_file`, `odek__patch`, etc. Two tools are never
exposed: `delegate_tasks` and `memory` (see [MCP.md](MCP.md)).

**Pros:** bidirectional tool access without context switching.
**Cons:** more moving parts; MCP serialization latency.

## Pattern 3 — Web UI for long-running sessions

Best for: complex multi-hour tasks you want to watch live.

```bash
odek serve --addr 127.0.0.1:3001
# Open http://127.0.0.1:3001 — live token stats, tool calls, approvals
```

## Pattern 4 — Scheduled jobs (recommended daily driver)

For recurring work, prefer the native scheduler over external cron: jobs get
missed-run catch-up, delivery to Telegram, and the unattended safety floor
(see [SCHEDULES.md](SCHEDULES.md)).

```bash
# Nightly test-suite run with the log delivered to Telegram
odek schedule add --cron "0 4 * * *" --deliver telegram \
  "Run the full test suite and summarize failures"

# Preview upcoming fire times before saving
odek schedule next "0 4 * * *"
```

For one-shot host-cron jobs, `odek run --deliver "task"` sends the final
answer to the configured Telegram `default_chat_id`.

## Shared state

All patterns share `~/.odek/`: skills (`~/.odek/skills/`), memory
(`~/.odek/memory/`), and sessions (`~/.odek/sessions/`). A run started from
any surface can be continued from another with `odek continue --id <id>`.
