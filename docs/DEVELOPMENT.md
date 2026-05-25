# Development

## Prerequisites

- Go 1.24+
- Docker (for sandbox integration tests only)

## Building

```bash
go build -o odek ./cmd/odek
```

## Source layout

```
odek.go                       Public API (Config, New, Run, Close)
`odek_test.go                  Config and model profile tests
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
    session_test.go           Session tests (42 tests, 89.7% coverage)
  render/
    render.go                 Terminal output with model label and color
  resource/
    resource.go               @-reference resolver (files, sessions)
    resource_test.go          Parse, resolve, search tests
  ws/
    ws.go                     RFC 6455 WebSocket framing (~200 LOC)
    ws_test.go                Handshake, framing, ping/pong tests
  transport/
    client.go                 Tuned HTTP transport with connection pooling
    client_test.go            Pool config tests (keep-alives, idle timeouts)
  tool/
    registry.go               Thread-safe tool registry
    registry_test.go          Registry tests
    clarify.go                Clarify tool — ask user questions with Answer function injection
    clarify_test.go           Clarify tool tests (11 tests, 100% coverage)
  danger/
    classifier.go             Command/URL classification for security gating
    classifier_test.go        209 tests, 8 risk classes, config overrides
    approver.go               Approver interface + TTYApprover (CLI /dev/tty)
  memory/
    memory.go                 MemoryManager orchestrator (facts, buffer, episodes)
    merge.go                  go-vector RP merge-on-write detector
    facts.go                  FactStore with caps, dedup, substring CRUD
    buffer.go                 Ring buffer for turn summaries
    episodes.go               EpisodeStore with search + LLM ranking
    scan.go                   Security scan (invisible Unicode, injection, credentials)
    tool.go                   memory tool for the agent (6 actions)
    *_test.go                 144 tests across all subsystems
  skills/
    types.go                  Skill/skill manager types, DefaultSkillsConfig, ValidateSkillName
    types_test.go             ValidateSkillName tests
    loader.go                 Skill loader + trie trigger index
    cache.go                  File mod-time cache + persistent disk cache (.skills_cache.json)
    cache_test.go             Cache tests
    derive.go                 Keyword derivation from skill body
    trigger.go                Trigger matching
    selfimprove.go            5 heuristics + runAllHeuristics
    curator.go                Quality audit, staleness, overlap, dedup
    llm_enhance.go            LLM enrichment for learning + curation
    importer.go               URI import with LLM risk assessment
    tools.go                  Skill CRUD tools (list/view/save/patch/delete/load)
    *_test.go                 127 tests across all subsystems (86.3% coverage)
  telegram/
    bot.go                    Telegram bot client (getFile, download, sendMessage, sendDocument)
    config.go                 Bot config from environment
    poller.go                 Long-polling update fetcher
    handler.go                Message dispatcher + command router
    commands.go               Command handlers (/start, /run, /plan, /sessions, etc.)
    session.go                Telegram session store (chat → odek session mapping)
    plan.go                   Plan management (Slugify, ListPlans, ReadPlan, DeletePlan, MostRecentPlan)
    download.go               Media download (voice, photo → file on disk)
    health.go                 HTTP health check endpoint (atomic.Bool ready state, 503→200)
    *_test.go                 473 tests across all subsystems (87.1% coverage)
cmd/odek/
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
  API.md                      Programmatic API (Go library)
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
# All tests (excluding E2E — those need ODEK_E2E=true)
go test ./... -count=1

# Specific package
go test ./internal/session/ -v

# With race detector
go test -race ./... -count=1

# E2E tests (builds odek binary, tests real subprocess spawning)
ODEK_E2E=true go test -v -count=1 ./cmd/odek/ -run "TestE2E_"

# MCP E2E tests (real MCP server subprocess + fakeserver compiled on-the-fly)
ODEK_E2E=true go test -v -count=1 ./cmd/odek/ -run "TestMCPClientE2E"

# Contract tests (sub-agent interface contract — binary must already be built)
go test -v -count=1 ./cmd/odek/ -run "TestSubagent|TestDelegateTasks"
```

Zero external test dependencies — tests use `httptest`, `testing`, and the standard library only.

### Test layers

| Layer | Runner | Tests | What's tested |
|-------|--------|-------|---------------|
| **Unit** | `go test ./...` | 1954 | All 17 packages — config, LLM client, loop, sessions, renderer, tools, WS, resources, memory, skills, telegram, danger, security, mcp |
| **Contract** | `go test ./cmd/odek/` | 60+ | Sub-agent flag parsing, JSON stdout, exit codes, tool schema, config, serve, shell |
| **E2E** | `ODEK_E2E=true go test -run 'TestE2E_'` | 16 | Real subprocess spawning, tool→binary pipeline, concurrency, timeouts, custom prompts |
| **MCP E2E** | `ODEK_E2E=true go test -run 'TestMCPClientE2E'` | 5 | MCP client with real fakeserver subprocess (compiled on-the-fly from testdata/main.go) |

### Test coverage

| Package | Tests | Focus |
|---------|-------|-------|
| `odek` | 62 | Config defaults, API key fallback, thinking passthrough, model profiles, AGENTS.md, Close lifecycle, token tracking, Memory() nil-safety |
| `internal/config` | 19 | Config file loading, env vars, merge chain, var expansion |
| `internal/llm` | 50 | JSON marshaling, thinking fields, response parsing, usage statistics, SimpleCall |
| `internal/loop` | 37 | ReAct engine with httptest mock server, context budgeting, skill loader |
| `internal/session` | 42 | CRUD, trim, cleanup, list, latest, fallback scan, corrupt data, path traversal protection, concurrent safety, atomic writes |
| `internal/tool` | 17 | Registry CRUD, duplicate detection, ClarifyTool (Name, Description, Schema, Call with all error paths) |
| `internal/ws` | 1 | WebSocket constant verification |
| `internal/resource` | 45 | @-reference parsing, file resolution, session resolution, security |
| `internal/render` | 62 | Terminal output, no-color mode, nil safety, tool call/result rendering |
| `internal/danger` | 281 | Command classification (8 risk classes), config overrides, allow/denylist |
| `internal/memory` | 144 | Facts CRUD, buffer ring, episodes, merge detector (go-vector), ReplaceEntry, AppendEntry, memory tool, security scan, LLM ranking |
| `internal/skills` | 127 | Loading, triggers, self-improvement (5 heuristics), curation, LLM-enhanced generation, import, tools, ValidateSkillName, isPrivateHost, extractRelevantChange |
| `internal/telegram` | 473 | Bot client, long-polling, command handlers, session management, plan CRUD, voice/photo download, health server, retry/backoff |
| `cmd/odek` | 441 | Flag parsing, init, version, sandbox setup, subagent, serve, security E2E, shell tool danger, browser tool, contract tests |

## Key packages

### Web UI (`cmd/odek/serve.go` + `internal/ws/ws.go` + `cmd/odek/ui/index.html`)

- **serve.go**: HTTP server with embedded WebSocket handler, `@` resource API, session list API
- **ws/ws.go**: Zero-dependency RFC 6455 WebSocket (~200 LOC). Handles upgrade, text frames, close, ping/pong
- **ui/index.html**: Single-file SPA, ~770 LOC vanilla JS + CSS. Streaming, collapsible tool blocks, `@` autocomplete, session sidebar

See [docs/WEBUI.md](docs/WEBUI.md) for the WebSocket protocol and full documentation.

### Sub-agents (`cmd/odek/subagent.go` + `cmd/odek/subagent_tool.go`)

- **subagent.go**: CLI handler for `odek subagent --goal <string>`. Parses flags, creates agent, runs with minimal system prompt, outputs JSON to stdout
- **subagent_tool.go**: `delegate_tasks` built-in tool. Spawns real OS processes via `exec.Command` with temp files for task data

See [docs/SUBAGENTS.md](docs/SUBAGENTS.md) for full documentation.

## Performance Architecture

### HTTP Connection Pooling (`internal/transport/`)

All API clients (LLM, Telegram) use `transport.NewPooledClient()` which creates an `*http.Client` with a tuned transport:

| Setting | Value | Purpose |
|---|---|---|
| `MaxIdleConns` | 20 | Pool up to 20 idle connections |
| `MaxIdleConnsPerHost` | 10 | 10 keep-alive connections per API host |
| `IdleConnTimeout` | 90s | Close idle connections after 90s of inactivity |
| `DisableCompression` | true | API responses (JSON) are already compact |
| `ForceAttemptHTTP2` | true | Prefer HTTP/2 multiplexing |
| `Dialer.KeepAlive` | 30s | TCP keep-alive interval |

Before pooling, every API call opened a new TCP + TLS connection (~200-500ms). With pooling, connections are reused across calls.

### Context Trimming (`internal/loop/loop.go`)

The `trimContext` function uses a **running token total** to avoid O(n²) behavior. Instead of calling `estimateMessages()` on the full message list after every group drop, it subtracts only the dropped group's tokens from a precomputed total. The fast path (no trim needed) is zero allocations, ~62ns.

### Skill Caching (`internal/skills/cache.go`)

Two cache layers:
1. **In-memory** `fileCache` — maps SKILL.md paths to their last-known mtime. Within a single process, unchanged files skip re-parsing.
2. **Persistent** `.skills_cache.json` — serializes the in-memory cache to `~/.odek/skills/` for reuse across process invocations. On the next `odek run`, skills are loaded from the cache instead of re-parsing 151 YAML frontmatters. Invalidated on format version bumps or explicit mutations.

### Episode Index Cache (`internal/memory/episodes.go`)

The episode `index.json` is cached in memory after the first read. Subsequent `Search()` calls (one per agent loop turn) hit the cache instead of re-reading + unmarshalling from disk. A `sync.RWMutex` allows concurrent readers. The cache is invalidated after writes (rare, ~once per session).

## Contributing

1. Fork and clone
2. Make changes
3. Run `go test ./...`
4. Open a PR

**Minimal dependencies policy:** Contributions should prefer stdlib and avoid unnecessary external Go modules.
