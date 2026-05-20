# CLI Reference

## Commands

| Command | Description |
|---------|-------------|
| `odek run [flags] <task>` | Execute a task with the agent loop (single-shot by default) |
| `odek run --session [flags] <task>` | Execute and save conversation as a multi-turn session |
| `odek run [--no-learn] [flags] <task>` | Execute with skill learning (on by default, use --no-learn to disable) |
| `odek continue [--id <id>] <task>` | Continue the most recent session (or by `--id`) |
| `odek repl [flags]` | Interactive REPL mode (persistent multi-turn session). Accepts `--model`, `--thinking`, `--sandbox`, and `--sandbox-*` flags. |
| `odek session list` | List sessions |
| `odek session show [id]` | Show session details (default: latest) |
| `odek session delete <id>` | Delete a session |
| `odek session trim <id> <n>` | Keep only the `n` most recent messages |
| `odek session cleanup <days>` | Delete sessions older than N days |
| `odek skill list` | List all available skills |
| `odek skill view <name>` | View a skill's full content |
| `odek skill delete <name>` | Delete a skill |
| `odek skill import <uri> [flags]` | Import a skill from file:// or https:// |
|| `odek skill curate` | Analyze skills for quality, staleness, trigger overlap |
|| `odek serve [--addr :8080] [--open]` | Web UI server with WebSocket streaming, `@` resource completion, session history |
|| `odek subagent --goal <string> [flags]` | Run a focused sub-task; outputs JSON on stdout. Spawned by `delegate_tasks` tool |
| `odek init [--global] [--force]` | Create a config file template |
| `odek mcp [--sandbox]` | Start MCP server (expose tools to Claude Code) or connect to external MCP servers (via `mcp_servers` config) |
| `odek version` | Print version and exit |

## Run flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--model <name>` | string | `deepseek-chat` | LLM model — profiles auto-set thinking/timeout (see [Providers](docs/PROVIDERS.md)) |
| `--base-url <url>` | string | `https://api.deepseek.com/v1` | OpenAI-compatible API endpoint |
| `--max-iter <n>` | int | `90` | Max think→act cycles |
| `--thinking <level>` | string | profile default | Reasoning depth: `enabled`/`disabled`/`low`/`medium`/`high` |
| `--sandbox` | bool | false | Execute shell commands inside Docker container |
| `--no-color` | bool | false | Disable colored terminal output |
| `--no-agents` | bool | false | Skip loading AGENTS.md |
| `--session` | bool | false | Save conversation as a multi-turn session |
| `--learn` | bool | `true` | Enable skill learning mode (detects patterns, saves skills). On by default |
| `--no-learn` | bool | `false` | Disable skill learning mode (overrides config/default) |
| `--system <prompt>` | string | built-in | Override system prompt |

## Shell tool schema

The shell tool accepts an optional `description` field in addition to `command`:

```json
{
  "command": "rm -rf /var/log",
  "description": "Clear stale nginx logs before restarting"
}
```

## Dangerous operations

When running without `--sandbox`, kode classifies every shell command by risk and prompts for high-risk operations:

| Class | Default | Examples |
|-------|---------|----------|
| 🟢 safe | allow | `ls`, `cat`, `grep`, `go build` |
| 🟡 local_write | allow | `rm file`, `mv`, `echo > file` |
| 🟠 system_write | **prompt** | `sudo`, `apt install`, writes to `/etc/` |
| 🔴 destructive | **deny** | `rm -rf /`, `dd if=/dev/zero`, `mkfs` |
| 🔴 network_egress | **prompt** | `curl`, `git push`, `ssh`, `scp` |
| 🔴 code_execution | **prompt** | `curl url \| bash`, `eval`, `node -e`, `go run` |
| 🟠 install | **prompt** | `npm install`, `pip install`, `go install <path>` |
| ⬛ blocked | **deny** | Fork bombs, `dd` to block devices |

The approval prompt accepts:

- `A` — Approve once
- `D` — Deny (returns error to agent)
- `T` — Trust all commands of this class for this session
- `?` — Show full context

Configurable via `dangerous` section in `~/kode/config.json` or `./odek.json`:

```json
{
  "dangerous": {
    "action": "prompt",
    "non_interactive": "allow",
    "classes": {
      "destructive": "prompt",
      "network_egress": "allow"
    },
    "allowlist": ["git push origin main"],
    "denylist": ["rm -rf /"]
  }
}
```

See [docs/SECURITY.md](docs/SECURITY.md) for details.

## Skills

The **skills system** provides just-in-time domain knowledge to the agent. Skills are SKILL.md files
with YAML frontmatter that define trigger keywords, quality metadata, and markdown body content.

### How skills work

1. Skills are stored in `~/.kode/skills/<name>/SKILL.md` (user-global) or `./.kode/skills/<name>/SKILL.md` (project)
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

# Import a skill from a file or URL
odek skill import ./skills/my-skill.md
odek skill import https://example.com/skills/deploy.md

# Import with flags
odek skill import https://example.com/skills/deploy.md --basic   # skip LLM risk assessment
odek skill import https://example.com/skills/deploy.md --yes     # auto-approve (scripting)

# Run curation (quality, staleness, overlap checks)
odek skill curate
```

### Skill file format

```yaml
---
name: docker-build
description: Build and optimize Docker images
version: 1.0.0
author: kode
kode:
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

The `kode skill curate` command runs four quality passes:

- **Staleness** — flags skills unused for 90+ days (configurable via `skill.curation.staleness_days`)
- **Trigger overlap** — detects skills with 2+ shared topic keywords that may need merging
- **Quality audit** — checks for missing sections, short bodies, long descriptions
- **Body dedup** — detects skills with identical body content by SHA256 hash

## Sandbox flags

| Flag | Default | Description |
|------|---------|-------------|
| `--sandbox-image <img>` | `alpine:latest` | Docker image |
| `--sandbox-network <mode>` | `bridge` | Network: `bridge`/`none`/`host` |
| `--sandbox-readonly` | false | Mount working directory read-only |
| `--sandbox-memory <s>` | — | Memory limit (e.g. `512m`, `2g`) |
| `--sandbox-cpus <n>` | — | CPU limit (e.g. `0.5`, `2`) |
| `--sandbox-user <s>` | — | Run as user (`uid:gid`) |

## Init flags

| Flag | Description |
|------|-------------|
| `--global`, `-g` | Create global config at `~/kode/config.json` |
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
```

## Config priority

Config sources from lowest to highest priority:

```
1.  ~/kode/config.json    ← Global defaults
2.  ./odek.json           ← Project overrides
3.  KODE_* env vars       ← Runtime overrides
4.  CLI flags             ← Explicit invocation (highest)
```

See [Configuration](CONFIG.md) for details.
