# Tool Selection Guide

Control which tools odek exposes to the LLM. By default every built-in tool is
available, but many deployments want a smaller surface: a chatbot with only
search and voice, a read-only research assistant, or a locked-down CI runner.

## Default behaviour

With no `tools` configuration, odek registers **all** built-in tools that its
environment supports:

- Core tools: `shell`, `delegate_tasks`, `read_file`, `write_file`, `search_files`,
  `patch`, `batch_read`, `batch_patch`, `glob`, `file_info`, `parallel_shell`,
  `http_batch`, `math_eval`, `diff`, `count_lines`, `multi_grep`, `json_query`,
  `tree`, `checksum`, `sort`, `head_tail`, `base64`, `tr`, `word_count`
- Media tools: `transcribe`, `vision`
- Memory: `memory` (persistent facts/episodes)
- Session search: `session_search`
- Browser: `browser`
- Web search: `web_search` (only when `web_search.base_url` is configured)
- Skill tools: `skill_load`, `skill_list`, `skill_save`, `skill_patch`,
  `skill_delete` (only when skill learning is enabled)
- MCP tools: prefixed as `<server>__<tool_name>` (only when `mcp_servers` are
  configured)

Nothing is hidden by default. You opt out with `disabled`, or opt in with
`enabled`.

## Configuration

Use the `tools` section in any operator-controlled config source:

| Source | File / mechanism |
|---|---|
| Global config | `~/.odek/config.json` |
| Project config | `./odek.json` — **can only disable, never enable** |
| Environment | `ODEK_TOOLS_ENABLED`, `ODEK_TOOLS_DISABLED` |
| CLI | `--tool <name>`, `--no-tool <name>` |

Priority, highest to lowest:

```
CLI flags → ODEK_* env vars → ./odek.json → ~/.odek/config.json
```

`enabled` is replaced by the highest layer that sets it. `disabled` is merged
across layers.

## Schema

```json
{
  "tools": {
    "enabled": ["web_search", "transcribe", "vision", "send_message"],
    "disabled": ["shell", "write_file", "patch", "delegate_tasks"]
  }
}
```

- `enabled` — whitelist. When non-empty, only these tools are registered.
  An empty array means **no tools at all**.
- `disabled` — blacklist. Removed from the default set (or from `enabled`
  when both are present).

## Examples

### Chatbot with web search and voice

```bash
# CLI only
odek run \
  --tool web_search \
  --tool transcribe \
  --tool vision \
  --tool send_message \
  --no-tool shell \
  --no-tool write_file \
  --no-tool patch \
  --no-tool delegate_tasks \
  "what's the weather in Tokyo?"
```

Or set it once in `~/.odek/config.json`:

```json
{
  "tools": {
    "enabled": ["web_search", "transcribe", "vision", "send_message"]
  }
}
```

### Read-only research assistant

```json
{
  "tools": {
    "enabled": [
      "browser",
      "web_search",
      "read_file",
      "session_search",
      "multi_grep",
      "search_files"
    ]
  }
}
```

This agent can read and search but cannot write files, run shell commands, or
spawn sub-agents.

### Locked-down CI runner

```json
{
  "tools": {
    "disabled": [
      "write_file", "patch", "batch_patch", "delegate_tasks",
      "browser", "web_search"
    ]
  }
}
```

Keeps `shell` available for builds/tests but removes file-mutation, delegation,
and network tools.

### Disable persistent memory

```json
{
  "tools": {
    "disabled": ["memory"]
  }
}
```

The `memory` tool is also subject to filtering. If you use an `enabled`
whitelist and want memory, include `"memory"` explicitly.

## Environment variables

```bash
# Whitelist via env
ODEK_TOOLS_ENABLED=web_search,vision odek run "compare these phones"

# Blacklist via env
ODEK_TOOLS_DISABLED=shell,write_file,patch odek run "review this diff"
```

## CLI flags

```bash
odek run --tool web_search --tool vision --no-tool shell "find me a recipe"
```

Flags override environment and file config. `--tool` sets the whitelist;
`--no-tool` adds to the blacklist.

## Security note

`./odek.json` is treated as untrusted. It may add to `tools.disabled`, but any
`tools.enabled` it sets is ignored. This prevents a malicious repository from
widening the tool surface (for example, enabling `shell` in a shared project).

If `./odek.json` contains `tools.enabled`, odek prints a warning and uses the
operator-controlled source instead.

## Tool names reference

Use these exact names in config, env vars, and CLI flags:

| Category | Names |
|---|---|
| Shell / execution | `shell`, `parallel_shell` |
| Delegation | `delegate_tasks` |
| Files | `read_file`, `write_file`, `patch`, `batch_read`, `batch_patch`, `glob`, `file_info` |
| Search | `search_files`, `multi_grep`, `session_search` |
| Data / transform | `math_eval`, `diff`, `count_lines`, `json_query`, `tree`, `checksum`, `sort`, `head_tail`, `base64`, `tr`, `word_count`, `http_batch` |
| Media | `transcribe`, `vision` |
| Network | `browser`, `web_search` |
| Memory | `memory` |
| Skills | `skill_load`, `skill_list`, `skill_save`, `skill_patch`, `skill_delete` |
| Telegram | `send_message`, `clarify` |
| MCP | `<server>__<tool_name>` |

Unknown names are silently ignored, so typos do not crash startup.

## Mode-specific required tools

Some odek modes preserve tools they need to function:

- **Telegram** always keeps `send_message` and `clarify` so the bot can respond
  and ask clarifications, even if you disable them.
- Other modes respect the filter exactly as configured.

## Choosing between whitelist and blacklist

- Use **`enabled`** when you know exactly which tools the deployment needs.
  This is the safest default for limited-purpose agents.
- Use **`disabled`** when you want the full agent but want to remove a few
  risky tools (for example, disable `shell` and `delegate_tasks` in an
  untrusted-input environment).

You can combine both: `enabled` narrows the set, then `disabled` removes
specific tools from that narrowed set.
