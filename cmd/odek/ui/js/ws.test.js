// DOM-shim tests for the ws.js server-event dispatch around cancellation:
// a `cancelled` event must tear down every pending approval card so an
// approve-after-cancel can never execute a tool on a dead context. These
// drive the real ws.js message switch through a minimal browser shim —
// the same harness approvals.test.js uses. Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Minimal browser shims (ws.js → state/dom/utils/render/approvals/…) ──

class FakeElement {
  constructor(tag = 'div') {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.style = {};
    this.dataset = {};
    this._listeners = {};
    this.className = '';
    this.id = '';
    this.textContent = '';
    this.innerHTML = '';
    this.title = '';
    this.disabled = false;
    this.tabIndex = 0;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
    this.value = '';
    this._attrs = {};
  }
  appendChild(c) {
    if (c.parentNode) {
      const i = c.parentNode.children.indexOf(c);
      if (i >= 0) c.parentNode.children.splice(i, 1);
    }
    c.parentNode = this;
    this.children.push(c);
    return c;
  }
  insertBefore(c, ref) {
    if (c.parentNode) {
      const i = c.parentNode.children.indexOf(c);
      if (i >= 0) c.parentNode.children.splice(i, 1);
    }
    c.parentNode = this;
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i >= 0) this.children.splice(i, 0, c); else this.children.push(c);
    return c;
  }
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
  // sessions.js inserts its search-clear button next to the input at import.
  insertAdjacentElement(_pos, node) { this.appendChild(node); return node; }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  fire(type, ev = {}) {
    for (const fn of [...(this._listeners[type] || [])]) {
      fn({ target: this, preventDefault() {}, stopPropagation() {}, ...ev });
    }
  }
  focus() {}
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  querySelectorAll(sel) {
    const out = [];
    const cls = sel.startsWith('.') ? sel.slice(1) : null;
    const idSel = sel.startsWith('#') ? sel.slice(1) : null;
    const walk = (el) => {
      for (const c of el.children) {
        if ((cls && classNames(c).includes(cls)) || (idSel && c.id === idSel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  get classList() {
    const self = this;
    return {
      names: () => classNames(self),
      add: (...names) => { self.className = [...new Set([...classNames(self), ...names])].join(' '); },
      remove: (...names) => { self.className = classNames(self).filter((n) => !names.includes(n)).join(' '); },
      toggle: (name, force) => {
        const has = classNames(self).includes(name);
        const want = force === undefined ? !has : !!force;
        if (want) self.classList.add(name); else self.classList.remove(name);
        return want;
      },
      contains: (name) => classNames(self).includes(name),
    };
  }
}

function classNames(el) { return String(el.className).split(/\s+/).filter(Boolean); }

const els = new Map();
const el = (id) => {
  if (!els.has(id)) els.set(id, new FakeElement());
  return els.get(id);
};

const selEls = new Map();
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.document = {
  getElementById: (id) => el(id),
  createElement: (tag) => new FakeElement(tag),
  // sessions.js wires static buttons via querySelector at import time.
  querySelector: (sel) => {
    if (!selEls.has(sel)) selEls.set(sel, new FakeElement());
    return selEls.get(sel);
  },
  addEventListener: () => {},
  body: new FakeElement('body'),
  activeElement: null,
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
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

// Minimal WebSocket stand-in: connect() only needs the constructor plus the
// OPEN constant; tests dispatch frames by calling S.ws.onmessage directly.
globalThis.WebSocket = class FakeWebSocket {
  static get OPEN() { return 1; }
  constructor() {
    this.readyState = 1;
    this.sent = [];
    this.onopen = null;
    this.onclose = null;
    this.onerror = null;
    this.onmessage = null;
  }
  send(m) { this.sent.push(m); }
  close() { this.readyState = 3; }
};

const S = (await import('./state.js')).S;
const approvals = await import('./approvals.js');
const ws = await import('./ws.js');

function deliver(event) {
  S.ws.onmessage({ data: JSON.stringify(event) });
}

beforeEach(() => {
  ws.connect();
  approvals.clearApprovals();
  while (el('messages').children.length) el('messages').children[0].remove();
});

// ── cancelled tears down pending approvals ──

test('cancelled event dismisses the active approval card and queue', () => {
  approvals.queueApproval({ id: 'apr-1', risk: 'local_write', command: 'echo hi', allow_trust: true });
  assert.equal(S.activeApprovalId, 'apr-1', 'card must be up before the cancel lands');

  deliver({ type: 'cancelled', session_id: 's1' });

  assert.equal(S.activeApprovalId, null, 'active approval id must be cleared');
  assert.equal(S.activeApprovalCard, null, 'approval card must be removed');
  assert.equal(S.approvalQueue.length, 0, 'queued requests must be dropped');
  // The card is gone from the transcript — nothing left to click.
  assert.equal(el('messages').querySelector('.approval-card'), null);
});

test('cancelled event also clears queued-but-hidden requests', () => {
  approvals.queueApproval({ id: 'apr-a', risk: 'safe', command: 'one' });
  approvals.queueApproval({ id: 'apr-b', risk: 'safe', command: 'two' });
  deliver({ type: 'cancelled', session_id: 's1' });
  assert.equal(S.approvalQueue.length, 0);
  assert.equal(S.activeApprovalId, null);
});

test('approve after cancelled sends nothing — the dead card is gone', () => {
  approvals.queueApproval({ id: 'apr-1', risk: 'local_write', command: 'echo hi', allow_trust: true });
  deliver({ type: 'cancelled', session_id: 's1' });
  // Drive the real click path post-cancel: sendApproval must bail on the
  // cleared activeApprovalId and emit no WS frame — the exact
  // approve-after-cancel race this fix closes.
  approvals.sendApproval('approve');
  assert.deepEqual(S.ws.sent, [], 'no approval_response may be sent after the cancel teardown');
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.approvalQueue.length, 0);
});

test('idle cancelled (nothing to cancel) still clears stale cards', () => {
  approvals.queueApproval({ id: 'apr-stale', risk: 'safe', command: 'stale' });
  deliver({ type: 'cancelled', session_id: 's1', idle: true });
  assert.equal(S.approvalQueue.length, 0, 'stale card must not survive an idle cancel');
});

test('cancelled event still appends the system notice', () => {
  deliver({ type: 'cancelled', session_id: 's1' });
  const msgs = el('messages').children;
  assert.ok(msgs.length >= 1, 'system message appended');
  assert.ok(classNames(msgs[msgs.length - 1]).includes('msg'));
});

test('keepalive frames are ignored — no system message, no busy change', () => {
  const before = el('messages').children.length;
  deliver({ type: 'keepalive', t: Date.now() });
  assert.equal(el('messages').children.length, before, 'keepalive must not append transcript noise');
});

test('provider timeout errors render a retry hint, not the raw SDK line', () => {
  deliver({ type: 'error', message: 'iteration 3: context deadline exceeded' });
  const html = el('messages').children.at(-1).innerHTML;
  assert.match(html, /Provider request timed out/);
  assert.ok(!html.includes('deadline exceeded'), 'raw SDK timeout must not leak');
});

test('stream-idle errors render a stall hint', () => {
  deliver({ type: 'error', message: 'llm: stream idle for over 2m0s without an event' });
  const html = el('messages').children.at(-1).innerHTML;
  assert.match(html, /Provider stream stalled/);
});

// ── usage/done feed the ctx gauge with the parent window (wire v3) ──

test('usage event seeds the gauge with the parent window and server model limit', () => {
  deliver({ type: 'usage', windowTokens: 38412, maxContextTokens: 200000, outputTokens: 512 });
  assert.equal(S.metrics.ctxTokens, 38412, 'gauge must show the parent window, not a cumulative');
  assert.equal(S.metrics.maxContext, 200000, 'server-reported model limit must override the models table');
});

test('usage without windowTokens never moves the gauge', () => {
  S.metrics.ctxTokens = 1234;
  deliver({ type: 'usage', outputTokens: 10 });
  assert.equal(S.metrics.ctxTokens, 1234, 'absent windowTokens means "not reported" — gauge holds');
});

test('done seeds the gauge from windowTokens, totals from session fields', () => {
  deliver({
    type: 'done', latency: 0.5,
    windowTokens: 41000, maxContextTokens: 200000,
    inputTokens: 152300, outputTokens: 8231,
    sessionContextTokens: 300000, sessionOutputTokens: 40000,
  });
  assert.equal(S.metrics.ctxTokens, 41000, 'gauge must take the final PARENT window from done — never inputTokens');
  assert.equal(S.metrics.maxContext, 200000, 'done maxContextTokens must override the models table');
  assert.equal(S.metrics.sessIn, 300000);
  assert.equal(S.metrics.sessOut, 40000);
});

test('done without windowTokens holds the last gauge value — no zeroing', () => {
  S.metrics.ctxTokens = 41000;
  deliver({ type: 'done', latency: 0.5, inputTokens: 100, outputTokens: 10 });
  assert.equal(S.metrics.ctxTokens, 41000, 'absent windowTokens means "not reported" — gauge holds');
});
