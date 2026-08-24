// Plan panel: read-only view of the current session's structured task plan
// (docs/PLANNING.md, "Surface Integration" → serve/WebUI). Data comes from
// GET /api/sessions/{id}/plan, which parses the persisted "[Current plan:"
// message with the same strict resume parser the engine uses — what renders
// here is exactly what the model steers by. Strictly read-only: this module
// has zero mutation controls by contract.
//
// ── Why polling, not WS push ──
// plan_created / plan_updated ride the odek.event/v1 stream into serve.go's
// EventHandler, which only appends to the /api/events ring buffer ("EventHandler:
// func(ev events.Event) { serveEvents.add(ev) }"). Unlike ToolEventHandler
// (tool_call/tool_result — the events serve_test.go pins as LIVE), SkillEventHandler,
// and MemoryEventHandler, nothing relays generic runtime events to WebSocket
// clients, so there is no live channel to subscribe to yet. Until such a relay
// exists, this panel polls the endpoint every PLAN_POLL_MS while visible
// (panels drawer open + plan tab active + document visible), refetches
// immediately on tab activation and session switch, and stops otherwise.
// Responses are tiny (version + counts + step rows), so the interval is cheap.
import { S, getSessionToken } from './state.js';
import { getSessionPlan } from './api.js';

const PLAN_POLL_MS = 5000;
const NOTE_MAX_CHARS = 140;

const listEl = document.getElementById('plan-list');

// Status glyphs mirror internal/telegram's plan renderer exactly
// (formatTelegramPlanStep): ⬜ pending, 🔄 in_progress, ✅ done, ⛔ blocked.
// Unknown statuses fall back to ⬜, matching the Go default.
export const PLAN_GLYPHS = {
  pending: '⬜',
  in_progress: '🔄',
  done: '✅',
  blocked: '⛔',
};

export function planGlyph(status) {
  return PLAN_GLYPHS[status] || PLAN_GLYPHS.pending;
}

// planSummary renders the header line for a plan response, mirroring the
// Telegram renderer's shape ("📋 Plan — vN · X/Y done[ · Z blocked]") minus
// the markdown emphasis. A collapsed all-done plan parses to a version with
// zero step rows (server contract), reported as "complete".
export function planSummary(plan) {
  const steps = (plan && plan.steps) || [];
  if (plan && plan.found && steps.length === 0) {
    return '📋 Plan — v' + plan.version + ' · complete';
  }
  const done = steps.filter(s => s.status === 'done').length;
  const blocked = steps.filter(s => s.status === 'blocked').length;
  let out = '📋 Plan — v' + ((plan && plan.version) || 0) + ' · ' + done + '/' + steps.length + ' done';
  if (blocked > 0) out += ' · ' + blocked + ' blocked';
  return out;
}

// truncateLine defensively caps model-chosen text (the validator flattens
// newlines, but length is only bounded for titles server-side).
function truncateLine(s, max) {
  if (!s) return '';
  return s.length > max ? s.slice(0, max) + '…' : s;
}

// el is a tiny createElement helper. Untrusted strings (step ids/titles/
// notes are model-derived content) reach the DOM exclusively through
// textContent — never innerHTML interpolation — so hostile markup stays inert.
function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text != null) node.textContent = text;
  return node;
}

// renderPlan draws one API response into root (default: the panel list).
// Exported for tests; production callers go through refreshPlanPanel.
export function renderPlan(plan, root) {
  const target = root || listEl;
  if (!target) return;
  target.textContent = '';

  if (!plan || !plan.found) {
    target.appendChild(el('div', 'mf-empty', 'No active plan'));
    return;
  }

  target.appendChild(el('div', 'plan-header', planSummary(plan)));

  const steps = plan.steps || [];
  steps.forEach(step => {
    const row = el('div', 'plan-row st-' + (step.status || 'pending'));

    const glyph = el('span', 'plan-glyph', planGlyph(step.status));
    glyph.title = step.status || 'pending';

    const body = el('div', 'plan-body');
    const head = el('div', 'plan-step-head');
    // Step id is a model-chosen token — untrusted like the title.
    head.appendChild(el('span', 'plan-step-id', step.id || ''));
    head.appendChild(el('span', 'plan-title', step.title || ''));
    body.appendChild(head);
    if (step.note) {
      body.appendChild(el('div', 'plan-note', truncateLine(step.note, NOTE_MAX_CHARS)));
    }

    row.append(glyph, body);
    target.appendChild(row);
  });
}

function showPlanError(err) {
  if (!listEl) return;
  listEl.textContent = '';
  const box = el('div', 'mf-empty', 'failed to load: ' + (err && err.message ? err.message : 'request failed'));
  listEl.appendChild(box);
}

// documentVisible reports whether the page can currently be refreshed.
// Drawer/tab state belongs to panels.js (it starts/stops polling on tab and
// drawer transitions); document visibility is checked here so a hidden tab
// never burns requests.
function documentVisible() {
  return !document.hidden;
}

// refreshPlanPanel performs one fetch+render cycle for the current session.
export async function refreshPlanPanel() {
  if (!listEl) return;
  if (!documentVisible()) return;
  const sid = S.sessionId;
  if (!sid) {
    renderPlan({ found: false });
    return;
  }
  try {
    const token = getSessionToken(sid);
    const plan = await getSessionPlan(sid, token || undefined);
    // Session may have switched while the request was in flight — a stale
    // response must never render under the new session's id.
    if (S.sessionId !== sid) return;
    renderPlan(plan);
  } catch (err) {
    if (S.sessionId !== sid) return; // same guard for late failures
    showPlanError(err);
  }
}

// ── Polling lifecycle ──

let planPollTimer = null;

export function stopPlanPolling() {
  if (planPollTimer) clearInterval(planPollTimer);
  planPollTimer = null;
}

export function startPlanPolling() {
  stopPlanPolling();
  planPollTimer = setInterval(() => {
    // Hidden document → skip this tick; the timer keeps running so the next
    // tick after the tab becomes visible catches up within one interval.
    if (documentVisible()) refreshPlanPanel();
  }, PLAN_POLL_MS);
}

// Tab becoming visible again mid-poll: refresh immediately instead of
// waiting out the remainder of the interval.
if (typeof document.addEventListener === 'function') {
  document.addEventListener('visibilitychange', () => {
    if (documentVisible() && planPollTimer) refreshPlanPanel();
  });
}

// resetPlanPanel is the session-switch hook (newSession / loadAndRenderSession):
// clear synchronously so a stale session's plan can never linger under a new
// session id, then refetch right away when the panel is being watched.
export function resetPlanPanel() {
  if (!listEl) return;
  listEl.textContent = '';
  // Only fetch eagerly when polling is active (panel open on the plan tab);
  // otherwise the next activation fetches fresh anyway.
  if (planPollTimer) refreshPlanPanel();
}
