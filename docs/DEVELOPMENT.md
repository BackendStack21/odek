# Development

## Prerequisites

- Go 1.24+
- Docker (for sandbox integration tests only)

## Building

```bash
go build -o kode ./cmd/kode
```

## Testing

```bash
# All tests
go test ./... -v -count=1

# Specific package
go test ./internal/session/ -v
```

Zero external test dependencies — tests use `httptest`, `testing`, and the standard library only.

### Test coverage

| Package | Tests | Focus |
|---------|-------|-------|
| `kode` | 31 | Config defaults, API key fallback, thinking passthrough, model profiles, AGENTS.md |
| `internal/config` | 17 | Config file loading, env vars, merge chain, var expansion |
| `internal/llm` | 18 | JSON marshaling, thinking fields, response parsing, usage statistics |
| `internal/loop` | 7 | ReAct engine with httptest mock server |
| `internal/session` | 19 | CRUD, trim, cleanup, list, latest, edge cases |
| `internal/tool` | 7 | Registry CRUD, lookup, duplicate detection |
| `cmd/kode` | 20+ | Flag parsing, init, version, sandbox setup, integration |

## Source layout

```
kode.go               Public API (Config, New, Run, Close)
kode_test.go          Config and model profile tests
internal/
  config/
    loader.go         Config file loading, env vars, priority merge
    loader_test.go    Config loading tests
  llm/
    client.go         OpenAI-compatible HTTP client
    client_test.go    JSON marshaling + response parsing tests
  loop/
    loop.go           ReAct engine (observe → think → act → repeat)
    loop_test.go      Engine tests with mock server
  session/
    session.go        Session store (CRUD, trim, cleanup)
    session_test.go   Session tests
  render/
    render.go         Terminal output with model label and color
  tool/
    registry.go       Thread-safe tool registry
    registry_test.go  Registry tests
cmd/kode/
  main.go             CLI entry point, flag parsing, commands, sandbox
  main_test.go        CLI tests
  shell.go            Built-in shell tool (local or docker exec)
  shell_test.go       Shell + sandbox tests
docs/                 Documentation
  CLI.md              CLI reference
  CONFIG.md           Configuration system
  PROVIDERS.md        Models, profiles, thinking, context
  SESSIONS.md         Multi-turn sessions
  SECURITY.md         Prompt injection, security model
  SANDBOXING.md       Sandbox configuration
  DEVELOPMENT.md      This file
```

## Contributing

1. Fork and clone
2. Make changes
3. Run `go test ./...`
4. Open a PR

**Zero-dependency policy:** Contributions must not introduce external Go modules. stdlib only.
