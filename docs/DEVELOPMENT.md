# Development

## Prerequisites

- Go 1.24+
- Docker (for sandbox integration tests only)

## Building

```bash
go build -o kode ./cmd/kode
```

## Source layout

```
kode.go                       Public API (Config, New, Run, Close)
kode_test.go                  Config and model profile tests
internal/
  config/
    loader.go                 Config file loading, env vars, priority merge
    loader_test.go            Config loading tests
  llm/
    client.go                 OpenAI-compatible HTTP client
    client_test.go            JSON marshaling + response parsing tests
  loop/
    loop.go                   ReAct engine (observe → think → act → repeat)
    loop_test.go              Engine tests with mock server
  session/
    session.go                Session store (CRUD, trim, cleanup)
    session_test.go           Session tests
  render/
    render.go                 Terminal output with model label and color
  resource/
    resource.go               @-reference resolver (files, sessions)
    resource_test.go          Parse, resolve, search tests
  ws/
    ws.go                     RFC 6455 WebSocket framing (~200 LOC)
    ws_test.go                Handshake, framing, ping/pong tests
  tool/
    registry.go               Thread-safe tool registry
    registry_test.go          Registry tests
  danger/
    classifier.go             Command/URL classification for security gating
    classifier_test.go        209 tests, 8 risk classes, config overrides
    approver.go               Approver interface + TTYApprover (CLI /dev/tty)
  memory/
    memory.go                 MemoryManager orchestrator (facts, buffer, episodes)
    facts.go                  FactStore with caps, dedup, substring CRUD
    buffer.go                 Ring buffer for turn summaries
    merge.go                  go-vector RP merge-on-write detector
    episodes.go               EpisodeStore with search + LLM ranking
    scan.go                   Security scan (invisible Unicode, injection, credentials)
    tool.go                   memory tool for the agent (6 actions)
    *_test.go                 95 tests across all subsystems
  skills/
    types.go                  Skill/skill manager types, DefaultSkillsConfig
    loader.go                 Skill loader + trie trigger index
    derive.go                 Keyword derivation from skill body
    trigger.go                Trigger matching
    selfimprove.go            5 heuristics + runAllHeuristics
    curator.go                Quality audit, staleness, overlap, dedup
    llm_enhance.go            LLM enrichment for learning + curation
    importer.go               URI import with LLM risk assessment
    tools.go                  Skill CRUD tools (list/view/save/patch/delete/load)
    *_test.go                 106 tests across all subsystems
cmd/kode/
  main.go                     CLI entry point, flag parsing, commands, sandbox
  main_test.go                CLI tests (flag parsing, version, init)
  shell.go                    Built-in shell tool (local or docker exec)
  shell_test.go               Shell + sandbox tests
  serve.go                    Web UI server (HTTP + WebSocket)
  subagent.go                 Sub-agent command (--goal, --context, --task, JSON stdout)
  subagent_tool.go            delegate_tasks built-in tool
  wsapprover.go               WSApprover — WebSocket-based approval for serve mode
  subagent_test.go            Tests (flag parsing, JSON stdout, exit codes, tool schema)
  subagent_contract_test.go   Contract tests (flag parsing, stdout protocol, exit codes)
  subagent_e2e_test.go        E2E tests (16 — KODE_E2E=true, real subprocess spawning)
  ui/
    index.html                Single-page web UI (~770 LOC, vanilla JS + CSS)
docs/                         Documentation
  CLI.md                      CLI reference
  CONFIG.md                   Configuration system
  PROVIDERS.md                Models, profiles, thinking, context
  SESSIONS.md                 Multi-turn sessions
  WEBUI.md                    Web UI server + WebSocket protocol + @ completion
  SUBAGENTS.md                Task decomposition + sub-agents + delegate_tasks tool
  SECURITY.md                 Prompt injection, security model
  SANDBOXING.md               Sandbox configuration
  MCP.md                      MCP server over stdio (Model Context Protocol)
  DEVELOPMENT.md              This file
```

## Testing

```bash
# All tests (excluding E2E — those need KODE_E2E=true)
go test ./... -count=1

# Specific package
go test ./internal/session/ -v

# With race detector
go test -race ./... -count=1

# E2E tests (builds kode binary, tests real subprocess spawning)
KODE_E2E=true go test -v -count=1 ./cmd/kode/ -run "TestE2E_"

# Contract tests (sub-agent interface contract — binary must already be built)
go test -v -count=1 ./cmd/kode/ -run "TestSubagent|TestDelegateTasks"
```

Zero external test dependencies — tests use `httptest`, `testing`, and the standard library only.

### Test layers

| Layer | Runner | Tests | What's tested |
|-------|--------|-------|---------------|
| **Unit** | `go test ./...` | 859+ | All 13 packages — config, LLM client, loop, sessions, renderer, tools, WS, resources, memory, skills, danger, security |
| **Contract** | `go test ./cmd/kode/` | 60+ | Sub-agent flag parsing, JSON stdout, exit codes, tool schema, config, serve, shell |
| **E2E** | `KODE_E2E=true go test -run 'TestE2E_'` | 16 | Real subprocess spawning, tool→binary pipeline, concurrency, timeouts, custom prompts |

### Test coverage

| Package | Tests | Focus |
|---------|-------|-------|
| `kode` | 54 | Config defaults, API key fallback, thinking passthrough, model profiles, AGENTS.md, Close lifecycle |
| `internal/config` | 19 | Config file loading, env vars, merge chain, var expansion |
| `internal/llm` | 40 | JSON marshaling, thinking fields, response parsing, usage statistics, SimpleCall |
| `internal/loop` | 31 | ReAct engine with httptest mock server, context budgeting, skill loader |
| `internal/session` | 22 | CRUD, trim, cleanup, list, latest, edge cases, path traversal protection |
| `internal/tool` | 7 | Registry CRUD, lookup, duplicate detection |
| `internal/ws` | 13 | WebSocket upgrade, framing, ping/pong, large messages |
| `internal/resource` | 24 | @-reference parsing, file resolution, session resolution, security |
| `internal/render` | 26 | Terminal output, no-color mode, nil safety, tool call/result rendering |
| `internal/danger` | 209 | Command classification (8 risk classes), config overrides, allow/denylist |
| `internal/memory` | 95 | Facts CRUD, buffer ring, episodes, merge detector (go-vector), memory tool, security scan, LLM ranking |
| `internal/skills` | 106 | Loading, triggers, self-improvement (5 heuristics), curation, LLM-enhanced generation, import, tools |
| `cmd/kode` | 213 | Flag parsing, init, version, sandbox setup, subagent, serve, security E2E, shell tool danger, browser tool, contract tests |

## Key packages

### Web UI (`cmd/kode/serve.go` + `internal/ws/ws.go` + `cmd/kode/ui/index.html`)

- **serve.go**: HTTP server with embedded WebSocket handler, `@` resource API, session list API
- **ws/ws.go**: Zero-dependency RFC 6455 WebSocket (~200 LOC). Handles upgrade, text frames, close, ping/pong
- **ui/index.html**: Single-file SPA, ~770 LOC vanilla JS + CSS. Streaming, collapsible tool blocks, `@` autocomplete, session sidebar

See [docs/WEBUI.md](docs/WEBUI.md) for the WebSocket protocol and full documentation.

### Sub-agents (`cmd/kode/subagent.go` + `cmd/kode/subagent_tool.go`)

- **subagent.go**: CLI handler for `kode subagent --goal <string>`. Parses flags, creates agent, runs with minimal system prompt, outputs JSON to stdout
- **subagent_tool.go**: `delegate_tasks` built-in tool. Spawns real OS processes via `exec.Command` with temp files for task data

See [docs/SUBAGENTS.md](docs/SUBAGENTS.md) for full documentation.

## Contributing

1. Fork and clone
2. Make changes
3. Run `go test ./...`
4. Open a PR

**Zero-dependency policy:** Contributions must not introduce external Go modules. stdlib only.
