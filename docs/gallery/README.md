# Web UI gallery

These 1280 × 720 screenshots show the real bundled Web UI using synthetic
sessions from `scripts/webui/fixture-server.mjs`, captured at revision `a78f94dc`.
They contain no provider credentials or private session data. The fixture's
example results are presentation data, not evidence of an actual agent run.

To refresh, run `node scripts/webui/fixture-server.mjs` and open the printed URL
at 1280 × 720 in dark mode, with the session rail open. Capture:

- `results.png` — open “Build a better workspace”, open the inspector,
  Deliverables → Inspect all activity → the `diff` result.
- `plan.png` — the same session with the inspector on Overview.
- `approval.png` — in a new session, send a prompt containing
  “upload the test report”; the fixture raises a `network_upload` approval.
- `agents.png` — in a new session, send a prompt containing “in parallel”;
  expand the `delegate_tasks` group, open the inspector, and scroll to Agents.
- `workspace.png` — a new session with a task typed in the composer.
- `commands.png` — “Build a better workspace” with the palette open (⌘K).

Hide the transient toast before capturing. Keep image filenames and dimensions
consistent with the landing page, and update this revision when refreshing.
