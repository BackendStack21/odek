# odek — Agent Maintenance Guide

This file is automatically loaded by odek when running inside this repository.
It provides context about the project's architecture, conventions, and how to update/maintain it.

---

## Project Identity

- **Package:** `odek` (Go module: `github.com/BackendStack21/kode`)
- **What it is:** Minimal Go autonomous agent runtime — ReAct (Reasoning + Acting) loop with minimal external dependencies (stdlib + a few focused packages).
- **Binary:** `odek` — single static binary, ~11 MB, instant startup.
- **Config:** Layered priority: `~/.odek/config.json` → `./odek.json` → `ODEK_*` env vars → CLI flags.

## Source Layout

```
odek.go                       Public API (Config, New, Run, Close, ModelProfile, KnownProfiles)
odek_test.go                  Tests for public API
cmd/odek/
  main.go                     CLI entry point, flag parsing, commands, sandbox
  shell.go                    Built-in shell tool (local or docker exec)
  serve.go                    Web UI server (HTTP + WebSocket)
  subagent.go                 Sub-agent command (--goal, --context, --task)
  subagent_tool.go            delegate_tasks built-in tool
  ui/index.html               Single-page Web UI (~770 LOC, vanilla JS + CSS)
  *_test.go                   CLI, subagent, contract, and E2E tests
internal/
  config/loader.go            Config file loading, env vars, priority merge
  llm/client.go               OpenAI-compatible HTTP client
  loop/loop.go                ReAct engine (observe → think → act → repeat)
  session/session.go          Session store (CRUD, trim, cleanup)
  render/render.go            Terminal output with model label and color
  resource/resource.go        @-reference resolver (files, sessions)
  ws/ws.go                    RFC 6455 WebSocket framing (~200 LOC)
  tool/registry.go            Thread-safe tool registry, clarify.go
  danger/classifier.go        Command/URL classification for security gating
  memory/                     MemoryManager (facts, buffer, episodes, merge, scan)
  skills/                     Skill system (types, loader, triggers, self-improve, curator, import)
  telegram/                   Telegram bot client, poller, handler, commands
docs/                         Documentation (CLI, API, CONFIG, MCP, MEMORY, etc.)
```

## How It Works

1. **Agent Loop (`internal/loop/loop.go`):** ReAct cycle: observe → think → act → repeat.
   - LLM returns a tool call or final answer.
   - Tools execute and return results.
   - Max 90 iterations by default (`--max-iter`).
2. **Tools:** Built-in: `read_file`, `write_file`, `search_files`, `patch`, `shell`, `browser`, `memory`, `clarify`, `delegate_tasks`. All gated by the `danger` security classifier.
3. **Skills:** Trigger-matched `SKILL.md` files loaded on-demand. Auto-learns from patterns every session. Stored in `~/.odek/skills/` and `./.odek/skills/`.
4. **Memory:** Three tiers — facts (durable entries), session buffer (turn summaries), episodes (LLM-extracted knowledge). Uses go-vector RandomProjections for merge-on-write.
5. **Sub-agents:** `delegate_tasks` spawns real OS subprocesses via `odek subagent`. Each gets a fresh agent process with its own config.

## Key Conventions

- **Minimal dependencies policy:** Prefer stdlib, keep dependencies minimal and focused. All contributions must maintain this.
- **Tests:** 1760+ tests, run with `go test ./...` (no network, no Docker needed for unit tests).
- **Error handling:** Return errors, don't panic. Fatal errors go through `fmt.Fprintf(os.Stderr, ...)`.
- **Config structs:** JSON tags for serialization. `*bool` for optional tristate fields (nil = not set).
- **Security:** Symlinked AGENTS.md is refused. Shell commands are classified into 8 risk classes. Sandbox mode uses Docker with no network and read-only mounts.
- **Model profiles:** Added in `odek.go`'s `KnownProfiles` var. Longest-prefix matching.
- **Parallel tool execution:** When the LLM returns multiple `tool_calls` in one response, they execute concurrently via goroutines with a channel semaphore (default cap: 4). Results are collected in original call order.
- **Batch approval gate:** When an approver is set and > 1 tool calls per iteration, a single batch approval prompt fires before Phase 2. If approved, `SetTrustAll(true)` is called on the approver to bypass individual tool-level prompts.

## How to Update/Maintain This Project

### Building

```bash
go build -o bin/odek ./cmd/odek     # build binary
go build ./...                       # check compilation
```

### Testing

```bash
go test ./... -count=1               # all unit tests
go test -race ./... -count=1         # with race detector
go test -v ./internal/session/       # specific package
KODE_E2E=true go test ./cmd/odek/ -run "TestE2E_"  # E2E tests
```

### Adding a New Model Profile

Edit `odek.go`, add to `KnownProfiles`:
```go
{
    Prefix: "my-model",
    Profile: ModelProfile{
        Label:           "My Model",
        DefaultThinking: "enabled",
        Timeout:         120,
        MaxContext:      128_000,
    },
},
```

### Adding a New Tool

1. Create the tool struct in `cmd/odek/` or `internal/tool/`.
2. Implement `Name()`, `Description()`, `Schema()`, `Call(args string) (string, error)`.
3. Register in `registerBuiltinTools()` in `cmd/odek/main.go`.
4. Add security classification in `internal/danger/classifier.go` if it runs shell commands.
5. Add tests.

### Adding a New Command/Flag

1. Define the flag in `main.go`'s `flags` struct.
2. Parse in `parseFlags()`.
3. Wire through `resolveConfig()` into the resolved config.
4. Pass to `odek.New()` or the relevant handler.
5. Document in `docs/CLI.md` and `docs/CONFIG.md`.
6. Add env var support in `internal/config/loader.go`.

### Documentation

- All docs are Markdown in `/docs/`.
- `docs/CLI.md` — CLI reference (commands, flags, error codes).
- `docs/API.md` — Programmatic API with Go examples.
- `docs/CONFIG.md` — Configuration system.
- `docs/DEVELOPMENT.md` — Building, testing, contributing.
- Update docs when adding/changing features.

### Skill System Maintenance

- Skills live in `~/.odek/skills/` and `./.odek/skills/` as `SKILL.md` files with YAML frontmatter.
- Learning is on by default (`--no-learn` to disable).
- Use `odek skill curate` to audit quality/overlap.
- Use `odek skill import <uri>` to import from URLs.

### Memory System

- Facts: `~/.odek/memory/user.md` and `~/.odek/memory/env.md`.
- Episodes: Stored as JSON files in `~/.odek/memory/episodes/`.
- Buffer: Ring buffer of turn summaries, stored in `~/.odek/memory/buffer.json`.
- Use the `memory` tool (6 actions: read, add, replace, remove, consolidate, search).

### Security Considerations

- **Prompt injection:** Identity anchoring anchors agent identity at the start of the system prompt. Anti-injection rules applied on top. AGENTS.md comes after identity.
- **Symlink protection:** `LoadProjectFile()` refuses symlinks.
- **Sandbox:** `--sandbox` flag runs in Docker with no network, no host mounts beyond working dir, zero capabilities.
- **Danger classifier:** Commands classified into 8 risk classes. Configurable via `dangerous` config section.

### Release Process

1. Update version tag: `git tag v0.x.y`
2. Build cross-platform: `make build-all`
3. Run full test suite: `make test-all`
4. Push tag: `git push origin v0.x.y`

## Common Gotchas

- When modifying `Config` struct in `odek.go`, also update `internal/config/loader.go` (CLI flags, env vars, JSON serialization).
- When adding to KnownProfiles, ensure tests in `odek_test.go` cover the new profile.
- The Web UI (`cmd/odek/ui/index.html`) is embedded — rebuild the binary to see UI changes.
- Session files are JSON in `~/.odek/sessions/` — corrupt data is handled gracefully with fallback scan.
- The Telegram bot uses long-polling (no webhooks), built on stdlib `net/http`.
- `delegate_tasks` sub-agents have a 120s default timeout. Override via `subagent.timeout_seconds` config.
- **Parallel tool tests** use `timedTool` with configurable delays — always run with `-race` to catch data races on the results slice.
- **Batch approval gate** (`Phase 1.5`) checks `e.approver != nil && len(result.ToolCalls) > 1` — single-tool responses skip the gate. When adding a new approver implementation, implement `SetTrustAll(bool)` to benefit from batch trust cascade.
- **SetTrustAll** is bounded by `defer` — it's enabled before Phase 2 and disabled when `runLoop` returns. The mockApprover's `trustAll` field will be `false` after `Run()` returns because the deferred `SetTrustAll(false)` runs first.
