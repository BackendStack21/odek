# Configuration

kode uses a **layered configuration system** with convention over configuration — opt-in files and environment variables, no mandatory setup.

## Priority chain

Each layer overrides the one below it. Unset fields inherit from the layer below:

```
1.  ~/kode/config.json    ← Global defaults (shared across projects)
2.  ./kode.json           ← Project-specific overrides
3.  KODE_* env vars       ← Runtime/environment overrides
4.  CLI flags             ← Explicit invocation (highest priority)
```

## Config files

### Global defaults (`~/kode/config.json`)

Shared across all projects:

```json
{
  "model": "deepseek-v4-flash",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "${DEEPSEEK_API_KEY}",
  "thinking": "",
  "max_iterations": 90,
  "sandbox": false,
  "no_color": false,
  "no_agents": false,
  "system": ""
}
```

### Project overrides (`./kode.json`)

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

Every config knob has a `KODE_*` counterpart:

| Variable | Maps to | Type |
|----------|---------|------|
| `KODE_MODEL` | `--model` | string |
| `KODE_BASE_URL` | `--base-url` | string |
| `KODE_API_KEY` | config files only | string |
| `KODE_THINKING` | `--thinking` | string |
| `KODE_MAX_ITER` | `--max-iter` | int |
| `KODE_SANDBOX` | `--sandbox` | bool |
| `KODE_NO_COLOR` | `--no-color` | bool |
| `KODE_NO_AGENTS` | `--no-agents` | bool |
| `KODE_SYSTEM` | `--system` | string |
| `KODE_SANDBOX_IMAGE` | `--sandbox-image` | string |
| `KODE_SANDBOX_NETWORK` | `--sandbox-network` | string |
| `KODE_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
| `KODE_SANDBOX_MEMORY` | `--sandbox-memory` | string |
| `KODE_SANDBOX_CPUS` | `--sandbox-cpus` | string |
| `KODE_SANDBOX_USER` | `--sandbox-user` | string |

## API key fallback order

`KODE_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`

## kode init

Create a config file template:

```bash
# Local project config (./kode.json)
kode init

# Global config (~/kode/config.json)
kode init --global

# Overwrite existing file
kode init --force
```

## Quick examples

```bash
# Global config
echo '{"api_key": "${DEEPSEEK_API_KEY}", "model": "deepseek-v4-flash"}' > ~/kode/config.json
kode run "list files"

# Per-project override
echo '{"max_iterations": 30}' > ./kode.json
kode run "quick status"

# Env var override for one-off
KODE_SANDBOX=true kode run "run untrusted script"

# CLI flag always wins
kode run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```
