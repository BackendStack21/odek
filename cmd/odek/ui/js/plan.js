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
import { paintIntent } from './render.js';

// Bodek cadences: 250ms WS-trigger debounce, 1s strip while a turn runs,
// 3s while the Now tab is watching. Hidden-tab 5s poll is gone — idle
// sessions keep the last snapshot and only refetch on a trigger.
const PLAN_DEBOUNCE_MS = 250;
const PLAN_LIVE_MS = 1000;
const PLAN_TAB_MS = 3000;
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
  const panels = document.getElementById('panels');
  if (panels && panels.classList && typeof panels.classList.contains === 'function') {
    const tab = panels.querySelector ? panels.querySelector('.ptab.active') : null;
    if (!panels.classList.contains('active') || !tab || tab.dataset.tab !== 'now') {
      stopPlanPolling();
      return;
    }
  }
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
    adoptPlan(plan, { confirm: true });
    renderPlan(S.plan || plan);
  } catch (err) {
    if (S.sessionId !== sid) return; // same guard for late failures
    showPlanError(err);
  }
}

// ── Live snapshot (Bodek: optimistic tool_call + REST confirm + busy poll) ──

let planPollTimer = null;
let planLiveTimer = null;
let planDebounceTimer = null;
let planReqSeq = 0;

export function adoptPlan(plan, opts) {
  const confirm = !!(opts && opts.confirm);
  if (!plan) return;
  if (S.planDirty && !confirm) return;
  const ver = Number(plan.version) || 0;
  if (S.plan && S.planVer && ver < S.planVer) return;
  S.plan = plan;
  S.planVer = ver;
  S.planAvail = 'available';
  S.planDirty = false;
  paintIntent();
  if (nowTabWatching()) renderPlan(plan);
}

function coerceStatus(s) {
  if (s === 'pending' || s === 'in_progress' || s === 'done' || s === 'blocked') return s;
  return 'pending';
}

// applyPlanMutation patches S.plan from a plan tool_call payload so the
// strip moves on the same frame as the step (Bodek applyPlanMutation).
export function applyPlanMutation(data) {
  if (S.planAvail === 'unavailable') return false;
  let args;
  try { args = JSON.parse(String(data || '').trim()); } catch { return false; }
  if (!args || typeof args !== 'object') return false;
  const verb = args.verb;
  if (verb === 'create') {
    const steps = [];
    (args.steps || []).forEach((raw) => {
      if (!raw || !raw.id) return;
      steps.push({
        id: String(raw.id),
        title: String(raw.title || ''),
        note: String(raw.note || ''),
        status: coerceStatus(raw.status),
      });
    });
    if (!steps.length) return false;
    S.plan = { found: true, version: S.planVer || 0, steps };
    S.planAvail = 'available';
    S.planDirty = true;
    paintIntent();
    if (nowTabWatching()) renderPlan(S.plan);
    return true;
  }
  if (verb === 'update') {
    if (!S.plan || !S.plan.steps) return false;
    let changed = false;
    (args.updates || []).forEach((u) => {
      if (!u || !u.id) return;
      S.plan.steps.forEach((st) => {
        if (st.id !== u.id) return;
        if (u.status) { st.status = coerceStatus(u.status); changed = true; }
        if (u.note != null) st.note = String(u.note);
      });
    });
    if (changed) {
      S.planDirty = true;
      paintIntent();
      if (nowTabWatching()) renderPlan(S.plan);
    }
    return changed;
  }
  if (verb === 'complete') {
    if (!S.plan || !S.plan.steps) return false;
    const id = args.step_id || args.stepId;
    if (!id) return false;
    let changed = false;
    S.plan.steps.forEach((st) => {
      if (st.id === id && st.status !== 'done') {
        st.status = 'done';
        changed = true;
      }
    });
    if (changed) {
      S.planDirty = true;
      paintIntent();
      if (nowTabWatching()) renderPlan(S.plan);
    }
    return changed;
  }
  return false;
}

export async function fetchPlanSnapshot(opts) {
  const confirm = !!(opts && opts.confirm);
  const sid = S.sessionId;
  if (!sid) return;
  const seq = ++planReqSeq;
  try {
    const plan = await getSessionPlan(sid, getSessionToken(sid) || undefined);
    if (seq !== planReqSeq || S.sessionId !== sid) return;
    adoptPlan(plan, { confirm });
  } catch (err) {
    if (seq !== planReqSeq || S.sessionId !== sid) return;
    if (err && err.status === 404) S.planAvail = 'unavailable';
  }
}

export function schedulePlanRefresh() {
  if (planDebounceTimer) clearTimeout(planDebounceTimer);
  planDebounceTimer = setTimeout(() => {
    planDebounceTimer = null;
    fetchPlanSnapshot({ confirm: true });
  }, PLAN_DEBOUNCE_MS);
}

function nowTabWatching() {
  if (!documentVisible()) return false;
  const panels = document.getElementById('panels');
  if (!panels || !panels.classList || typeof panels.classList.contains !== 'function') return false;
  if (!panels.classList.contains('active')) return false;
  const tab = panels.querySelector && panels.querySelector('.ptab.active');
  return !!(tab && tab.dataset.tab === 'now');
}

export function stopPlanPolling() {
  if (planPollTimer) clearInterval(planPollTimer);
  planPollTimer = null;
  if (!S.busy) stopPlanLive();
}

function stopPlanLive() {
  if (planLiveTimer) clearInterval(planLiveTimer);
  planLiveTimer = null;
}

export function startPlanPolling() {
  stopPlanPolling();
  planPollTimer = setInterval(() => {
    if (documentVisible()) refreshPlanPanel();
  }, PLAN_TAB_MS);
}

export function kickPlanLive() {
  if (!S.busy || !S.sessionId || S.planAvail === 'unavailable') return;
  fetchPlanSnapshot();
  if (planLiveTimer) return;
  planLiveTimer = setInterval(() => {
    if (!S.busy) { stopPlanLive(); return; }
    if (S.planDirty) return;
    fetchPlanSnapshot();
  }, PLAN_LIVE_MS);
}

export function stopPlanLiveIfIdle() {
  if (!S.busy) stopPlanLive();
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
  S.plan = null;
  S.planDirty = false;
  S.planVer = 0;
  S.planAvail = 'unknown';
  planReqSeq++;
  if (planDebounceTimer) { clearTimeout(planDebounceTimer); planDebounceTimer = null; }
  paintIntent();
  if (!listEl) return;
  listEl.textContent = '';
  if (planPollTimer) refreshPlanPanel();
}
