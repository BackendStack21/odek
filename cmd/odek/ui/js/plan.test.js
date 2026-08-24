// Tests for the plan panel (plan.js): endpoint contract, found:false empty
// state, XSS inertness for model-derived step text, Telegram-mirrored status
// glyphs, header summary counts, session-switch reset, and the render.js
// toolEmoji mirror of internal/render's plan→📋 change.
//
// Drives the real modules through minimal browser shims (same harness style
// as approvals.test.js / api.test.js). Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Minimal browser shims ──

class FakeElement {
  constructor(tag = 'div') {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.className = '';
    this.id = '';
    this.title = '';
    this.dataset = {};
    this.style = {};
    this.disabled = false;
    this.hidden = false;
    this._listeners = {};
    this._attrs = {};
    this._text = '';
    this.innerHTML = ''; // stays '' unless code assigns innerHTML — the inertness tripwire
  }
  get textContent() {
    if (this.children.length) return this.children.map(c => c.textContent).join('');
    return this._text;
  }
  set textContent(v) {
    // Real DOM: assigning textContent replaces all children with one text node.
    this.children = [];
    this._text = String(v);
  }
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach(c => this.appendChild(c)); }
  remove() {
    if (this.parentNode) {
      const i = this.parentNode.children.indexOf(this);
      if (i >= 0) this.parentNode.children.splice(i, 1);
      this.parentNode = null;
    }
  }
  setAttribute(k, v) { this._attrs[k] = String(v); if (k === 'id') this.id = v; }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    const l = this._listeners[type];
    if (l) this._listeners[type] = l.filter(f => f !== fn);
  }
  focus() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
}

const els = new Map();
function el(id) {
  if (!els.has(id)) els.set(id, new FakeElement());
  return els.get(id);
}
// plan.js captures this element at import time — seed it up front.
els.set('plan-list', new FakeElement());

const docListeners = {};
globalThis.document = {
  getElementById: (id) => el(id),
  createElement: (tag) => new FakeElement(tag),
  querySelector: (sel) => {
    if (sel === 'meta[name="odek-ws-token"]') return { getAttribute: () => 'test-instance-token' };
    return null;
  },
  querySelectorAll: () => [],
  addEventListener: (type, fn) => { (docListeners[type] ||= []).push(fn); },
  body: new FakeElement('body'),
  hidden: false,
};
globalThis.localStorage = (() => {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
})();

// ── fetch mock ──
const requests = [];
let responses = [];
const EMPTY_PLAN = JSON.stringify({ session_id: '', version: 0, steps: [], found: false });
globalThis.fetch = async (url, init = {}) => {
  requests.push({ url: String(url), init });
  const r = responses.length ? responses.shift()
    : { status: 200, body: EMPTY_PLAN, headers: { 'content-type': 'application/json' } };
  return {
    ok: r.status >= 200 && r.status < 300,
    status: r.status,
    headers: { get: (h) => (r.headers || {})[h.toLowerCase()] || null },
    json: async () => JSON.parse(r.body),
    text: async () => r.body,
  };
};

const { S } = await import('./state.js');
const plan = await import('./plan.js');
const render = await import('./render.js');
const api = await import('./api.js');

function planResponse(steps, version = 3, found = true) {
  return JSON.stringify({ session_id: 'sess-1', version, steps, found });
}

function walk(node, fn) {
  fn(node);
  node.children.forEach(c => walk(c, fn));
}

function rowsIn(root) {
  return root.children.filter(c => String(c.className).split(/\s+/).includes('plan-row'));
}

beforeEach(() => {
  requests.length = 0;
  responses = [];
  S.sessionId = 'sess-1';
  S.sessionTokens = { 'sess-1': 'tok-1' };
  document.hidden = false;
  plan.stopPlanPolling();
  el('plan-list').textContent = '';
});

// ── Endpoint contract ──

test('getSessionPlan issues GET /api/sessions/{id}/plan with auth headers', async () => {
  await api.getSessionPlan('sess-1', 'tok-1');
  const req = requests[requests.length - 1];
  assert.equal(req.init.method, undefined); // fetch default GET
  assert.equal(req.url, '/api/sessions/sess-1/plan');
  assert.equal(req.init.headers['X-Session-Token'], 'tok-1');
  assert.equal(req.init.headers['X-Odek-Ws-Token'], 'test-instance-token');
});

test('refreshPlanPanel fetches the current session plan with its session token', async () => {
  responses.push({ status: 200, body: planResponse([{ id: 's1', title: 't', status: 'pending' }]), headers: { 'content-type': 'application/json' } });
  await plan.refreshPlanPanel();
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, '/api/sessions/sess-1/plan');
  assert.equal(requests[0].init.headers['X-Session-Token'], 'tok-1');
});

// ── Empty states ──

test('found:false renders the No-active-plan empty state', async () => {
  responses.push({ status: 200, body: planResponse([], 0, false), headers: { 'content-type': 'application/json' } });
  await plan.refreshPlanPanel();
  const list = el('plan-list');
  assert.equal(rowsIn(list).length, 0);
  assert.equal(list.children.length, 1);
  assert.match(list.children[0].className, /mf-empty/);
  assert.equal(list.children[0].textContent, 'No active plan');
});

test('no active session renders the empty state without fetching', async () => {
  S.sessionId = null;
  await plan.refreshPlanPanel();
  assert.equal(requests.length, 0);
  assert.equal(el('plan-list').children[0].textContent, 'No active plan');
});

test('request failure surfaces an error box, not a blank panel', async () => {
  responses.push({ status: 401, body: 'invalid session token', headers: {} });
  await plan.refreshPlanPanel();
  const first = el('plan-list').children[0];
  assert.match(first.className, /mf-empty/);
  assert.match(first.textContent, /failed to load/);
});

test('a response for a superseded session is dropped, never rendered', async () => {
  // Deferred fetch: hold sess-1's response until after the session switches.
  const oldFetch = globalThis.fetch;
  let release;
  globalThis.fetch = () => new Promise(resolve => {
    release = () => resolve({
      ok: true,
      status: 200,
      headers: { get: () => 'application/json' },
      json: async () => ({ session_id: 'sess-1', version: 1, steps: [{ id: 's1', title: 'old work', status: 'done' }], found: true }),
    });
  });
  try {
    const inflight = plan.refreshPlanPanel(); // sess-1 request in flight
    S.sessionId = 'sess-2';                   // operator switches sessions
    release();                                // sess-1's answer arrives late
    await inflight;
    // The stale plan must not appear under sess-2.
    assert.equal(rowsIn(el('plan-list')).length, 0);
    assert.equal(el('plan-list').children.length, 0);
  } finally {
    globalThis.fetch = oldFetch;
  }
});

// ── XSS inertness ──

test('hostile step titles/notes render inert — no element parsing, raw text only', async () => {
  const HOSTILE_TITLE = `<script>alert('xss')</script>`;
  const HOSTILE_NOTE = '`backtick` payload <img src=x onerror=alert(1)> tail';
  const HOSTILE_TITLE2 = '<img onerror=alert(1) src=x>';
  const HOSTILE_ID = '"><script>1</script>';
  responses.push({
    status: 200,
    body: planResponse([
      { id: 's1', title: HOSTILE_TITLE, status: 'in_progress', note: HOSTILE_NOTE },
      { id: HOSTILE_ID, title: HOSTILE_TITLE2, status: 'blocked' },
    ]),
    headers: { 'content-type': 'application/json' },
  });
  await plan.refreshPlanPanel();

  const list = el('plan-list');
  // Exactly the expected structure: header + two rows, nothing else — a
  // hostile payload parsed as HTML would have spawned extra nodes.
  assert.equal(rowsIn(list).length, 2);
  assert.equal(list.children.length, 3);

  // No executable/embedding element anywhere in the rendered tree.
  const tags = [];
  walk(list, n => tags.push(n.tagName));
  for (const dangerous of ['SCRIPT', 'IMG', 'IFRAME', 'SVG', 'OBJECT', 'EMBED']) {
    assert.ok(!tags.includes(dangerous), 'found dangerous element: ' + dangerous);
  }

  // The safe path builds rows with createElement + textContent and never
  // assigns innerHTML — if a regression switched to innerHTML interpolation
  // (escaped or not), this tripwire fails.
  walk(list, n => assert.equal(n.innerHTML, '', 'innerHTML was assigned on ' + n.tagName));

  // Payloads survive verbatim as text (they went in as text nodes).
  const text = list.textContent;
  assert.ok(text.includes(HOSTILE_TITLE), 'script title missing from text');
  assert.ok(text.includes(HOSTILE_NOTE), 'img-onerror note missing from text');
  assert.ok(text.includes(HOSTILE_TITLE2), 'img-onerror title missing from text');
  assert.ok(text.includes(HOSTILE_ID), 'hostile id missing from text');
});

// ── Status glyphs (mirror internal/telegram formatTelegramPlanStep) ──

test('glyph mapping covers all four statuses with ⬜ fallback', () => {
  assert.equal(plan.planGlyph('pending'), '⬜');
  assert.equal(plan.planGlyph('in_progress'), '🔄');
  assert.equal(plan.planGlyph('done'), '✅');
  assert.equal(plan.planGlyph('blocked'), '⛔');
  assert.equal(plan.planGlyph('something_else'), '⬜');
  assert.equal(plan.planGlyph(undefined), '⬜');
});

test('rows carry the right glyph, status class, and title attribute', async () => {
  responses.push({
    status: 200,
    body: planResponse([
      { id: 's1', title: 'one', status: 'done' },
      { id: 's2', title: 'two', status: 'in_progress' },
      { id: 's3', title: 'three', status: 'pending' },
      { id: 's4', title: 'four', status: 'blocked' },
      { id: 's5', title: 'five', status: 'bogus_status' },
    ]),
    headers: { 'content-type': 'application/json' },
  });
  await plan.refreshPlanPanel();
  const rows = rowsIn(el('plan-list'));
  assert.equal(rows.length, 5);

  const expect = [
    ['✅', 'st-done', 'done'],
    ['🔄', 'st-in_progress', 'in_progress'],
    ['⬜', 'st-pending', 'pending'],
    ['⛔', 'st-blocked', 'blocked'],
    ['⬜', 'st-bogus_status', 'bogus_status'], // unknown falls back to ⬜
  ];
  expect.forEach(([glyph, cls, status], i) => {
    const glyphEl = rows[i].children.find(c => String(c.className).includes('plan-glyph'));
    assert.equal(glyphEl.textContent, glyph, 'row ' + i + ' glyph');
    assert.equal(glyphEl.title, status, 'row ' + i + ' glyph tooltip');
    assert.ok(String(rows[i].className).includes(cls), 'row ' + i + ' class ' + cls);
  });
});

// ── Header summary (mirrors the Telegram renderer's shape) ──

test('summary line reports version, done fraction, and blocked count', () => {
  const steps = [
    { id: 's1', status: 'done' },
    { id: 's2', status: 'in_progress' },
    { id: 's3', status: 'blocked' },
    { id: 's4', status: 'pending' },
    { id: 's5', status: 'pending' },
  ];
  assert.equal(plan.planSummary({ found: true, version: 3, steps }), '📋 Plan — v3 · 1/5 done · 1 blocked');
});

test('summary omits the blocked segment when nothing is blocked', () => {
  const steps = [{ id: 's1', status: 'done' }, { id: 's2', status: 'in_progress' }];
  assert.equal(plan.planSummary({ found: true, version: 7, steps }), '📋 Plan — v7 · 1/2 done');
});

test('collapsed all-done plan (version, zero rows) reports complete', () => {
  assert.equal(plan.planSummary({ found: true, version: 9, steps: [] }), '📋 Plan — v9 · complete');
});

test('panel header element shows the summary for the fetched plan', async () => {
  responses.push({
    status: 200,
    body: planResponse([
      { id: 's1', title: 'a', status: 'done' },
      { id: 's2', title: 'b', status: 'pending' },
    ], 4),
    headers: { 'content-type': 'application/json' },
  });
  await plan.refreshPlanPanel();
  const head = el('plan-list').children.find(c => String(c.className).includes('plan-header'));
  assert.equal(head.textContent, '📋 Plan — v4 · 1/2 done');
});

// ── Session-switch reset ──

test('resetPlanPanel clears stale rows synchronously and refetches while polling', async () => {
  // Load sess-1's plan.
  responses.push({
    status: 200,
    body: planResponse([{ id: 's1', title: 'old work', status: 'done' }]),
    headers: { 'content-type': 'application/json' },
  });
  await plan.refreshPlanPanel();
  assert.equal(rowsIn(el('plan-list')).length, 1);

  // Switch sessions with the panel being watched (polling active).
  plan.startPlanPolling();
  S.sessionId = 'sess-2';
  S.sessionTokens = { 'sess-2': 'tok-2' };
  plan.resetPlanPanel();

  // Stale rows are gone synchronously — before any network round-trip.
  assert.equal(rowsIn(el('plan-list')).length, 0);

  // The refetch targets the NEW session.
  await new Promise(r => setImmediate(r));
  const req = requests[requests.length - 1];
  assert.equal(req.url, '/api/sessions/sess-2/plan');
  assert.equal(req.init.headers['X-Session-Token'], 'tok-2');
});

test('resetPlanPanel without polling clears but does not fetch', async () => {
  responses.push({
    status: 200,
    body: planResponse([{ id: 's1', title: 'x', status: 'done' }]),
    headers: { 'content-type': 'application/json' },
  });
  await plan.refreshPlanPanel();
  assert.ok(el('plan-list').children.length > 0);

  plan.resetPlanPanel();
  await new Promise(r => setImmediate(r));
  assert.equal(requests.length, 1); // only the original fetch
  assert.equal(el('plan-list').children.length, 0);
});

test('switching to a session with no plan lands on the empty state', async () => {
  plan.startPlanPolling();
  S.sessionId = 'sess-9';
  S.sessionTokens = { 'sess-9': '' };
  plan.resetPlanPanel();
  await new Promise(r => setImmediate(r));
  assert.equal(requests[requests.length - 1].url, '/api/sessions/sess-9/plan');
  assert.equal(el('plan-list').children[0].textContent, 'No active plan');
});

// ── Visibility guard ──

test('refresh skips the fetch while the document is hidden', async () => {
  document.hidden = true;
  await plan.refreshPlanPanel();
  assert.equal(requests.length, 0);
  document.hidden = false;
});

test('becoming visible mid-poll triggers an immediate refresh', async () => {
  plan.startPlanPolling();
  assert.ok(docListeners.visibilitychange, 'visibilitychange listener registered');
  document.hidden = false;
  for (const fn of docListeners.visibilitychange) fn();
  await new Promise(r => setImmediate(r));
  assert.equal(requests.length, 1);
});

// ── render.js toolEmoji mirror (internal/render/render.go parity) ──

test('toolEmoji maps plan to 📋 and retires the dead todo arm to the default', () => {
  assert.equal(render.toolEmoji('plan'), '📋');
  assert.equal(render.toolEmoji('todo'), '🔧'); // dead arm removed in Go — falls through
  // Neighbors unchanged by the Go diff stay pinned.
  assert.equal(render.toolEmoji('cronjob'), '⏰');
  assert.equal(render.toolEmoji('skill_view'), '➕');
  assert.equal(render.toolEmoji('clarify'), '➕');
  assert.equal(render.toolEmoji('shell'), '💻');
});
