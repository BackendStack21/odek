# CLI Reference

## Commands

| Command | Description |
|---------|-------------|
| `odek run [flags] <task>` | Execute a task with the agent loop (single-shot by default) |
| `odek run --session [flags] <task>` | Execute and save conversation as a multi-turn session |
| `odek run [--no-learn] [flags] <task>` | Execute with skill learning (on by default, use --no-learn to disable) |
| `odek continue [--id <id>] <task>` | Continue the most recent session (or by `--id`) |
| `odek repl [flags]` | Interactive REPL mode (persistent multi-turn session). Accepts `--model`, `--thinking`, `--sandbox`, `--sandbox-*`, `--tool`, and `--no-tool` flags. |
| `odek session list` | List sessions |
| `odek session show [id]` | Show session details (default: latest) |
| `odek session delete <id>` | Delete a session |
| `odek session trim <id> <n>` | Keep only the `n` most recent messages |
| `odek session cleanup <days>` | Delete sessions older than N days |
| `odek skill list` | List all available skills |
| `odek skill view <name>` | View a skill's full content |
| `odek skill delete <name>` | Delete a skill |
| `odek skill promote <name>` | Clear `NeedsReview` on a tainted auto-saved skill so it can auto-load |
| `odek skill import <uri> [flags]` | Import a skill from file:// or https:// |
|| `odek skill curate` | Analyze skills for quality, staleness, trigger overlap |
|| `odek skill curate --apply` | Apply all curation suggestions (merge, delete, prune) |
|| `odek skill curate --interactive` | Review each suggestion one-by-one |
|| `odek skill reset-skips [name]` | Reset skip list (all or specific skill) |
| `odek audit <session-id>` | Print the prompt-injection audit log for a session (JSON) |
| `odek audit --list` | List sessions with non-zero ingest counts and divergence flags |
|| `odek serve [--addr :8080] [--open] [--no-sandbox] [--trusted-proxies <ips/cidrs>]` | Web UI server. Sandbox is on by default; pass `--no-sandbox` to disable. Accepts `--tool` and `--no-tool` flags. Binding to a non-loopback address prints a loud warning because anyone with the token can drive the agent. `--trusted-proxies` honours `X-Forwarded-For`/`X-Real-Ip` only from those addresses. |
|| `odek subagent --goal <string> [flags]` | Run a focused sub-task; outputs JSON on stdout. Spawned by `delegate_tasks` tool. Flags: `--goal`, `--task <file>`, `--context`, `--timeout` (≤3600s), `--max-iter` (≤100), `--quiet`, `--stream`. |
| `odek init [--global] [--force]` | Create a config file template |
| `odek mcp [--sandbox]` | Start MCP server (expose tools to Claude Code) or connect to external MCP servers (via `mcp_servers` config) |
| `odek telegram` | Start the Telegram bot (long-polling). Hosts the embedded scheduler unless `schedules.enabled=false` |
| `odek schedule <subcommand>` | Manage native in-process scheduled tasks (cron): `list`, `add`, `rm`, `enable`, `disable`, `run`, `next`, `daemon`. See [Schedules](SCHEDULES.md) |
| `odek version` | Print version and exit |

## Run flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--model <name>` | string | `deepseek-chat` | LLM model — profiles auto-set thinking/timeout (see [Providers](docs/PROVIDERS.md)). Consider using `deepseek-v4-flash` for faster/cheaper tasks. |
| `--base-url <url>` | string | `https://api.deepseek.com/v1` | OpenAI-compatible API endpoint |
| `--max-iter <n>` | int | `90` | Max think→act cycles |
| `--thinking <level>` | string | profile default | Reasoning depth: `enabled`/`disabled`/`low`/`medium`/`high`. Requires a model that supports extended thinking. |
| `--thinking-budget <n>` | int | `5000` | Max thinking tokens for extended thinking (Anthropic budget_tokens). Only applied when `--thinking` is set. |
| `--sandbox` | bool | false | Execute shell commands inside Docker container |
| `--deliver` | bool | false | Deliver the agent's final response to the configured Telegram `default_chat_id`. Requires `telegram.bot_token` + `telegram.default_chat_id` in config. Handy for host-cron one-shots; for recurring tasks prefer the native scheduler (`odek schedule`, see [Schedules](SCHEDULES.md)). |
| `--interaction-mode <mode>` | string | `engaging` | Tool-call rendering: `engaging` (emoji narration) or `verbose` (raw tool output) |
| `--no-color` | bool | false | Disable colored terminal output |
| `--prompt-caching` | bool | false | Enable Anthropic/OpenAI/DeepSeek prompt caching markers |
| `--no-agents` | bool | false | Skip loading AGENTS.md |
| `--session` | bool | false | Save conversation as a multi-turn session |
| `--learn` | bool | `true` | Enable skill learning mode (detects patterns, saves skills). On by default |
| `--no-learn` | bool | `false` | Disable skill learning mode (overrides config/default) |
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
| `--guard-scan-skills` / `--guard-no-scan-skills` | bool | `false` | Guard skill bodies and suggestions |
| `--guard-scan-tool-outputs` / `--guard-no-scan-tool-outputs` | bool | `false` | Guard external tool outputs (warning-only) |
| `--guard-scan-telegram` / `--guard-no-scan-telegram` | bool | `false` | Guard Telegram captions/transcripts |

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

- `trust_level`: `"untrusted"` (default when omitted) or `"trusted"`. Untrusted tasks force `non_interactive: deny` and deny `destructive`, `code_execution`, `install`, `system_write`, `network_egress`, `unknown`, and `blocked`.
- `max_risk`: highest risk class the sub-agent may execute. Anything ranked above it is forced to `deny`.

MCP servers are not loaded into untrusted sub-agents, because MCP tool adapters do not perform their own danger classification.

## Dangerous operations

When running without `--sandbox`, odek classifies every shell command by risk and prompts for high-risk operations:

| Class | Default | Examples |
|-------|---------|----------|
| 🟢 safe | allow | `ls`, `cat`, `grep`, `go build` |
| 🟡 local_write | allow | `rm file`, `mv`, `echo > file` |
| 🟠 system_write | **prompt** | `sudo`, `apt install`, writes to `/etc/` |
| 🔴 destructive | **deny** | `rm -rf /`, `dd if=/dev/zero`, `mkfs` |
| 🔴 network_egress | **prompt** | `curl`, `git push`, `ssh`, `scp` |
| 🔴 code_execution | **prompt** | `curl url \| bash`, `eval`, `node -e`, `go run` |
| 🟠 install | **prompt** | `npm install`, `pip install`, `go install <path>` |
| 🔴 unknown | **deny** | any command whose program name isn't recognised; MCP tools (`<server>__<tool>`) |
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

See [docs/SECURITY.md](docs/SECURITY.md) for details.

## Skills

The **skills system** provides just-in-time domain knowledge to the agent. Skills are SKILL.md files
with YAML frontmatter that define trigger keywords, quality metadata, and markdown body content.

### How skills work

1. Skills are stored in `~/.odek/skills/<name>/SKILL.md` (user-global) or `./.odek/skills/<name>/SKILL.md` (project)
2. Skills with `auto_load: true` are injected into the system prompt on start
3. Lazy skills are loaded on demand when the user's input matches their trigger keywords (topic × action)
4. The `--learn` flag detects reusable patterns during a run and prompts to save as a draft skill

### Skill commands

```bash
# List all skills
odek skill list

# View a skill's full content
odek skill view docker-build

# Delete a skill
odek skill delete docker-build

# Promote a tainted auto-saved skill so it can auto-load.
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

# Run curation (quality, staleness, overlap, dedup checks)
odek skill curate

# Apply all curation suggestions automatically
odek skill curate --apply

# Review curation suggestions one-by-one
odek skill curate --interactive

# Reset skip list (re-enable suppressed suggestions)
odek skill reset-skips              # clear all
odek skill reset-skips procedure-grep  # clear specific skill
```

### Skill file format

```yaml
---
name: docker-build
description: Build and optimize Docker images
version: 1.0.0
author: odek
`odek:
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

### Curation

The `odek skill curate` command runs four quality passes:

- **Staleness** — flags skills unused for 90+ days (configurable via `skills.curation.staleness_days`)
- **Trigger overlap** — detects skills with 2+ shared topic keywords that may need merging
- **Quality audit** — checks for missing sections, short bodies, long descriptions
- **Body dedup** — detects skills with identical body content by SHA256 hash

**Auto-curation** runs after every session where skills are auto-saved (`skills.curation.auto_curate: true` by default):

- **Merge** — overlapping draft-quality skills are automatically merged (union keywords, concatenated bodies)
- **Skip deletion** — skills skipped ≥ `skip_threshold` times are auto-deleted
- **Stale pruning** — if `auto_prune: true`, stale skills are deleted automatically

Run `odek skill curate --apply` to manually trigger the full curation pipeline with merge execution.
Use `odek skill reset-skips` to clear the skip list and re-enable suppressed suggestions.

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

# Run with skill learning (on by default — use --no-learn to disable)
odek run "Set up CI with GitHub Actions"

# File attachments
odek run --ctx go.mod "check go version"
odek run -c main.go,util.go "refactor both files"
odek run "&#64;schema.sql design a migration plan"

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
