# CLI Reference

## Commands

| Command | Description |
|---------|-------------|
| `odek run [flags] <task>` | Execute a task with the agent loop (single-shot by default) |
| `odek run --session [flags] <task>` | Execute and save conversation as a multi-turn session |
| `odek continue [--id <id>] [--external-ref <ref>] <task>` | Continue the most recent session (or by `--id`). Sessions persist per completed step: Ctrl-C/SIGTERM resumes from the last step; SIGKILL may lose the in-flight step |
| `odek repl [flags]` | Interactive REPL mode (persistent multi-turn session). Flags: `--id`, `--model`, `--thinking`, `--thinking-budget`, `--sandbox`, `--sandbox-*`, `--prompt-caching`, `--stream`, `--compaction`, `--planning` / `--no-planning`, `--interaction-mode`. Unrecognized flags are rejected with an error — in particular `--tool` / `--no-tool` are **not** supported in repl (use `odek run`, `serve`, or the `tools` config instead). |
| `odek session list` | List sessions |
| `odek session show [id]` | Show session details (default: latest) |
| `odek session delete <id>` | Delete a session |
| `odek session trim <id> <n>` | Keep only the `n` most recent messages |
| `odek session cleanup <days>` | Delete sessions older than N days (see also: automatic storage maintenance below) |
| `odek cleanup [--dry-run]` | One-shot storage sweep of `~/.odek`: expired sessions, audit records, plans, and oversized-log rotation, per the `[maintenance]` config. `--dry-run` previews without deleting. The same sweep runs automatically in the Telegram bot, `odek serve`, and `odek schedule daemon`. See [MAINTENANCE.md](MAINTENANCE.md) |
| `odek skill list` | List all available skills |
| `odek skill view <name>` | View a skill's full content |
| `odek skill delete <name>` | Delete a skill |
| `odek skill promote <name>` | Clear `NeedsReview` on a tainted skill so it can trigger-load and load via `skill_load` |
| `odek skill import <uri> [flags]` | Import a skill from file:// or https:// |
| `odek memory list` | List pending (pending-review) memory facts; aliases `ls`, `pending` |
| `odek memory promote <session-id>` | Promote a session's pending facts to the durable fact files |
| `odek memory extended <subcommand>` | Extended-memory atom management; see below |
| `odek memory extended <forget|promote|pin|quarantine|compact|stats|consolidate|nudges|pending|confirm|reject> [args]` | Extended-memory operations: delete/promote/pin atoms, list or confirm/reject pending-review atoms, quarantine listing, manual compaction, store stats, consolidate, and proactive-nudge management |
| `odek audit <session-id>` | Print the prompt-injection audit log for a session (JSON) |
| `odek audit --list` | List sessions with non-zero ingest counts and divergence flags |
| `odek serve [--addr <addr>] [--open] [--no-sandbox] [--trusted-proxies <ips/cidrs>] [--log-file <path>]` | Web UI server (default `127.0.0.1:8080`). Sandbox is on by default; pass `--no-sandbox` to disable. Flags: `--tool` / `--no-tool` (repeatable), `--prompt-caching`, `--compaction`, `--planning` / `--no-planning`, `--stream` / `--no-stream`, `--log-file` (durable run/turn log, default `~/.odek/serve.log`). Binding to a non-loopback address prints a loud warning because anyone with the token can drive the agent. `--trusted-proxies` honours `X-Forwarded-For`/`X-Real-Ip` only from those addresses. |
| `odek subagent --goal <string> [flags]` | Run a focused sub-task; outputs JSON on stdout. Spawned by `delegate_tasks` tool. Flags: `--goal`, `--task <file>`, `--context`, `--timeout` (≤1800s), `--max-iter` (≤100), `--profile <name>`, `--parent-session <id>`, `--quiet`, `--stream`. |
| `odek init [--global|--local] [--force]` | Create a config file template (scope-aware: full schema globally, project-safe fields locally) |
| `odek mcp [--sandbox]` | Start MCP server (expose tools to Claude Code) or connect to external MCP servers (via `mcp_servers` config) |
| `odek telegram` | Start the Telegram bot (long-polling). Hosts the embedded scheduler unless `schedules.enabled=false` |
| `odek schedule <subcommand>` | Manage native in-process scheduled tasks (cron): `list`, `add`, `rm`, `enable`, `disable`, `run`, `next`, `daemon`. See [Schedules](SCHEDULES.md) |
| `odek upgrade [--check]` | Self-upgrade to the latest GitHub release. Auto-detects OS/arch (`odek-<goos>-<goarch>` asset), verifies the download against the release `checksums.txt` (SHA-256), and installs it atomically over the current binary. `--check` reports the latest version without installing |
| `odek version` / `odek --version` / `odek -v` | Print version and exit |

## Run flags

Unknown flags are a **hard error** — they are never folded into the task text (a typo'd flag must not silently corrupt the prompt, and nothing that controls odek's argv should gain an injection vector). If your task text itself starts with `-`, pass it after an explicit `--` separator: `odek run -- "--dash-prefixed task"`.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--model <name>` | string | `deepseek-v4-flash` | LLM model — profiles auto-set thinking/timeout (see [Providers](PROVIDERS.md)). |
| `--base-url <url>` | string | `https://api.deepseek.com/v1` | OpenAI-compatible API endpoint |
| `--max-iter <n>` | int | `90` | Max think→act cycles |
| `--thinking <level>` | string | profile default | Reasoning depth: `enabled`/`disabled`/`low`/`medium`/`high`. Requires a model that supports extended thinking. |
| `--thinking-budget <n>` | int | `5000` | Max thinking tokens for extended thinking (Anthropic budget_tokens). Only applied when `--thinking` is set. |
| `--temperature <n>` | float | `0` | LLM sampling temperature (0.0–2.0). Forced to 1 when Anthropic extended thinking is active. |
| `--sandbox` | bool | default on | Execute shell commands inside Docker container. Defaults ON when no layer sets it; degrades loudly to unsandboxed when Docker is unavailable (fatal with `ODEK_REQUIRE_SANDBOX=1`). Explicit `--sandbox` keeps the hard-fail behavior. |
| `--no-sandbox` | bool | — | Explicitly disable the sandbox (same as `ODEK_NO_SANDBOX=1`); silences the default-on behavior. |
| `--deliver` | bool | false | Deliver the agent's final response to the configured Telegram `default_chat_id`. Requires `telegram.bot_token` + `telegram.default_chat_id` in config. Handy for host-cron one-shots; for recurring tasks prefer the native scheduler (`odek schedule`, see [Schedules](SCHEDULES.md)). |
| `--interaction-mode <mode>` | string | `engaging` | Tool-call rendering: `engaging` (emoji narration) or `verbose` (raw tool output) |
| `--no-color` | bool | false | Disable colored terminal output |
| `--prompt-caching` | bool | false | Enable Anthropic/OpenAI/DeepSeek prompt caching markers |
| `--stream` | bool | config | Stream reasoning and answer text to the terminal as it arrives. Run/repl have no `--no-stream` inverse — disable via `stream: false` in config or `ODEK_STREAM=false` (`odek serve` does accept `--no-stream`) |
| `--compaction` | bool | `true` | Enable LLM-based rolling compaction of trimmed context. On by default |
| `--no-compaction` | bool | `false` | Disable rolling compaction (overrides config/default) |
| `--planning` | bool | `true` | Enable the plan tool and protected plan message. On by default; accepted by `run`, `repl`, and `serve` |
| `--no-planning` | bool | `false` | Disable planning (overrides config/default) |
| `--memory-extended-enabled` | bool | config | Turn on Extended Memory for this run (see [EXTENDED_MEMORY.md](EXTENDED_MEMORY.md)) |
| `--memory-extended-max-size-mb <n>` | int | config | Store size-cap override |
| `--memory-extended-atom-max-chars <n>` | int | config | Per-atom size-cap override |
| `--memory-extended-memory-budget-chars <n>` | int | config | Prompt-injection budget override |
| `--memory-extended-user-state-turn-interval <n>` / `--memory-extended-user-state-max-pending <n>` | int | config | User-state inference cadence and pending-queue cap overrides |
| `--memory-extended-associations-enabled` / `-disabled` | bool | config | Toggle atom associations; `--memory-extended-association-semantic-top-k <n>` caps semantic neighbors per atom |
| `--memory-extended-proactive-return-after-break` / `-no-proactive-return-after-break` | bool | config | Toggle the return-after-break summary |
| `--memory-extended-style-mirroring-enabled` / `-disabled` | bool | config | Toggle user-style mirroring |
| `--memory-extended-anaphora-resolution-enabled` / `-disabled` | bool | config | Toggle anaphora resolution |
| `--memory-extended-follow-up-anticipation-enabled` / `-disabled` | bool | config | Toggle follow-up anticipation |
| `--no-agents` | bool | false | Skip loading AGENTS.md |
| `--session` | bool | false | Save conversation as a multi-turn session |
| `--events-jsonl <path>` | string | — | Append the structured runtime event stream (schema `odek.event/v1`, one JSON object per line). File is created/hardened `0600`; the parent directory must already exist; a symlink at the target path is refused. See [Extensions](EXTENSIONS.md) |
| `--events-include-args` | bool | false | With `--events-jsonl`: include raw (secret-redacted) tool-call arguments in `tool_call_started` events, for incident review. Without it the stream carries only digests plus the structured `args_summary` (program name, target, class). |
| `--external-ref <ref>` | string | — | Attach an external-state reference to the session (repeatable; also on `odek continue`). Forms: `kind=...,uri=...,created_by=...[,read_only=...]` or shorthand `kind=uri` (`created_by` defaults to `cli`). odek stores refs verbatim and never dereferences them. Persisted only with `--session` (a warning is printed otherwise). See [Sessions](SESSIONS.md#external-state-references) |
| `--max-runtime <sec>` | int | — | Hard execution budget: max wall-clock seconds per run |
| `--max-tool-calls <n>` | int | — | Hard execution budget: max total tool calls |
| `--max-input-tokens <n>` | int | — | Hard execution budget: max cumulative input tokens |
| `--max-output-tokens <n>` | int | — | Hard execution budget: max cumulative output tokens |
| `--max-cost-usd <n>` | float | — | Hard execution budget: max estimated cost in USD (requires configured per-million prices — see [CONFIG.md → limits](CONFIG.md#execution-budgets-limits)). Budget exhaustion exits with code 4 |
| `--tool <name>` | string | — | Enable a specific tool for the LLM (repeatable). Highest-priority layer for the tool whitelist. |
| `--no-tool <name>` | string | — | Disable a specific tool for the LLM (repeatable). Merges with lower-priority disabled lists. |
| `--system <prompt>` | string | built-in | Override system prompt |
| `--ctx <files>` / `-c` | string | — | Attach comma-separated files as context blocks |
| `--guard-provider <local|piguard>` | string | `local` | Prompt-injection guard provider |
| `--guard-url <url>` | string | — | Guard sidecar single-text endpoint |
| `--guard-batch-url <url>` | string | — | Guard sidecar batch endpoint |
| `--guard-long-url <url>` | string | — | Guard sidecar long-text endpoint |
| `--guard-socket-path <path>` | string | — | Guard sidecar Unix socket path |
| `--guard-threshold <score>` | float | `0.9` | Injection score threshold |
| `--guard-timeout <seconds>` | int | `5` | Guard sidecar request timeout |
| `--guard-fallback` / `--guard-no-fallback` | bool | `true` | Fall back to local scan if sidecar fails |
| `--guard-scan-memory` / `--guard-no-scan-memory` | bool | `true` | Guard legacy/Extended Memory surfaces |
| `--guard-scan-system-prompt` / `--guard-no-scan-system-prompt` | bool | `true` | Guard system-prompt sources |
| `--guard-scan-mcp` / `--guard-no-scan-mcp` | bool | `true` | Guard MCP tool descriptions |
| `--guard-scan-skills` / `--guard-no-scan-skills` | bool | `false` | Guard skill bodies (load time and import) |
| `--guard-scan-tool-outputs` / `--guard-no-scan-tool-outputs` | bool | `false` | Guard external tool outputs (warning-only) |
| `--guard-scan-telegram` / `--guard-no-scan-telegram` | bool | `false` | Guard Telegram captions/transcripts |

## Execution budgets

`odek run` supports hard execution budgets (odek-extension/v1): wall-clock runtime, total tool calls, cumulative input/output tokens, and estimated cost. Sources: the `limits` section of `~/.odek/config.json` (a project `./odek.json` may only *lower* them — see [CONFIG.md → limits](CONFIG.md#execution-budgets-limits)) and the five `--max-*` flags above. There is no `ODEK_*` env-var layer for limits.

Enforcement points: runtime is checked before every LLM call, token totals and estimated cost after every LLM response, and the tool-call count before each tool batch is scheduled.

On exhaustion odek:

1. emits a `budget_exceeded` runtime event (`limit_name`, `observed`, `limit`) — visible via `--events-jsonl`,
2. persists the latest safe session state (when `--session` is active),
3. returns a typed budget error naming the limit, observed value, and maximum, and exits with code **4**.

A partial-progress summary is produced only when the tool-call budget fired and the other budgets still have headroom. The marker names which budget fired: `[Execution budget reached — partial summary]` (tool-call/token/cost budget), `[Time budget reached — partial summary]` (wall-clock runtime), and `[Iteration budget reached — partial summary]` (iteration cap exhausted — the engine makes one final tool-less call for the summary instead of discarding progress).

Cost enforcement is active only when `max_cost_usd` **and** both per-million prices (`input_cost_per_million_usd`, `output_cost_per_million_usd`) are configured; otherwise a stderr warning is printed and the token budgets stay active. odek never hard-codes provider prices.

**Current limitation:** budgets apply to `odek run` only — not `continue`, the REPL, `serve`, or Telegram.

## Exit codes

| Code | Meaning | Commands |
|------|---------|----------|
| `0` | Success | all |
| `1` | Task/model/tool error | all |
| `2` | Overall timeout (killed by parent/context, or the wall-clock budget fired and the sub-agent concluded with a `partial` time-budget report) | `subagent` |
| `3` | Setup/contract error | `subagent` |
| `4` | Execution budget exhausted (typed `budget.Error`) | `run`, `subagent` |

## File attachments

Attach files to any task prompt to provide the agent with context — no tool calls needed. File content is injected as **context blocks** before the prompt.

### `--ctx` / `-c` (CLI)

```bash
# Single file
odek run --ctx data.csv "analyze this"

# Multiple files (comma-separated)
odek run --ctx main.go,lib.go "compare these files"

# Short flag
odek run -c config.json "validate"

# With session persistence
odek run --session --ctx schema.sql "design the migration"
```

### `@file` inline references

Reference files directly in the task text — works in `odek run`, `odek continue`, REPL, and Web UI:

```bash
odek run "@README.md summarize this project"
odek run --session "@auth.go review the security"
odek continue "@auth.go now add rate limiting" --id 20260518-abc123
```

When `@ref` resolution fails (file not found), the reference is left as-is in the prompt.

### Web UI

In `odek serve`:
- **Paperclip button** next to the input to pick files
- **Drag-and-drop** files onto the chat area
- **Attached file chips** show filename, size, and a remove button
- **`@` autocomplete** dropdown for files and sessions
- 5 MB per file, 10 MB total per prompt

### Implementation

Files are read client-side in the Web UI and server-side in the CLI through `enrichTask()` in the `cmd/odek/refs.go` package. The `resource` package handles resolution: file content is wrapped in `--- filename ---` / `--- end filename ---` markers and prepended to the task.

### Sandbox file injection

When `--sandbox` is active, `--ctx` files are automatically **copied into the sandbox container** via `docker cp`, placed at `/workspace/<relative-path>`. Files outside the working directory use their basename. This means the agent can use tools (`read_file`, `shell cat`, `patch`) on the same files it sees in context — no "content visible but file doesn't exist" gap. Directories and missing files are silently skipped.

## Shell tool schema

```json
{
  "command": "rm -rf /var/log",
  "description": "Clear stale nginx logs before restarting"
}
```

## `delegate_tasks` tool schema

Spawn focused sub-agents. Each task carries parent-side trust signals:

```json
{
  "tasks": [
    {
      "goal": "Find and fix the failing test",
      "context": "The test in internal/foo/bar_test.go started failing after commit abc123.",
      "guidance": "Use grep and go test only; do not edit files outside internal/foo.",
      "trust_level": "untrusted",
      "max_risk": "local_write"
    }
  ]
}
```

- `trust_level`: `"untrusted"` (default when omitted) or `"trusted"`. **Every** sub-agent runs non-interactive (`non_interactive: deny` is forced — trusted ones never prompt either). Untrusted tasks additionally deny `destructive`, `code_execution`, `install`, `system_write`, `persistence`, `unread_exec`, `network_egress`, `unknown`, and `blocked`.
- `max_risk`: highest risk class the sub-agent may execute. Anything ranked above it is forced to `deny`.
- **Trust is non-increasing downward**: the delegate tool stamps the parent's own effective trust into the task (`parent_trust`), and the child runs at `min(parent_trust, trust_level)`. A task tree rooted in untrusted content cannot spawn trusted children.
- **Sub-agents never prompt for approvals.** Every sub-agent runs non-interactive — prompt-class operations are denied even for trusted sub-agents; the operator `allowlist` (exact pre-approved invocations) is the only path to prompt-class operations. Denied operations are reported in the result's `denials` array (`{tool, class, reason}`, capped at 20 with `denials_total` carrying the full count) and surfaced as `subagent_denied` runtime events, so the parent can adapt or escalate instead of failing blind.

MCP servers are not loaded into untrusted sub-agents, because MCP tool adapters do not perform their own danger classification.

## Dangerous operations

When running without `--sandbox`, odek classifies every shell command by risk and prompts for high-risk operations:

| Class | Default | Examples |
|-------|---------|----------|
| 🟢 safe | allow | `ls`, `cat`, `grep`, `go build` |
| 🟡 local_write | allow | `rm file`, `mv`, `echo > file` |
| 🟠 system_write | **prompt** | `sudo`, `apt install`, writes to `/etc/`, `chmod -R 777 /`, `git reset --hard`, `git clean -fdx` |
| 🔴 destructive | **deny** | `rm -rf /`, `dd if=/dev/zero`, `mkfs` |
| 🔴 network_egress | **prompt** | `curl`, `git push`, `ssh`, `scp` |
| 🔴 code_execution | **prompt** | `curl url \| bash`, `eval`, `node -e`, `go run` |
| 🟠 install | **prompt** | `npm install`, `pip install`, `go install <path>` |
| 🔴 unknown | **deny** | any command whose program name isn't recognised; MCP tools (`<server>__<tool>`); pipe-fed `xargs <verb>` whose stdin payload isn't statically determinable |
| ⬛ blocked | **deny** | Fork bombs, `dd` to block devices |

odek **fails closed**: a command or MCP tool whose name matches no known-safe or known-dangerous
pattern is classified `unknown` and denied by default. Permit a specific tool by adding
its exact invocation to `allowlist`, or soften the class with `"unknown": "prompt"`.

The approval prompt accepts:

- `A` — Approve once
- `D` — Deny (returns error to agent)
- `T` — Trust all commands of this class for this session
- `?` — Show full context

Configurable via `dangerous` section in `~/.odek/config.json` or `./odek.json`:

```json
{
  "dangerous": {
    "action": "prompt",
    "non_interactive": "deny",
    "classes": {
      "destructive": "prompt",
      "network_egress": "allow"
    },
    "allowlist": ["git push origin main"],
    "denylist": ["rm -rf /"]
  }
}
```

Only `"allow"` and `"deny"` are valid `non_interactive` values; anything else (including the previously accepted `"prompt"`) is rejected at load time with a warning and treated as `"deny"`, because a non-interactive environment cannot prompt.

See [docs/SECURITY.md](SECURITY.md) for details.

## Skills

The **skills system** provides just-in-time domain knowledge to the agent. Skills are SKILL.md files
with YAML frontmatter that define trigger keywords, quality metadata, and markdown body content.

### How skills work

1. Skills are stored in `~/.odek/skills/<name>/SKILL.md` (user-global) or `./.odek/skills/<name>/SKILL.md` (project)
2. Skills with `auto_load: true` are injected into the system prompt on start
3. Lazy skills are loaded on demand when the user's input matches their trigger keywords (topic × action)

### Skill commands

```bash
# List all skills
odek skill list

# View a skill's full content
odek skill view docker-build

# Delete a skill
odek skill delete docker-build

# Promote a tainted skill so it can trigger-load.
# Skills derived from sessions that ingested untrusted content
# (browser fetch, file outside CWD, MCP response, audio) are
# saved with NeedsReview=true and pinned to the Lazy set.
# Review the body first, then promote.
odek skill view my-skill
odek skill promote my-skill

# Import a skill from a file or URL
odek skill import ./skills/my-skill.md
odek skill import https://example.com/skills/deploy.md

# Import with flags
odek skill import https://example.com/skills/deploy.md --basic   # skip LLM risk assessment
odek skill import https://example.com/skills/deploy.md --yes     # auto-approve (scripting)
```

### Skill file format

```yaml
---
name: docker-build
description: Build and optimize Docker images
version: 1.0.0
author: odek
odek:
  trigger:
    topic: docker container image
    action: build optimize
  auto_load: false
  quality: verified
---
## Overview

Procedure for building optimized Docker images.

## Step-by-Step

1. Write a `.dockerignore` file
2. Use multi-stage builds
3. Run `docker build -t <name> .`

## Common Pitfalls

- Forgetting `.dockerignore` leads to large build contexts
- Not pinning base image versions causes build drift

## Verification

- `docker build` exits with code 0
- `docker images` shows the new image
```

## Sandbox flags

| Flag | Default | Description |
|------|---------|-------------|
| `--sandbox-image <img>` | `alpine:latest` | Docker image |
| `--sandbox-network <mode>` | `none` | Network: `none`/`bridge`. `host` rejected. |
| `--sandbox-readonly` | false | Mount working directory read-only |
| `--sandbox-memory <s>` | — | Memory limit (e.g. `512m`, `2g`) |
| `--sandbox-cpus <n>` | — | CPU limit (e.g. `0.5`, `2`) |
| `--sandbox-user <s>` | — | Run as user (`uid:gid`) |
| `--no-sandbox` | — | (serve only) Disable the default-on sandbox. Prints a warning. |

`odek serve` enables `--sandbox` by default. `odek run` and `odek repl` keep sandbox opt-in but print a startup warning when running unsandboxed. Set `ODEK_SUPPRESS_SANDBOX_WARNING=1` to silence the warning if you've made an informed decision.

**Project-level sandbox approval:** if `./odek.json` sets `sandbox_env`, `sandbox_image`, `sandbox_network`, or `sandbox_volumes`, odek prompts for approval before applying them. In CI or scripted invocations, set `ODEK_APPROVE_PROJECT_SANDBOX=1` to auto-approve, or place sandbox config in `~/.odek/config.json` / `ODEK_*` env vars / CLI flags instead, which do not require approval.

## Audit log

`odek audit` reads the per-session prompt-injection audit log written under `<sessions>/audit/<id>.json`. Every time the agent ingests externally-sourced content (browser fetch, file read, MCP tool response, audio transcript) the log records:

- the source (URL / path / `mcp:<server>:<tool>`)
- a 16-hex SHA-256 prefix of the content
- the turn it landed on

After each turn, odek runs a divergence heuristic and sets `suspicious_divergence=true` when the agent ingested untrusted content **and** its actions or final response reference resources that either (a) did not appear in the user's preceding message, or (b) were introduced by the untrusted content itself. This catches classic prompt injection, response-only exfiltration, and reused-resource injection.

```bash
odek audit --list
# Session                Ingests  Turns  Suspicious  First-Ingest-Source
# 20260527-a1b2c3            12      4           1   https://example.com/blog
# 20260527-d4e5f6             3      2           0   /tmp/spec.md

odek audit 20260527-a1b2c3
# JSON: { "session_id": "...", "ingests": [...], "turns": [...] }

odek audit 20260527-a1b2c3 | jq '.turns[] | select(.suspicious_divergence)'
```

The audit log is local-only — nothing in odek transmits it.

See [SECURITY.md](SECURITY.md) for the full threat model.

## Init flags

| Flag | Description |
|------|-------------|
| `--global`, `-g` | Create global config at `~/.odek/config.json` |
| `--force`, `-f` | Overwrite existing file without prompting |

## Background commands

Headless `odek run` is one run, one session, one process: background jobs
started with `bg_start` are killed when the run ends (including budget
exhaustion). Cross-turn scenarios — start a dev server in one turn, curl it
in the next — belong on the interactive surfaces (repl, serve, telegram),
where jobs are scoped to the session and killed when the session or the
process ends. `--events-jsonl` includes `bg_started`/`bg_exited` events
(command content is hashed, never included). `/jobs` lists live jobs in the
REPL.

## Examples

```bash
# Quick task (single-shot, no session saved)
odek run "How many Go files in this project?"

# Save as session for follow-up
odek run --session "Refactor the auth module"

# Continue a session
odek continue "Now add error handling to the refactored auth"

# Continue a specific session by ID
odek continue --id 20260518-abc123 "Add unit tests"

# List all sessions
odek session list

# Show latest session transcript
odek session show

# Show a specific session
odek session show 20260518-abc123

# Trim session to last 10 messages (preserves system prompt)
odek session trim 20260518-abc123 10

# Delete sessions older than 30 days
odek session cleanup 30

# Wipe all sessions
odek session cleanup 0

# Sweep all expired storage (sessions, audit, plans, log rotation)
odek cleanup

# Preview the sweep without deleting anything
odek cleanup --dry-run

# OpenAI
odek run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this code"

# Sandboxed execution
odek run --sandbox "npm test"

# Custom sandbox image
odek run --sandbox --sandbox-image node:20-alpine "node --version"

# Interactive REPL with sandbox
odek repl --sandbox --model deepseek-v4-pro

# Sandbox REPL with custom image and no network
odek repl --sandbox --sandbox-image node:20-alpine --sandbox-network none

# Resume a sandboxed session in REPL mode
odek repl --id 20260518-abc123

# Custom system prompt
odek run --system "You are a Go expert. Answer with code only." "Write HTTP server"

# Simple one-shot run
odek run "Set up CI with GitHub Actions"

# File attachments
odek run --ctx go.mod "check go version"
odek run -c main.go,util.go "refactor both files"
odek run "&#64;schema.sql design a migration plan"

# Bounded run with event stream + external ref (CI orchestration)
odek run --session --events-jsonl /tmp/odek-events.jsonl \
  --max-runtime 300 --max-tool-calls 50 --max-cost-usd 0.25 \
  --external-ref ci-run=https://ci.example.test/runs/4821 \
  "Summarize the failing tests in this repo"

# Cron integration: deliver agent result to Telegram
odek run --deliver "Daily weather forecast for Berlin"
odek run --deliver "Check the CI pipeline status"

# Systemd cron example (crontab -e):
# */5 * * * * /usr/local/bin/odek run "Say hello" --deliver >> /tmp/odek-cron.log 2>&1
```

## Config priority

Config sources from lowest to highest priority:

```
1.  ~/.odek/config.json    ← Global defaults
2.  ./odek.json           ← Project overrides
3.  ODEK_* env vars       ← Runtime overrides
4.  CLI flags             ← Explicit invocation (highest)
```

See [Configuration](CONFIG.md) for details.
