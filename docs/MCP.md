# MCP

odek has **two-way** Model Context Protocol support:

- **odek as MCP server** (`odek mcp`) — other agents (Claude Code, Cursor) use odek's tools
- **odek as MCP client** (config) — odek connects to external MCP servers and uses their tools

---

## odek as MCP Server (`odek mcp`)

Start odek as an MCP server over stdio. This lets Claude Code, Cursor, and other
MCP-compatible clients use odek's built-in tools.

```bash
odek mcp
```

### Claude Code setup

Add to `~/.claude/claude_dotfiles/claude.json` or your project's `.claude/settings.json`:

```json
{
  "mcpServers": {
    "odek": {
      "command": "odek",
      "args": ["mcp"]
    }
  }
}
```

For **Cursor**, add the same entry in Cursor Settings → MCP Servers.

### Exposed tools

| Tool | Description |
|------|-------------|
| `shell` | Run shell commands (with security classification) |
| `read_file` | Read files with line numbers and pagination |
| `write_file` | Write content to files (creates directories) |
| `search_files` | Search file contents or find files by name |
| `patch` | Find-and-replace edits with fuzzy matching |
| `browser` | Navigate web pages, take snapshots, click elements |

The `delegate_tasks` and `memory` tools are **not** exposed via MCP — they are
specific to odek's own agent loop.

### Sandbox

```bash
odek mcp --sandbox
```

All shell commands run inside a Docker container with `--cap-drop ALL`,
`--security-opt no-new-privileges`, resource limits, and noexec tmpfs.

### Security

Same `DangerousConfig` as `odek run`. No TTY in MCP mode → `non_interactive`
fallback applies (default: deny). Configure per-class overrides in `odek.json`:

```json
{
  "dangerous": {
    "non_interactive": "deny",
    "classes": {
      "network_egress": "deny",
      "code_execution": "prompt"
    }
  }
}
```

### Protocol

Stdio transport with JSON-RPC 2.0:

- `initialize` — protocol handshake (`protocolVersion: "2025-03-26"`)
- `tools/list` — returns all available tools with schemas
- `tools/call` — invokes a tool with the given arguments
- `ping` — health check (returns empty object)
- `initialized` — notification

Logging goes to stderr; stdin/stdout are reserved for the MCP protocol.

---

## odek as MCP Client

odek can connect to **external MCP servers** and expose their tools to the agent
during `odek run`, `odek repl`, `odek serve`, and `odek mcp`.

### Configuration

Add `mcp_servers` to `~/.odek/config.json` (global, operator-trusted) or `odek.json`
(project-level):

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
    },
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"],
      "env": {
        "LOG_LEVEL": "debug"
      }
    }
  }
}
```

Each server is defined by:
- `command` — the executable to run
- `args` — optional command-line arguments
- `env` — optional environment variable overrides (empty string removes the variable)

> **Environment sanitisation.** MCP server children receive only a minimal
> allowlist of safe variables (e.g. `PATH`, `HOME`, `LANG`) plus the overrides
> from `env`. Keys matching secret patterns (`*_API_KEY`, `*_TOKEN`,
> `*_SECRET`, `*_PASSWORD`, etc.) are stripped even when listed in `env`, so a
> compromised server cannot exfiltrate parent secrets. Pass authentication
> material via server-specific config files or command-line arguments instead
> of environment variables.

The format matches Claude Code's `mcpServers` config — any MCP server you use
with Claude Code can be added to odek's config.

### Project-level MCP server approval

Because `mcp_servers` in `./odek.json` can execute arbitrary commands, odek
requires **explicit approval** for any server introduced by a project config
before it spawns the subprocess. Global servers from `~/.odek/config.json` are
operator-trusted and do not require approval.

Approval methods:

1. **Interactive prompt** — when running on a TTY, odek asks for each project
   server: `Approve? [y/N]`.
2. **`ODEK_APPROVE_MCP=1`** — approve all project MCP servers for a single
   invocation. Useful in CI, scheduled jobs, or non-interactive use:
   ```bash
   ODEK_APPROVE_MCP=1 odek run "task"
   ```
3. **Persisted approvals** — approvals are stored in
   `~/.odek/mcp_approvals.json` (0600) keyed by project directory + server name
   + command + args + sorted `env` map hash. If the config changes, you are
   prompted again.

If approval is required and cannot be obtained, odek aborts before spawning any
MCP server.

### Project-level MCP tool approval

After a project-level server is approved, each individual tool it advertises via
`tools/list` must also be approved before the agent can call it. This prevents a
server from quietly registering a high-risk or spoofed tool after its server
config was reviewed.

Tool approval uses the same methods as server approval:

1. **Interactive prompt** — on a TTY, odek lists the discovered tools and asks
   which to approve.
2. **`ODEK_APPROVE_MCP=1`** — approves every tool from every project-level
   server for the invocation.
3. **Persisted approvals** — approved tools are stored in
   `~/.odek/mcp_tool_approvals.json` (0600), keyed by project directory + server
   name + tool name + sorted `env` map hash. If a tool is renamed, a new tool
   appears, or the server's `env` changes, it must be approved again.

Tools from global servers (`~/.odek/config.json`) are operator-trusted and do
not require per-tool approval.

### How it works

On startup, odek:
1. Spawns each configured MCP server as a subprocess
2. Performs the MCP handshake (`initialize`)
3. Discovers all tools via `tools/list`
4. Registers each tool as `<server_name>__<tool_name>` (e.g., `playwright__navigate`)
5. Forwards `tools/call` requests to the appropriate server
6. Cleans up all server processes on exit

### Naming

Tools are prefixed with the server name to avoid collisions between servers:

- `playwright__navigate` — from the `playwright` server
- `fetch__fetch` — from the `fetch` server
- `github__search_issues` — from the `github` server

Tool names must be ASCII letters, digits, underscores, or hyphens and no longer
than 64 characters; they must not contain `__`. Names that do not match this
pattern, or that collide with odek's built-in tool names (even before prefixing),
are rejected at startup with a warning. Server names follow the same rules and
also must not contain `__`, preventing collisions where server `a` + tool
`b__c` would otherwise look identical to server `a__b` + tool `c`.

### Tool schema hardening

MCP servers supply an `inputSchema` JSON for every tool. That schema is
serialized into the model's function catalogue, so a malicious server could hide
instructions in property descriptions, default values, or enum strings.

Before a tool is registered, odek:

- Recursively scans every string in `inputSchema` with the same injection guard
  used for tool descriptions. Tools that trigger the guard are skipped.
- Rejects serialized schemas larger than 256 KiB to prevent prompt stuffing.
- Displays a SHA-256 hash and byte size of the canonical schema in the
  interactive tool-approval prompt, so you can notice when a previously-approved
  tool's schema has changed.

### What MCP servers work

Any server that implements the MCP stdio transport with `tools/list` and
`tools/call`. Common examples:

- **Playwright MCP** (`npx @playwright/mcp`) — browser automation
- **Fetch MCP** (`uvx mcp-server-fetch`) — HTTP requests
- **GitHub MCP** — repository management
- **SQLite MCP** — database queries
- **Filesystem MCP** — file operations
- **Docker MCP** — container management

### Lifecycle

MCP server processes are spawned when odek starts and killed when odek exits
(via `defer`). Each process gets its own stdin/stdout pipes — stderr from
MCP servers is shown in the odek console.

Each MCP request uses a default timeout when the caller does not supply one, so
a hung server cannot block discovery or tool calls indefinitely.

### Logging

odek logs MCP server connections to stderr:

```
odek: connected MCP server "playwright" (5 tools)
odek: connected MCP server "fetch" (1 tool)
```

Errors during discovery are reported at startup — the server is skipped and
odek continues with the remaining servers.

### Config reference

```json
{
  "mcp_servers": {
    "my-server": {
      "command": "command",
      "args": ["arg1", "arg2"],
      "env": {
        "API_KEY": "${MY_API_KEY}",
        "REMOVE_ME": ""
      }
    }
  }
}
```
