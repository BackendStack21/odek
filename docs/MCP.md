# MCP

odek speaks Model Context Protocol in both directions, in one binary:

- **Server** — `odek mcp` exposes odek's built-in tools over stdio to Claude Code, Cursor, or any MCP client.
- **Client** — `mcp_servers` in config spawns external MCP servers and registers their tools on the agent as `<server>__<tool>`.

The [odek-extension/v1](EXTENSIONS.md) contract is optional on top of plain MCP: per-server limits, artifact refs, events, and budgets. A server that only implements MCP `2025-03-26` still works.

---

## Server (`odek mcp`)

```bash
odek mcp
```

JSON-RPC 2.0 on stdin/stdout. Logs go to stderr.

### Connect a client

**Claude Code** — user scope in `~/.claude.json`, or project scope in `.mcp.json` at the repo root (not `.claude/settings.json`):

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

**Cursor** — the same `mcpServers` object in `~/.cursor/mcp.json` (all projects) or `.cursor/mcp.json` (this repo).

### Exposed tools

`odek mcp` exposes the built-in tool set through `tools/list` / `tools/call`, after `tools.enabled` / `tools.disabled`. Two tools are never served: `delegate_tasks` and `memory`. Surface-only tools (`clarify`, `send_message`, `bg_*`) are not on this path either.

| Always (default config) | Conditional |
|-------------------------|-------------|
| `shell`, `parallel_shell`, `patch`, `batch_patch` | `web_search` — only when `web_search.base_url` is set |
| `read_file`, `write_file`, `search_files`, `batch_read`, `glob`, `file_info` | `plan` — only when planning is enabled (on by default) |
| `browser`, `http_batch` | |
| `multi_grep`, `diff`, `tree`, `count_lines`, `head_tail`, `word_count`, `checksum`, `sort`, `base64`, `tr`, `json_query`, `math_eval` | |
| `session_search`, `transcribe`, `vision` | |
| `skill_load`, `skill_list`, `artifact_read`, `list_subagent_profiles` | |
| `config_view`, `list_tools` | |

`transcribe` and `vision` always register; a missing binary fails at call time, not at `tools/list`.

Configured `mcp_servers` are loaded into `odek mcp` too, so a client talking to odek also sees those `<server>__<tool>` names. `tools.enabled` / `tools.disabled` apply after that load.

### Sandbox

```bash
odek mcp --sandbox
```

Sandbox is **opt-in** here (`--sandbox` or config `sandbox: true`). That is not the same as `odek run`, which defaults the sandbox on. The project-sandbox approval gate still runs. Shell then uses Docker with `--cap-drop ALL`, `--security-opt no-new-privileges`, resource limits, and noexec tmpfs.

### Security

Same `DangerousConfig` as `odek run`. There is no TTY, so prompt-class operations use `non_interactive` (built-in default **`read_only`**: inspection proceeds, writes / exec / egress deny). `"deny"` blocks prompted work including reads; `"allow"` runs everything.

```json
{
  "dangerous": {
    "non_interactive": "read_only"
  }
}
```

### Protocol

Stdio JSON-RPC 2.0. Default version `2026-07-28`; also accepts `2025-11-25`, `2025-03-26`, and `2024-11-05`.

| Method | Role |
|--------|------|
| `server/discover` | Stateless protocol / capability discovery |
| `initialize` / `initialized` | Handshake (legacy clients) |
| `tools/list`, `tools/call` | Tool catalog and invocation |
| `ping` | Health check (empty object) |
| `resources/*`, `prompts/*` | Compatibility; odek registers none, so lists are empty |

Requests are capped at 10 MiB. Malformed JSON-RPC is rejected. Tool-handler panics return `tool handler panicked` with no panic value on the wire or stderr.

A stateless modern `tools/call` must send protocol `2026-07-28` **and** client capabilities in `_meta`. Metadata-free calls are accepted only after a successful `initialize`.

---

## Client (`mcp_servers`)

odek spawns each configured server as a subprocess, sends `initialize` with protocol `2025-03-26`, then `tools/list`, and registers each tool as `<server>__<tool>`.

### Where it loads

| Surface | MCP client |
|---------|------------|
| `odek run`, `continue`, `repl`, `serve`, `mcp` | Yes. Connect / discover failure aborts the process. |
| `odek schedule daemon` / `schedule run` | Yes. Same fail-closed. |
| Trusted / capped sub-agents | Yes. |
| Untrusted sub-agents | **No.** Adapters do not danger-classify MCP tools. |
| `odek telegram` chat agent | **No.** |
| Scheduler embedded in `odek telegram` | Yes, but a connect failure is non-fatal (the bot keeps running without those tools). |

### Configuration

Put `mcp_servers` in `~/.odek/config.json` (operator-trusted) or `./odek.json` (project; extra approval). The `command` / `args` / `env` shape matches Claude Code's `mcpServers` object. odek does **not** expand `${VAR}` in `mcp_servers.*.env`.

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
      "env": { "LOG_LEVEL": "debug" },
      "timeout_seconds": 120,
      "max_response_bytes": 2097152,
      "max_result_chars": 100000,
      "artifact_roots": ["/var/ci-artifacts"]
    }
  }
}
```

| Field | Default | Notes |
|-------|---------|-------|
| `command` | required | Executable to spawn. |
| `args` | `[]` | |
| `env` | `{}` | Overrides; empty string unsets. Secret-looking keys are stripped even here. |
| `timeout_seconds` | `30` | Per-request; clamped to 3600 (warning). |
| `max_response_bytes` | 10 MiB | One JSON-RPC response line. Config above 64 MiB is rejected and the server is not started. Oversized line: drop it and close the connection. |
| `max_result_chars` | `200000` | Model-facing result text; clamped to 1_000_000 (warning). Oversized valid results get a structured truncation notice, never a silent cut. |
| `artifact_roots` | `[]` | Directories that may host `file://` artifact refs. **Empty rejects every ref.** |
| `auto_approve` | `false` | Skip server and per-tool prompts. Honored only from `~/.odek/config.json`. |

The four limit fields are **odek-extension/v1**; semantics live in [EXTENSIONS.md](EXTENSIONS.md).

**Environment.** Children get a small allowlist (`PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `LANG`, the listed `LC_*` locale variables, `TZ`, `TERM`) plus `env` overrides. Names matching secret patterns (`APIKEY`, `TOKEN`, `SECRET`, `PASSWORD`, `CREDENTIAL`, `CREDS`, `PRIVATEKEY`, `ACCESSKEY` after uppercasing and stripping `-`/`_`) are removed even from `env`. Pass auth via the server's own config file or argv, not the parent environment. Child stderr is inherited, so server crashes show up in odek's log. Details: [SECURITY.md — MCP hardening](SECURITY.md#mcp-hardening).

### Naming

Registered name is `<server>__<tool>` (`playwright__navigate`). Server and tool names must be 1–64 ASCII letters, digits, `_`, or `-`, and must not contain `__`. Invalid names or a collision with a built-in tool (`shell`, `clarify`, …) **abort startup** — they are not skipped with a warning.

### Approvals

Two layers, both fail closed when no TTY and nothing else grants trust.

**1. Server spawn** — project-level servers only (`./odek.json`). Global servers in `~/.odek/config.json` skip this. Stored in `~/.odek/mcp_approvals.json` (0600). The key hashes project directory, server name, command, args, env, and the four extension limit fields (`timeout_seconds`, `max_response_bytes`, `max_result_chars`, `artifact_roots`). Schema and description are **not** in this key.

**2. Per-tool register** — **every** server, including global. Stored in `~/.odek/mcp_tool_approvals.json` (0600). The key hashes project directory, server name, tool name, command, args, env, the four limit fields, the canonical-JSON SHA-256 of `inputSchema`, and the full description. The TTY prompt shows the (sanitized) description plus `schema: sha256:… (N bytes)` — not the env map. Env values are shown on the **server** prompt.

Ways to approve:

1. Interactive `Approve? [y/N]` on a TTY.
2. `ODEK_APPROVE_MCP=1` for that invocation (all project servers **and** all tools).
3. A matching persisted key (any hashed field change re-prompts; older odek keys that omitted the limit fields re-prompt once after upgrade).
4. `auto_approve: true` in the **global** config (see below).

Declining a single tool skips that tool; the rest still load. If server approval is required and cannot be obtained, odek aborts before spawning any MCP server.

### `auto_approve`

Removes both prompts. Schema scans, size caps, name checks, env sanitization, and artifact-ref validation still run.

- **`~/.odek/config.json`** — honored, and only when the execution fingerprint matches: same command, args, env, timeout, response/result caps, and `artifact_roots`.
- **`./odek.json`** — stripped with a warning. A cloned repo cannot pre-approve its own servers.
- A command-less global `{ "auto_approve": true }` is **not** a name wildcard. It is dropped (not a connectable server) and does not bless a project-defined command of the same name. If a project entry swaps command/args/env/limits, global `auto_approve` does not follow.

### Tool schemas

Before a tool is registered, every string in `inputSchema` is injection-scanned (`mcp_schema` scope). A hit skips the tool (stderr warning). Serialized schemas over 256 KiB are skipped. Description hits withhold the description (placeholder) but keep the tool callable by name. Passing descriptions are still wrapped as untrusted data.

### Lifecycle

On startup: spawn → `initialize` (`2025-03-26`) → `tools/list` → approve → register. On exit, stdin is closed, the process is given 5s, then killed. Default per-request timeout is 30s when neither the caller nor `timeout_seconds` sets one.

Connect or discover errors are fatal on `run` / `continue` / `repl` / `serve` / `mcp` / standalone schedule — odek shuts down servers already started and aborts rather than running with a partial set. The Telegram-embedded scheduler is the exception (soft-fail).

Stderr from MCP children is shown on odek's stderr. There is no `odek: connected MCP server "…"` success line.

### Artifacts

A server can return an `odek.tool-result/v1` envelope with `file://` refs instead of bulk content. Validation is fail-closed (schema, absolute path, symlink-resolved containment in `artifact_roots`, hash/size, 64 MiB / 64 refs). The model sees metadata only — never the path or file bytes. Empty `artifact_roots` rejects every ref. See [EXTENSIONS.md](EXTENSIONS.md).

Any stdio MCP server that implements `tools/list` and `tools/call` works (Playwright, Fetch, GitHub, filesystem, …).
