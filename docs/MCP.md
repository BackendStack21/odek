# MCP Server (`kode mcp`)

kode implements the [Model Context Protocol](https://modelcontextprotocol.io) — a
standard interface for AI agents to discover and invoke tools. This allows Claude
Code, Cursor, and other MCP-compatible clients to use kode's built-in tools.

## Quick start

```bash
# Start kode MCP server
kode mcp
```

Then configure your MCP client to connect. For **Claude Code**, add this to
`~/.claude/claude_dotfiles/claude.json` or your project's `.claude/settings.json`:

```json
{
  "mcpServers": {
    "kode": {
      "command": "kode",
      "args": ["mcp"]
    }
  }
}
```

For **Cursor**, add a similar entry in Cursor Settings → MCP Servers.

## What tools are exposed

| Tool | Description |
|------|-------------|
| `shell` | Run shell commands (with security classification) |
| `read_file` | Read files with line numbers and pagination |
| `write_file` | Write content to files (creates directories) |
| `search_files` | Search file contents or find files by name |
| `patch` | Find-and-replace edits with fuzzy matching |
| `browser` | Navigate web pages, take snapshots, click elements |

The `delegate_tasks` and `memory` tools are **not** exposed via MCP — they are
specific to kode's own agent loop and don't translate to the MCP tool model.

## Security

The MCP server uses the same `DangerousConfig` and security classification as
`kode run`. There's no TTY in MCP mode, so the `non_interactive` fallback applies:

- **Default**: `"allow"` — all commands run without prompting
- Configure `"non_interactive": "deny"` in your kode config to block prompted
  operations in MCP mode
- Configure per-class overrides (e.g., `"network_egress": "deny"`) for
  fine-grained control

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

See [SECURITY.md](SECURITY.md) for details.

## Protocol

The server uses **stdio transport** — JSON-RPC 2.0 messages over stdin/stdout:

- `initialize` — protocol handshake (`protocolVersion: "2025-03-26"`)
- `tools/list` — returns all available tools with schemas
- `tools/call` — invokes a tool with the given arguments
- `ping` — health check (returns empty object)
- `initialized` — notification (no response expected)

Standard MCP capabilities (`tools`, `resources`, `prompts`) — currently only
`tools` are implemented.

## Logging

Startup info and errors are written to stderr. stdin/stdout are reserved for
the MCP protocol. Claude Code captures stderr automatically and shows it in
its logs.
