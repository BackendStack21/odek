# v2.29.7 — Danger classifier correctness and OpenAI progress notes

This patch closes the confirmed approval bypasses and makes classification
retain independent command effects. A configured denial of egress, mutation,
installation or execution survives a compound command with an allowed class.
OpenAI assistant notes now appear before their tools in the terminal, Web UI
and Telegram, including buffered responses and streaming fallbacks.

## Fixes

- Unknown programs retain their default denial beside redirects. Original
  executable identity survives normalization; custom-path executables and
  extensionless shell text participate in execution/provenance gates.
  Non-UTF-8 shell comments do not bypass the unread-script gate.
- Static assignments, cwd changes and env directory wrappers inform target
  and helper resolution. Uncertain writes and excessive expansion fail closed.
- Awk whitespace calls, addressed/grouped sed execution and writes, SQLite
  indirect scripts/extensions/outputs, rg/fd helpers, tar execution flags,
  toolchain plugins and Node syntax checks with preloads are gated.
- Curl/wget destinations, attached and combined output flags, default output
  names and output directories retain protected-write effects.
- Git hooks, filters, filesystem monitors and diff helpers retain execution
  effects. Project builds/tests execute code and now classify accordingly.
- Batch approvals show every command requiring approval. Pre-dispatch checks
  reject changed effects even when the display class stays the same. Blocked
  operations cannot be overridden by class settings or command allowlists.
- Inspection false positives are corrected for npm/jq scripts reads,
  /etc/passwd, sed/archive listing and lexical path-prefix lookalikes.
  Formatters, signals, compiler outputs and container mutations retain writes.
- Linux stdio aliases remain local writes even when the runner redirects them
  through procfs. Symlink traversal is resolved before `..` components, so
  lexical cleaning cannot disguise a protected path as a stdio alias.
- OpenAI pre-tool assistant notes are delivered as visible text separately
  from reasoning summaries. Engaging terminal mode preserves the actual note.
  Web UI and Telegram progress callbacks receive notes before tool events;
  headless REST event histories retain them without prefixing final results.
- Stream delivery is tracked per iteration and per text kind. Earlier deltas
  cannot hide later buffered notes, final replies or iteration-budget summaries.
- SDK v0.6.1 consumes a successful JSON response to a streaming request
  directly, preserving that generation's notes, reasoning, tool calls,
  signatures and usage without issuing a replacement generation.

## Compatibility

The go-llm-sdk dependency is updated from v0.6.0 to v0.6.1. Existing WebSocket
event types are reused. `IterationInfo` adds `Content`, `StreamedContent` and
`StreamedReasoning` for programmatic progress consumers. Existing Classify callers retain a
single display class; command policy uses the new Analyze effects internally.
Build/test and Git helper operations can now prompt under existing default
policies. Review explicit operator policy when adopting the stricter labels;
reading a script does not itself authorize its execution. A contradictory
blocked-class allow/prompt setting is invalid and fails closed.

The classifier remains heuristic. It is not a complete shell interpreter or
an OS filesystem/network boundary; sandbox enforcement remains necessary for
containment of arbitrary approved programs.

## Validation

All reproduced marker bypasses are rejected before execution. Regression
checks retain independent denials for execution, egress, installation, local
and protected writes, persistence, destructive and unknown operations across
compound commands and pipelines. Protected script operands cannot hide the
execution effect; prompts name the permission actually requiring approval.
OpenAI Chat Completions and Responses fixtures verify notes before tools and
exactly-once output across buffered, streamed and mixed delivery. Terminal
tests cover engaging mode, reasoning summaries and buffered budget summaries.
REST tests verify the final-result and approval-bridge contracts. Chinese-model
reasoning streaming remains covered by the existing provider regressions.

Measured statement coverage on macOS:

| Scope | Coverage |
|---|---:|
| New effect analysis (`analysis.go`) | 100.0% |
| New command adapters (`command_effects.go`) | 100.0% |
| Danger package, unit tests | 92.2% (previously 90.1%) |
| Danger package, combined integration/race coverage | 92.7% |
| New progress delivery / deduplication helpers | 100.0% |
| New SDK JSON-fallback branches | 100.0% |
| Loop package, combined coverage | 93.7% |
| Renderer package, combined coverage | 82.0% |
| CLI package, combined coverage | 71.2% |

Passing checks: full short Go suite (37 packages), race detection across all
four affected packages, CLI/MCP end-to-end suites, 249 Web UI tests,
golangci-lint, govulncheck,
and a 30-second policy fuzz soak (14,313 inputs in the final soak). The
fuzz-discovered escaped-whitespace case is retained as a corpus regression.
The SDK patch passes Linux/macOS race tests, Windows build/vet, lint and
CodeQL before publication. All four Linux/macOS amd64/arm64 release targets compile. Docker-only suites
were not run locally; GNU-specific sed/tar forms have classification coverage,
while execution rejection markers were verified on macOS.

An unrelated MCP oversized-response fixture returned an early EOF during a
heavily concurrent full run. Its package rerun and the subsequent full suite
with reduced package concurrency passed without changes to MCP production
code.
