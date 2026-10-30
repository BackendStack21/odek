# Threat model — odek

The full, maintained threat model is `docs/SECURITY.md` in this repository. Read it first; this file summarizes it for the scanner.

## What this project does and where untrusted input enters
- odek is an autonomous LLM agent runtime (single Go binary): a ReAct loop that calls tools (`shell`, `read_file`, `write_file`, `patch`, `browser`, `http_request`, `web_search`, MCP tools, sub-agents, background jobs) on the operator's machine or in a Docker sandbox.
- Untrusted, and the main attack surface: everything the agent ingests — web pages, HTTP responses (including redirects), file contents, shell output, search results, MCP tool descriptions/results/errors, transcribed audio, images, imported skills, project config (`./odek.json`), and `AGENTS.md` files in a project. Treat all of these as attacker-controlled (prompt injection).
- Untrusted network clients: the `odek serve` Web UI / REST / WebSocket API, and Telegram messages from users other than the configured allowlist.
- Trusted: the operator, `~/.odek/config.json`, `~/.odek/secrets.env`, CLI flags and `ODEK_*` env vars.

## Components that matter most / least
- Highest priority: `internal/danger` (shell danger classifier — parses shell syntax and must fail closed), `internal/guard` and tool-call approval flow, `internal/sandbox` (Docker isolation), `internal/tool` (native tools, path handling, SSRF/egress checks), `internal/config` (trust split between global and project config), `internal/ws` / `internal/transport` and `cmd/odek` serve handlers (auth on the Web UI/API), `internal/telegram` (sender allowlist), `internal/mcp` / `internal/mcpclient`, `internal/pathutil`, `internal/fsatomic`, `internal/redact`.
- Medium: `internal/memory` (taint tracking), `internal/skills` (provenance gate), `internal/session`, `internal/schedule`, `internal/bgproc`.
- Out of scope: `docs/` site assets, `scripts/`, `tests/` fixtures, `internal/eval`, the LLM providers themselves.

## How to exercise it
- `go test ./... -short` (unit tests; E2E and sandbox suites are env-gated). `/usr/local/bin/odek` is prebuilt in the image.
- No LLM credentials or Docker daemon are available in the scan environment; drive components through their Go APIs and tests.

## How you rate severity
- Critical: untrusted content (a web page, file, tool output, MCP server, project config) causing command execution, file write outside the workspace, or secret exfiltration **without** an operator approval that accurately shows what will run; sandbox escape to the host; unauthenticated remote access to `odek serve` or the Telegram bot that can run tools.
- High: a danger-classifier bypass that makes a dangerous command classify as `safe`/auto-allowed under the default config; SSRF to internal/metadata addresses through `browser`/`http_request`; approval prompt spoofing (control/bidi chars, stale approvals); project config overriding operator-only fields; path traversal in file tools; secrets leaking into logs, events or session files.
- Medium: memory/skill taint bypasses that need a later operator action, resource-limit bypasses (budgets, output bounds), DoS of the agent process.
- Low: hardening, defense-in-depth gaps that are already covered by another layer.

## Anything to leave alone
- The model choosing to call a tool is not itself a vulnerability: report only what bypasses odek's gating, sandboxing, or boundaries.
- Behavior with `--no-sandbox` or with `dangerous` rules the operator explicitly set to `allow`.
