# v2.29.7 — Danger classifier correctness

This patch closes the confirmed approval bypasses and makes classification
retain independent command effects. A configured denial of egress, mutation,
installation or execution survives a compound command with an allowed class.

## Fixes

- Unknown programs retain their default denial beside redirects. Original
  executable identity survives normalization; custom-path executables and
  extensionless shell text participate in execution/provenance gates.
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

## Compatibility

No dependency or public protocol changes. Existing Classify callers retain a
single display class; command policy uses the new Analyze effects internally.
Build/test and Git helper operations can now prompt under existing default
policies. Review explicit operator policy when adopting the stricter labels;
reading a script does not itself authorize its execution. A contradictory
blocked-class allow/prompt setting is invalid and fails closed.

The classifier remains heuristic. It is not a complete shell interpreter or
an OS filesystem/network boundary; sandbox enforcement remains necessary for
containment of arbitrary approved programs.

## Validation

All ten confirmed marker bypasses are rejected before execution. Regression
checks retain independent denials for execution, egress, installation, local
and protected writes, persistence, destructive and unknown operations across
compound commands and pipelines. Protected script operands cannot hide the
execution effect; prompts name the permission actually requiring approval.

Measured statement coverage on macOS:

| Scope | Coverage |
|---|---:|
| New effect analysis (`analysis.go`) | 100.0% |
| New command adapters (`command_effects.go`) | 100.0% |
| Danger package, unit tests | 92.1% (previously 90.1%) |
| Danger package, combined integration/race coverage | 92.7% |
| Loop package, combined coverage | 93.6% |
| CLI package, combined coverage | 71.1% |

Passing checks: full short Go suite (37 packages), race detection across all
three changed packages, CLI/MCP end-to-end suites, golangci-lint, govulncheck,
and a 30-second policy fuzz soak (28,243 inputs in the final soak). The
fuzz-discovered escaped-whitespace case is retained as a corpus regression.
All four Linux/macOS amd64/arm64 release targets compile. Docker-only suites
were not run locally; GNU-specific sed/tar forms have classification coverage,
while execution rejection markers were verified on macOS.

An unrelated MCP oversized-response fixture returned an early EOF during a
heavily concurrent full run. Its package rerun and the subsequent full suite
with reduced package concurrency passed without changes to MCP production
code.
