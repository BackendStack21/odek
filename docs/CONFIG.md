# Configuration

`odek uses a **layered configuration system** with convention over configuration — opt-in files and environment variables, no mandatory setup.

## Priority chain

Each layer overrides the one below it. Unset fields inherit from the layer below:

```
1.  ~/.odek/config.json    ← Global defaults (shared across projects)
2.  ./odek.json           ← Project-specific overrides
3.  ODEK_* env vars       ← Runtime/environment overrides
4.  CLI flags             ← Explicit invocation (highest priority)
```

## Config files

### Global defaults (`~/.odek/config.json`)

Shared across all projects:

```json
{
  "model": "deepseek-v4-flash",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "${ODEK_API_KEY}",
  "thinking": "",
  "max_iterations": 90,
  "sandbox": false,
  "no_color": false,
  "no_agents": false,
  "system": ""
}
```

### Project overrides (`./odek.json`)

Same schema as global. Only set the fields you want to override:

```json
{
  "model": "gpt-4o",
  "base_url": "https://api.openai.com/v1",
  "max_iterations": 30
}
```

Both files are optional. Missing files are silently ignored. String values support `${VAR}` environment variable substitution — useful for API keys without plaintext storage.

## Environment variables

Every config knob has a `ODEK_*` counterpart:

| Variable | Maps to | Type |
|----------|---------|------|
| `ODEK_MODEL` | `--model` | string |
| `ODEK_BASE_URL` | `--base-url` | string |
| `ODEK_API_KEY` | config files only | string |
| `ODEK_THINKING` | `--thinking` | string |
| `ODEK_MAX_ITER` | `--max-iter` | int |
| `ODEK_SANDBOX` | `--sandbox` | bool |
| `ODEK_NO_COLOR` | `--no-color` | bool |
| `ODEK_NO_AGENTS` | `--no-agents` | bool |
| `ODEK_SYSTEM` | `--system` | string |
| `ODEK_SKILLS_LEARN` | `skills.learn` | bool |
| `ODEK_SANDBOX_IMAGE` | `--sandbox-image` | string |
| `ODEK_SANDBOX_NETWORK` | `--sandbox-network` | string |
| `ODEK_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
| `ODEK_SANDBOX_MEMORY` | `--sandbox-memory` | string |
| `ODEK_SANDBOX_CPUS` | `--sandbox-cpus` | string |
| `ODEK_SANDBOX_USER` | `--sandbox-user` | string |

## API key fallback order

`ODEK_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`

## Skills configuration

The `skills` section controls the skill system:

```json
{
  "skills": {
    "max_auto_load": 3,
    "max_lazy_slots": 5,
    "learn": true,
    "llm_learn": true,
    "llm_curate": true,
    "import": {
      "max_size_bytes": 1048576,
      "timeout_seconds": 5,
      "require_https": false
    },
    "curation": {
      "staleness_days": 90,
      "auto_prune": false,
      "auto_curate": true,
      "skip_threshold": 1,
      "skip_reset_days": 30
    },
    "auto_save": {
      "enabled": true,
      "require_llm": true,
      "max_per_run": 3
    }
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `max_auto_load` | — | 3 | Max skills injected into system prompt on start |
| `max_lazy_slots` | — | 5 | Max skills loaded per user input via trigger matching |
| `learn` | `ODEK_SKILLS_LEARN` | `true` | Enable skill learning mode (detects patterns, suggests skills). On by default |
| `llm_learn` | — | `true` | Use LLM to enrich detected patterns. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `llm_curate` | — | `true` | Use LLM for curation quality assessment. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `dirs` | — | [] | Extra skill directories beyond `~/.odek/skills` and `./.odek/skills` |
| `import.max_size_bytes` | — | 1048576 (1MB) | Max size for fetched skill content |
| `import.timeout_seconds` | — | 5 | HTTP timeout for skill URI fetch |
| `import.require_https` | — | false | Reject http:// URIs when true |
| `curation.staleness_days` | — | 90 | Days without use before flagging as stale |
| `curation.auto_prune` | — | false | Auto-delete stale skills on curate (no prompt) |
| `curation.auto_curate` | — | true | Run auto-curation after sessions (merge, dedup, prune) |
| `curation.skip_threshold` | — | 1 | Times a skill must be skipped before permanent suppression |
| `curation.skip_reset_days` | — | 30 | Days after which a skip expires (re-allows suggestion) |
| `auto_save.enabled` | — | true | Auto-save quality skill suggestions without prompting |
| `auto_save.require_llm` | — | true | Only auto-save if LLM enhancement was applied |
| `auto_save.max_per_run` | — | 3 | Max skills to auto-save per session |

## Memory configuration

The `memory` section controls the persistent memory system (see [docs/MEMORY.md](docs/MEMORY.md)):

```json
{
  "memory": {
    "enabled": true,
    "facts_limit_user": 1500,
    "facts_limit_env": 2500,
    "buffer_lines": 20,
    "buffer_enabled": true,
    "merge_on_write": true,
    "extract_on_end": true,
    "llm_search": true,
    "llm_extract": true,
    "llm_consolidate": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | true | Enable memory system entirely |
| `facts_limit_user` | 1500 | Max chars for `user.md` fact file |
| `facts_limit_env` | 2500 | Max chars for `env.md` fact file |
| `buffer_lines` | 20 | Max turn summaries in session buffer |
| `buffer_enabled` | true | Enable the turn-level buffer |
| `merge_on_write` | true | Use go-vector RP similarity to auto-merge related entries |
| `extract_on_end` | true | Extract durable facts via LLM at session end (≥3 turns) |
| `llm_search` | true | Use LLM to rank episode search results by relevance |
| `llm_extract` | true | Use LLM for end-of-session fact extraction |
| `llm_consolidate` | true | Use LLM to merge related fact entries |
| `merge_threshold` | 0.7 | go-vector cosine threshold for auto-merge (0.0–1.0) |
| `add_threshold` | 0.3 | go-vector cosine threshold for auto-add (0.0–1.0) |

## Sub-agent configuration

The `subagent` section controls task decomposition and parallel sub-agent execution (see [docs/SUBAGENTS.md](docs/SUBAGENTS.md)):

```json
{
  "subagent": {
    "max_concurrency": 3,
    "timeout_seconds": 120,
    "max_iterations": 15
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `max_concurrency` | 3 | Max sub-agents running in parallel (max 8) |
| `timeout_seconds` | 120 | Default timeout per sub-agent (overridden by `--timeout`) |
| `max_iterations` | 15 | Default max think→act cycles per sub-agent (overridden by `--max-iter`) |

This section is optional. Omitted fields inherit sensible defaults.

> **Note**: The `subagent` section is currently read only from `odek.json` by the `odek subagent` command in test code. Runtime values (`max_concurrency`, `timeout_seconds`) are hardcoded in production `odek run`/`odek serve`. This may be wired up fully in a future release.

## MCP server configuration

Connect to **external MCP servers** and expose their tools to the agent.
Any MCP server that works with Claude Code works with odek — same config format.

```json
{
  "mcp_servers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp"]
    },
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `command` | The executable to run |
| `args` | Optional command-line arguments |
| `env` | Optional environment variable overrides (empty string removes from env) |

Tools are registered as `<server_name>__<tool_name>` (e.g., `playwright__navigate`)
and are available in `odek run`, `odek repl`, `odek continue`, and `odek serve`.

See [docs/MCP.md](docs/MCP.md#odek-as-mcp-client) for detailed instructions.

## odek init

Create a config file template:

```bash
# Local project config (./odek.json)
odek init

# Global config (~/.odek/config.json)
odek init --global

# Overwrite existing file
odek init --force
```

## Quick examples

```bash
# Set API key via environment variable (recommended — keeps secrets out of config files)
export ODEK_API_KEY="sk-..."

# Global config (model and other settings only, no secrets)
echo '{"model": "deepseek-v4-flash"}' > ~/.odek/config.json
odek run "list files"

# Per-project override
echo '{"max_iterations": 30}' > ./odek.json
odek run "quick status"

# Env var override for one-off
ODEK_SANDBOX=true odek run "run untrusted script"

# Enable skill learning via env var
ODEK_SKILLS_LEARN=true odek run "set up CI"

# Sub-agent config (project-level)
echo '{"subagent": {"max_concurrency": 5, "timeout_seconds": 300}}' > ./odek.json

# CLI flag always wins
odek run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```
