# v2.29.0 — Task supervision WebUI

This cumulative feature release makes the WebUI a task workspace with clear
execution state, decisions, recovery and completion evidence.

## Changes

- Task Overview, Deliverables and Activity replace a flat management drawer.
  Settings separates preferences, knowledge, diagnostics and maintenance;
  scheduled work has its own entry point. The palette shares workspace
  destinations and removes duplicate commands and clear-transcript.
- Run settings in the top bar exposes workspace, sandbox and permission policy;
  the conversation has no extra execution-context row.
  Run settings group model, reasoning depth and tighter per-run runtime,
  tool-call, token and cost caps. Browser caps cannot raise operator limits.
- Pointer Send/Queue, ordinary multiline editing, IME-safe submission,
  history draft restoration and context browsing improve the composer.
- Stop acknowledgement means Stopping; terminal settlement means Stopped.
  Recovery reloads saved context under execution ownership and rejects stale
  revision/generation checkpoints. Preparing a rerun requires prompt review.
- Approval and question cards wait for server acceptance. Durable, redacted
  decision receipts remain in sessions; grants name their actual scope and
  can be revoked. Questions support multiline answers, Skip and Stop.
- Evidence distinguishes recorded file edits, passing/failed/unverified checks,
  unknown tool outcomes and immutable artifact captures with turn provenance.
  Exact-command reruns retain their earlier outcomes; a narrow passing check
  cannot erase a different failed suite. Plan completion remains agent-reported.
- Delegated work starts queued; partial, budget-limited, failed, stopped and
  unknown results stay distinct and offer a review/continue action.
- Mobile drawers isolate and trap focus, restore their opener and close with
  Escape. Reading size is adjustable and async completion preserves focus.
- Schedule presets, explicit timezone, next-three-fire preview, paused states,
  last results and recent execution-host heartbeat make schedules reviewable.

- Internal maintenance events stay silent. Memory requiring review links to
  Knowledge, and a disconnected connection offers Reconnect now.

## Compatibility and limits

Existing sessions and external protocol clients remain supported through
additive JSON fields. Decision receipts are bounded to 128 per session and do
not enter model context. Raw activity stays bounded to 300 entries / 16 MiB;
artifact previews remain a process-local cache cleared at server restart.

The new `turn_settled`, `permissions` and recovery contracts are documented in
[WEBUI.md](WEBUI.md). Cost enforcement still requires operator-configured model
prices. Scheduler health is a recent liveness observation, not a guarantee that
a future task will run or succeed. Stopping an agent does not undo side effects
or stop independently running background jobs.

## Validation

Production HTTP/WebSocket integration tests cover acknowledged receipts,
settlement after ownership release, stale recovery rejection and per-run caps.
JavaScript regressions cover decision queues, multiline questions, IME,
cancellation lifecycle and aggregated evidence. Browser validation uses the
production mux with a local mock provider and isolated session storage:

```sh
WEBUI_BROWSER_STOP_FILE=/tmp/odek-browser.stop \
  go test -v -count=1 -timeout 15m ./cmd/odek -run '^TestWebUIBrowserHarness$'
# Open the printed token URL; write the stop file after validation.
```

The browser harness exercises UI behavior without paid provider calls. Release
binaries remain built by the existing tag-driven workflow after this change is
merged and tagged.
