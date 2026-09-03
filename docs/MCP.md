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

`odek mcp` exposes **all built-in tools** over `tools/list` / `tools/call`, subject to the standard `tools.enabled` / `tools.disabled` filter. Two tools are never exposed — `delegate_tasks` and `memory` are specific to odek's own agent loop. `web_search` appears only when a SearXNG backend is configured (`web_search.base_url`).

Default exposure (no `tools` config, no SearXNG):

| Tools | |
|------|------|
| `shell`, `parallel_shell`, `patch`, `batch_patch` | mutation paths (danger-classified) |
| `read_file`, `write_file`, `search_files`, `batch_read`, `glob`, `file_info` | file access |
| `browser`, `http_batch`, `web_search`* | web access (*only when SearXNG configured) |
| `multi_grep`, `diff`, `tree`, `count_lines`, `head_tail`, `word_count`, `checksum`, `sort`, `base64`, `tr`, `json_query`, `math_eval` | inspection & transforms |
| `session_search`, `transcribe`, `vision` | sessions & media |
| `plan`* | planning (*only when planning is enabled) |
| `skill_load`, `skill_list`, `artifact_read`, `list_subagent_profiles` | skills, artifacts, sub-agent profiles |
| `config_view`, `list_tools` | sanitized config + tool-registry introspection |

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
- `timeout_seconds` — optional per-request timeout (default `30`, hard cap `3600`;
  values above the cap are clamped with a startup warning)
- `max_response_bytes` — optional cap on a single JSON-RPC response line
  (default 10 MiB; absolute ceiling 64 MiB — a config above the ceiling is
  rejected and the server is not started). An oversized response **fails
  closed**: the line is dropped and the connection closed
- `max_result_chars` — optional cap on tool result text forwarded to the model
  (default `200000`, hard cap `1000000`, clamped with a warning). A valid
  result over the cap gets a structured truncation notice naming the server,
  tool, limit, and observed size; `odek.tool-result/v1` envelopes keep their
  artifact refs. Malformed results are never silently truncated into place
- `artifact_roots` — optional directories under which `file://` artifact refs
  are accepted; **empty (default) rejects every artifact ref (fail closed)**
- `auto_approve` — optional operator trust flag (`true`/`false`, default
  `false`): the server skips the project-server approval prompt and the
  per-tool registration prompts (schema guard scans and size caps still
  apply). **Honor rules**: the flag is only honored from the operator-owned
  `~/.odek/config.json` — a `./odek.json` declaration is stripped with a
  warning, because a cloned repo must never be able to approve its own MCP
  servers. See [Auto-approving trusted servers](#auto-approving-trusted-servers).

The four limit fields are the **odek-extension/v1** server config schema;
see [EXTENSIONS.md](EXTENSIONS.md) for the normative contract.

### Artifact references (`odek.artifact-ref/v1`)

A server whose full output is too large for the model context can return an
`odek.tool-result/v1` envelope: a compact text summary plus references to
on-disk artifacts. odek validates every reference **fail-closed** before
anything reaches the model:

- exact schema match; `file://` URIs only; absolute, clean paths
- the resolved path (after `filepath.EvalSymlinks` on both path and roots)
  must lie inside a configured `artifact_roots` entry — empty roots reject
  every ref
- `sha256` and `size_bytes` are verified against the real file when present;
  missing files are errors
- any violation fails the whole tool call, naming server, tool, ref id, and
  reason

Artifact **content is never auto-read into the model context** — the model
sees the envelope text plus one metadata line per artifact (id, media type,
size, short hash, summary), never an absolute path. Validated paths stay
local. See [EXTENSIONS.md](EXTENSIONS.md) for the schema; a reference mock
server lives at `internal/mcpclient/testdata/artifact_server.go`.

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
   `~/.odek/mcp_approvals.json` (0600) keyed by project directory + server
   name + command + args + sorted `env` map hash + the canonical-JSON SHA-256
   of the input schema + the full tool description text + the
   odek-extension/v1 limit fields (`timeout_seconds`, `max_response_bytes`,
   `max_result_chars`, `artifact_roots`). If any of these change, you are
   prompted again — editing `artifact_roots` widens the set of files a server
   may hand to the agent, so it can never silently reuse an old approval.
   **Upgrade note:** because the odek-extension/v1 limit fields joined the
   hash, approvals persisted by an older odek re-prompt **once** after
   upgrading, then stick.
4. **`auto_approve: true`** in the global config — pre-trust a server and
   skip both prompts; see [Auto-approving trusted servers](#auto-approving-trusted-servers)

If approval is required and cannot be obtained, odek aborts before spawning any
MCP server.

### Auto-approving trusted servers

`auto_approve: true` removes the approval friction for servers you have
vetted — no server prompt, no per-tool prompts. Because the entire point of
the approval gate is that `./odek.json` is attacker-controllable (any repo
you clone could ship one), the flag obeys strict trust rules:

- **Global config (`~/.odek/config.json`)** — honored. Set it on a global
  server entry, or on any server you run.
- **Global trust marker for a project server** — a command-less global entry
  whose name matches a project-defined server applies the flag to it:

  ```jsonc
  // ~/.odek/config.json — "I trust whatever 'my-dev-server' is in this
  // project family, don't ask me again":
  { "mcp_servers": { "my-dev-server": { "auto_approve": true } } }
  ```

  The marker itself never becomes a connectable server. Changing the
  project's command/args no longer re-prompts (your global trust covers the
  name), which is the convenience trade-off you are opting into — revoke by
  deleting the marker.
- **Project config (`./odek.json`)** — **ignored with a warning**. A repo
  you cloned cannot approve its own MCP servers; the operator's global
  config is the only source of this trust.

Safety nets that always remain, regardless of the flag: per-tool schema
guard scans and size caps, built-in-name shadowing rejection, subprocess
env sanitization, artifact-ref fail-closed validation, and the extension
limits. `auto_approve` removes prompts, not checks.

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

Per-tool approval is required for **every** server — global servers are not
exempt. A globally-configured server can be pre-trusted with `auto_approve: true`
(global config only; the field is stripped from project config with a warning),
or per-tool prompts can be satisfied via `ODEK_APPROVE_MCP=1` or a persisted
approval. Without one of these, tool registration fails closed in
non-interactive runs.

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

Each MCP request uses a default timeout of 30s when neither the caller nor the
server config (`timeout_seconds`) supplies one, so a hung server cannot block
discovery or tool calls indefinitely.

### Logging

odek logs MCP server connections to stderr:

```
odek: connected MCP server "playwright" (5 tools)
odek: connected MCP server "fetch" (1 tool)
```

Errors during discovery are fatal at startup: odek reports the error, shuts
down any servers already started, and aborts rather than running with a
partial tool set.

### Config reference

```json
{
  "mcp_servers": {
    "my-server": {
      "command": "command",
      "args": ["arg1", "arg2"],
      "env": {
        "MY_SERVER_SETTING": "literal-value-here",
        "REMOVE_ME": ""
      },
      "timeout_seconds": 120,
      "max_response_bytes": 2097152,
      "max_result_chars": 100000,
      "artifact_roots": ["/var/ci-artifacts"]
    }
  }
}
```

Note: `env` values are passed to the server process verbatim — `${VAR}`
expansion is **not** performed for `mcp_servers.*.env` (it only applies to
the model connection fields). Secret-pattern names (`*API_KEY*`, `*TOKEN*`,
…) are additionally stripped from the child environment, so put secrets in
the server's own config file under operator control rather than in
`odek.json`.
