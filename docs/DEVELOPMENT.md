# Development

## Generate Changelog

CHANGELOG.md is auto-generated from conventional git commits. **Do not edit manually** — edits will be overwritten.

```bash
# Generate changelog for the next patch release (stdout)
./generate-changelog.sh --bump patch

# Prepend to CHANGELOG.md
./generate-changelog.sh --bump patch --prepend

# Release notes for gh release create
./generate-changelog.sh --bump patch --notes > /tmp/notes.md
gh release create v0.59.0 --notes-file /tmp/notes.md

# Explicit range
./generate-changelog.sh --from v0.57.0 --to v0.58.0
```

Commit prefixes and their section mapping:
- `feat|feature:` → ### Features
- `fix|bugfix:` → ### Bug Fixes
- `perf:` → ### Performance
- `refactor:` → ### Refactoring
- `docs:` → ### Documentation
- `test|tests:` → ### Testing
- `chore|build|ci:` → ### Infrastructure
- (unmatched) → ### Other Changes

## Prerequisites

- Go 1.25+ (matches `go.mod`; CI builds with the same toolchain)
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
    session_test.go           Session tests
    audit.go                  Per-session prompt-injection audit log (ingests, divergence)
    audit_test.go             Audit store tests
  render/
    render.go                 Terminal output with model label and color
  resource/
    resource.go               @-reference resolver (files, sessions)
    resource_test.go          Parse, resolve, search tests
  ws/
    ws.go                     RFC 6455 WebSocket framing (compact, stdlib-only)
    ws_test.go                Handshake, framing, ping/pong tests
  transport/
    client.go                 Tuned HTTP transport with connection pooling
    client_test.go            Pool config tests (keep-alives, idle timeouts)
  tool/
    registry.go               Thread-safe tool registry
    registry_test.go          Registry tests
    clarify.go                Clarify tool — ask user questions with Answer function injection
    clarify_test.go           Clarify tool tests
  sandbox/
    sandbox.go                Docker container lifecycle (image resolve, run args, file injection)
    sandbox_test.go           Sandbox tests (BuildRunArgs, ResolveImage, InjectFiles)
  danger/
    classifier.go             Command/URL classification for security gating
    classifier_test.go        Risk classification, 8 classes, config overrides
    approver.go               Approver interface + TTYApprover (CLI /dev/tty)
  memory/
    memory.go                 MemoryManager orchestrator (facts, buffer, episodes)
    merge.go                  go-vector RP merge-on-write detector
    facts.go                  FactStore with caps, dedup, substring CRUD
    buffer.go                 Ring buffer for turn summaries
    episodes.go               EpisodeStore with search + LLM ranking
    scan.go                   Security scan (invisible Unicode, injection, credentials)
    tool.go                   memory tool for the agent (6 actions)
    provenance.go             Episode trust-signal derivation (untrusted-source taint)
    *_test.go                 Tests across all subsystems
  skills/
    types.go                  Skill/skill manager types, DefaultSkillsConfig, ValidateSkillName
    types_test.go             ValidateSkillName tests
    loader.go                 Skill loader + trie trigger index
    cache.go                  File mod-time cache + persistent disk cache (.skills_cache.json)
    cache_test.go             Cache tests
    derive.go                 Keyword derivation from skill body
    trigger.go                Trigger matching
    selfimprove.go            Heuristics + runAllHeuristics + AutoSaveSuggestions
    learnloop.go              AnalyzeMessages + RunAutoSaveLoop (non-interactive end-of-session pipeline)
    curator.go                Quality audit, staleness, overlap, dedup
    llm_enhance.go            LLM enrichment for learning + curation
    importer.go               URI import with LLM risk assessment
    tools.go                  Skill CRUD tools (list/view/save/patch/delete/load)
    *_test.go                 Tests across all subsystems
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
    *_test.go                 Tests across all subsystems
cmd/odek/
  main.go                     Process entry, flag parsing, run/repl handlers
  main_test.go                CLI flag-parsing + lifecycle tests
  dispatch.go                 CLI subcommand dispatch table + exit-code translators
  dispatch_test.go            Routing tests (unknown command, version, exit codes)
  shell.go                    Built-in shell tool (local or docker exec)
  shell_test.go               Shell tests (sandbox tests live in internal/sandbox)
  audit.go                    `odek audit` CLI handler + per-turn audit recording
  audit_test.go               Audit handler + recording tests
  skill_promote.go            `odek skill promote` — clear NeedsReview on tainted skills
  untrusted.go                <untrusted_content> wrapper + ingest recorder + MCP tool wrapper
  serve.go                    Web UI server (HTTP + WebSocket)
  subagent.go                 Sub-agent command (--goal, --context, --task, JSON stdout)
  subagent_key.go             POSIX FD-based API key handoff (avoid /proc env leak)
  subagent_tool.go            delegate_tasks built-in tool
  wsapprover.go               WSApprover — WebSocket-based approval for serve mode
  subagent_test.go            Sub-agent flag-parsing + JSON-stdout tests
  subagent_contract_test.go   Contract tests (flag parsing, stdout protocol, exit codes)
  subagent_e2e_test.go        E2E sandbox + subprocess tests (ODEK_E2E=true)
  race_on_test.go / race_off_test.go  Build-tag-gated `raceEnabled` const for race-sensitive tests
  ui/
    index.html                Single-page web UI (vanilla JS + CSS)
    app.js, style.css         Extracted JS / CSS
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

| Layer | Runner | What's tested |
|-------|--------|---------------|
| **Unit** | `go test ./...` | Every package — config, LLM client, loop, sessions, renderer, tools, WS, resources, sandbox, memory, skills, telegram, danger, mcp |
| **Race** | `go test -race ./...` | Same suite under the race detector. Race-sensitive tests are gated on the `raceEnabled` build-tag constant so they fail loudly under default builds but skip cleanly under `-race`. |
| **Contract** | `go test ./cmd/odek/` | Sub-agent flag parsing, JSON stdout, exit codes, tool schema, config, serve, shell, audit, skill promote |
| **E2E** | `ODEK_E2E=1 go test -run 'TestE2E_'` | Real subprocess spawning, tool→binary pipeline, concurrency, timeouts, custom prompts, sandbox file injection (incl. nested paths) |
| **MCP E2E** | `ODEK_E2E=1 go test -run 'TestMCPClientE2E'` | MCP client against a real `fakeserver` subprocess compiled on-the-fly from `testdata/main.go` |

CI (`.github/workflows/test.yml`) runs the unit suite under `-race` on every push and PR. The E2E suite is opt-in locally — enable it before merging changes that touch the sandbox, subagent, or MCP client paths.

### What each package covers

| Package | Focus |
|---------|-------|
| `odek` | Config defaults, API key fallback, thinking passthrough, model profiles, AGENTS.md, Close lifecycle, token tracking, Memory() nil-safety |
| `internal/config` | Config file loading, env vars, merge chain, variable expansion |
| `internal/llm` | JSON marshaling, thinking fields, response parsing, usage statistics, SimpleCall, retry/backoff |
| `internal/loop` | ReAct engine with httptest mock server, context budgeting, skill loader |
| `internal/session` | Session CRUD, trim, cleanup, list, latest, fallback scan, corrupt data, path-traversal protection, concurrent safety, atomic writes, audit log roundtrip |
| `internal/sandbox` | Image resolution, `docker run` argument construction (security defaults, forbidden-mount filtering), nested-path file injection, build-from-Dockerfile caching |
| `internal/tool` | Registry CRUD, duplicate detection, ClarifyTool full surface |
| `internal/ws` | WebSocket constant verification |
| `internal/resource` | @-reference parsing, file resolution, session resolution, security |
| `internal/render` | Terminal output, no-color mode, nil safety, tool call/result rendering |
| `internal/danger` | Command classification across 9 risk classes (incl. fail-closed `unknown`), config overrides, allow/denylist, classifier-bypass attempts, approver friction |
| `internal/memory` | Facts CRUD, buffer ring, episodes, merge detector (go-vector), ReplaceEntry/AppendEntry, memory tool, security scan, LLM ranking, episode provenance |
| `internal/skills` | Loading, triggers, self-improvement heuristics, curation, LLM-enhanced generation, import, tools, AnalyzeMessages/RunAutoSaveLoop, ValidateSkillName, isPrivateHost |
| `internal/telegram` | Bot client, long-polling, command handlers, session management, plan CRUD, voice/photo download, health server, retry/backoff |
| `cmd/odek` | Flag parsing, init, version, dispatch table, sandbox setup wiring, subagent, serve, security E2E, shell-tool danger, browser tool, audit CLI, skill promote, untrusted-tool wrapper |

## Key packages

### Web UI (`cmd/odek/serve.go` + `internal/ws/ws.go` + `cmd/odek/ui/index.html`)

- **serve.go**: HTTP server with embedded WebSocket handler, `@` resource API, session list API
- **ws/ws.go**: Zero-dependency RFC 6455 WebSocket. Handles upgrade, text frames, close, ping/pong
- **ui/index.html + app.js + style.css**: Vanilla JS + CSS SPA. Streaming, collapsible tool blocks, `@` autocomplete, session sidebar

See [docs/WEBUI.md](docs/WEBUI.md) for the WebSocket protocol and full documentation.

### Sub-agents (`cmd/odek/subagent.go` + `cmd/odek/subagent_tool.go` + `cmd/odek/subagent_key.go`)

- **subagent.go**: CLI handler for `odek subagent --goal <string>`. Parses flags, creates agent, runs with minimal system prompt, outputs JSON to stdout (includes `parent_session` when `--parent-session` was passed)
- **subagent_tool.go**: `delegate_tasks` built-in tool. Spawns real OS processes via `exec.Command` with temp files for task data
- **subagent_key.go**: API key handoff to the spawned child via an unlinked-tempfile FD passed through `ExtraFiles`, so the secret never appears in the child's `/proc/<pid>/environ`

See [docs/SUBAGENTS.md](docs/SUBAGENTS.md) for full documentation.

### Sandbox (`internal/sandbox/` + `cmd/odek/main.go::setupSandbox`)

- **sandbox/sandbox.go**: container lifecycle inputs — image resolution (explicit / `Dockerfile.odek` / `alpine:latest`), `docker run` argument construction with mandatory hardening (`--cap-drop ALL`, `--security-opt no-new-privileges`, `--tmpfs /tmp:noexec`), and `InjectFiles` (preserves nested paths via in-container `mkdir -p`)
- **cmd/odek/main.go::setupSandbox**: wires the resolved container into `*shellTool` / `*parallelShellTool` — kept in `cmd/odek` because the sandbox package must not know about agent-tool internals

See [docs/SANDBOXING.md](docs/SANDBOXING.md) for the user-facing security model.

### Skill learning loop (`internal/skills/learnloop.go` + `cmd/odek/main.go::runLearnLoop`)

- **skills/learnloop.go**: non-interactive pipeline — `AnalyzeMessages` converts a conversation into suggestions (heuristics + LLM enhancement + provenance), `RunAutoSaveLoop` filters against the skip list, persists eligible suggestions, fires notifier events, and triggers post-save micro-curation
- **cmd/odek/main.go::runLearnLoop**: orchestration only — calls `AnalyzeMessages` → `FilterSkipped` → tries `RunAutoSaveLoop`; falls back to `interactiveSavePrompt` (the only TTY-coupled piece) when auto-save is disabled

See [docs/LEARNING.md](docs/LEARNING.md) for the user-facing skill model.

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
