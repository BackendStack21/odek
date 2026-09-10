# Web UI reference client coverage

Branch: `codex/feat-webui-reference-client`.

The implementation keeps the embedded, offline-capable client and native ES
modules. It adds no frontend build step or runtime framework dependency.

| Improvement | Implemented behavior |
| --- | --- |
| Trustworthy execution | Correlated call IDs, explicit outcomes, persisted historical outcomes, read/write receipt distinction, Go test package recognition. |
| Workspace design | Bundled Manrope typography, comfortable/compact density, collapsible desktop session rail (hidden by default), resizable inspector, consistent SVG icons, mobile two-row header, dark/light/high-contrast themes and reduced motion. |
| Tool visualization | Shared code, unified/aligned split diff, terminal, search references, source links, HTTP status, expandable JSON and text views; tool-specific plan steps, parallel command outcomes, batch file/edit groups and semantic headers; bounded pages, search, raw, copy and save. |
| Result navigation | Session result collection, inspect/jump controls, temporal plan-step links, agent trace links and searchable background-job output. |
| Session recovery | Tab-scoped text drafts, in-memory attachment drafts, session-owned prompt queues, explicit resume after interruption, late-upload session guards. |
| Runs and observability | Background run creation, full refreshable details, session navigation, typed approval friction, event search/session filtering and authoritative budget snapshots. |
| Tools, skills and memory | Full built-in descriptions/schemas, skill body/provenance review before promotion, memory consolidation preview with stale-snapshot rejection. |
| Operator controls | Additive capabilities endpoint, shared-store schedule CRUD and next-fire previews, retention policy display and confirmed cleanup report. |
| Artifacts and media | Session-authenticated immutable preview/download cache, MIME-checked media uploads inside the workspace, raster/audio/PDF previews and download-only fallback. |
| Quality infrastructure | Deterministic local browser fixture, production-mux Go tests, renderer/lifecycle regressions and documented visual review procedure. |

## Validation

- 217 JavaScript tests passed.
- Changed-package Go suites passed: loop, session, artifact, memory, MCP client,
  and CLI/server.
- Race checks passed for those packages. The final upload-path change has an
  additional focused race regression run.
- `go vet` passed for changed packages and the public API.
- Browser review covered 1440×1000, 1024×768 and 390×844; light, dark and high
  contrast; populated conversations; inline/detail split diffs; management forms;
  accessible control names and layout bounds. No browser console errors were
  observed. Desktop, tablet and mobile page widths matched their viewport widths.
- The fixture does not make provider calls or mutate real user state. Browser
  review is manual through the browser control surface, not an automated visual
  baseline suite.

## Explicit limits

- Schedules require a running scheduler daemon or Telegram host; the Web UI
  manages definitions and does not spawn a second executor.
- Preview cache is process-local: 10 MiB/file, 40 MiB total, 128 entries. Save
  artifacts that must survive eviction or restart.
- Uploaded project files live in `.odek-artifacts/uploads/`; home-directory
  retention cleanup does not sweep them. Media interpretation requires configured
  vision/transcription tools. This change does not add native multimodal provider
  message parts.
- Text drafts survive tab reloads; attachment drafts survive session switches
  only. Plan result links express timing, not proof of completion.
- Historical tool output remains subject to the runtime's original persistence
  truncation. Unknown legacy outcomes remain unknown.
- Real provider, Docker media execution, and every third-party MCP server are
  integration environments beyond the deterministic fixture. Pixel review covered
  the listed viewports and states, not every browser/device combination.

See [WEBUI.md](WEBUI.md) for API contracts and the repeatable review procedure.

Plan tool views cover creation and updates, separating requested changes from returned step states. Turn cancellation immediately settles pending tool and delegated-agent indicators, preserves completed outcomes, ignores late turn frames, and pauses queued prompts. Sessions start collapsed and can be opened with the Sessions button or command.
