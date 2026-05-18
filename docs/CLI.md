# CLI Reference

## Commands

| Command | Description |
|---------|-------------|
| `kode run [flags] <task>` | Execute a task with the agent loop (single-shot by default) |
| `kode run --session [flags] <task>` | Execute and save conversation as a multi-turn session |
| `kode continue [--id <id>] <task>` | Continue the most recent session (or by `--id`) |
| `kode repl [--id <id>]` | Interactive REPL mode (persistent multi-turn session) |
| `kode session list` | List sessions |
| `kode session show [id]` | Show session details (default: latest) |
| `kode session delete <id>` | Delete a session |
| `kode session trim <id> <n>` | Keep only the `n` most recent messages |
| `kode session cleanup <days>` | Delete sessions older than N days |
| `kode init [--global] [--force]` | Create a config file template |
| `kode version` | Print version and exit |

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
| `--system <prompt>` | string | built-in | Override system prompt |

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
kode run "How many Go files in this project?"

# Save as session for follow-up
kode run --session "Refactor the auth module"

# Continue a session
kode continue "Now add error handling to the refactored auth"

# Continue a specific session by ID
kode continue --id 20260518-abc123 "Add unit tests"

# List all sessions
kode session list

# Show latest session transcript
kode session show

# Show a specific session
kode session show 20260518-abc123

# Trim session to last 10 messages (preserves system prompt)
kode session trim 20260518-abc123 10

# Delete sessions older than 30 days
kode session cleanup 30

# Wipe all sessions
kode session cleanup 0

# OpenAI
kode run --model gpt-4o --base-url https://api.openai.com/v1 "Explain this code"

# Sandboxed execution
kode run --sandbox "npm test"

# Custom sandbox image
kode run --sandbox --sandbox-image node:20-alpine "node --version"

# Custom system prompt
kode run --system "You are a Go expert. Answer with code only." "Write HTTP server"
```

## Config priority

Config sources from lowest to highest priority:

```
1.  ~/kode/config.json    ← Global defaults
2.  ./kode.json           ← Project overrides
3.  KODE_* env vars       ← Runtime overrides
4.  CLI flags             ← Explicit invocation (highest)
```

See [Configuration](CONFIG.md) for details.
