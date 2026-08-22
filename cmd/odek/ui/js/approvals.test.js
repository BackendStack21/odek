// DOM-shim tests for the approval decision card (approvals.js): queue →
// render → click/keyboard → exact WS message, plus ack dismissal, the
// friction gate, and session-switch teardown. These drive the real module
// through a minimal browser shim — the "approval buttons do nothing"
// regression shipped because nothing exercised this DOM behavior.
// Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Minimal browser shims (approvals.js → state/dom/utils/render) ──

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
    // Real DOM inputs default to '' — never undefined.
    this.value = '';
    this._attrs = {};
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
  fire(type, ev = {}) {
    if (type === 'click' && this.disabled) return; // browsers don't dispatch clicks on disabled buttons
    for (const fn of [...(this._listeners[type] || [])]) {
      fn({ target: this, preventDefault() {}, stopPropagation() {}, ...ev });
    }
  }
  click() { if (!this.disabled) this.fire('click'); }
  focus() {}
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  querySelectorAll(sel) {
    const out = [];
    const cls = sel.startsWith('.') ? sel.slice(1) : null;
    const idSel = sel.startsWith('#') ? sel.slice(1) : null;
    const walk = (el) => {
      for (const c of el.children) {
        if ((cls && c.classList().includes(cls)) || (idSel && c.id === idSel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  classList() { return String(this.className).split(/\s+/).filter(Boolean); }
}

const els = new Map();
function el(id) {
  if (!els.has(id)) els.set(id, new FakeElement());
  return els.get(id);
}

// Document-level listeners are recorded so tests can drive the real
// keyboard shortcut handler registered by approvals.js at import time.
const docListeners = {};
globalThis.document = {
  getElementById: (id) => el(id),
  createElement: (tag) => new FakeElement(tag),
  addEventListener: (type, fn) => { (docListeners[type] ||= []).push(fn); },
  querySelector: () => null,
  body: new FakeElement('body'),
  activeElement: null,
};
function fireKey(key, ev = {}) {
  for (const fn of docListeners.keydown || []) {
    fn({
      key, target: {}, preventDefault() {}, stopPropagation() {},
      metaKey: false, ctrlKey: false, altKey: false, shiftKey: false,
      ...ev,
    });
  }
}

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

const S = (await import('./state.js')).S;
const approvals = await import('./approvals.js');

// Captured WS frames + a stub socket.
let sent;
beforeEach(() => {
  sent = [];
  S.ws = { readyState: 1, send: (m) => sent.push(JSON.parse(m)) };
  approvals.clearApprovals();
  while (el('messages').children.length) el('messages').children[0].remove();
});

// queueOne renders one non-friction card and returns its buttons.
function queueOne(overrides = {}) {
  approvals.queueApproval({
    id: 'apr-1', risk: 'local_write', command: 'echo hi',
    allow_trust: true, ...overrides,
  });
  const card = S.activeApprovalCard;
  return {
    card,
    approve: card.querySelector('.approve'),
    deny: card.querySelector('.deny'),
    trust: card.querySelector('.trust'),
  };
}

// ── Rendering ──

test('approval request renders an inline decision card with the active id set', () => {
  const { card } = queueOne();
  assert.ok(card, 'card should be rendered');
  // Regression: the id used to be nulled during render, deadening every
  // button. It must equal the shown request the moment the card appears.
  assert.equal(S.activeApprovalId, 'apr-1');
  assert.ok(el('messages').children.includes(card), 'card appended to #messages');
  assert.equal(card.querySelector('.ac-command').textContent, 'echo hi');
  assert.equal(card.getAttribute('role'), 'alertdialog');
});

test('trust button hidden when the server disallows class trust', () => {
  const { trust } = queueOne({ risk: 'destructive', allow_trust: false });
  assert.equal(trust.style.display, 'none');
});

test('queue position indicator lists waiting requests', () => {
  approvals.queueApproval({ id: 'apr-a', risk: 'safe', command: 'one' });
  approvals.queueApproval({ id: 'apr-b', risk: 'safe', command: 'two' });
  assert.match(S.activeApprovalCard.querySelector('.ac-queue-pos')?.textContent || '', /request 1 of 2/);
});

test('position hint appears on an already-shown card when a new request arrives', () => {
  // Regression: the first card rendered at depth 1 (no hint) and later
  // arrivals never re-rendered it — the "more waiting" cue never showed.
  approvals.queueApproval({ id: 'apr-a', risk: 'safe', command: 'one' });
  assert.equal(S.activeApprovalCard.querySelector('.ac-queue-pos'), null);
  approvals.queueApproval({ id: 'apr-b', risk: 'safe', command: 'two' });
  const pos = S.activeApprovalCard.querySelector('.ac-queue-pos');
  assert.ok(pos, 'hint must appear on the live card');
  assert.match(pos.textContent, /request 1 of 2/);
});

test('answering one of two queued requests drops the stale hint on the next card', () => {
  approvals.queueApproval({ id: 'apr-a', risk: 'safe', command: 'one' });
  approvals.queueApproval({ id: 'apr-b', risk: 'safe', command: 'two' });
  approvals.queueApproval({ id: 'apr-c', risk: 'safe', command: 'three' });
  S.activeApprovalCard.querySelector('.approve').click();
  // apr-b is now shown with one still waiting.
  const pos = S.activeApprovalCard.querySelector('.ac-queue-pos');
  assert.match(pos?.textContent || '', /request 1 of 2/);
  // Answering apr-c elsewhere removes it from the queue; the active card's
  // hint must update rather than keep claiming more are waiting.
  approvals.dismissApproval('apr-c');
  assert.equal(
    S.activeApprovalCard.querySelector('.ac-queue-pos'),
    null,
    'hint removed when no other request waits',
  );
});

// ── Buttons send the wire message ──
// The server parses these frames as {type,id,action} (approvalResponse in
// wsapprover.go); any drift here silently breaks remote approval.

test('approve button sends approval_response with the request id', () => {
  const { approve } = queueOne();
  approve.click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'approve' }]);
  assert.equal(S.approvalQueue.length, 0, 'request removed after answering');
  assert.equal(S.activeApprovalId, null, 'card dismissed after answering');
});

test('deny button sends action deny', () => {
  const { deny } = queueOne();
  deny.click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'deny' }]);
});

test('trust button sends action trust when allowed', () => {
  const { trust } = queueOne();
  trust.click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'trust' }]);
});

// ── Keyboard operation ──

test('keyboard shortcuts answer the active card (a / d / t)', () => {
  // One request can only be answered once — each key gets a fresh request.
  const { approve } = queueOne({ id: 'apr-1', risk: 'local_write', command: 'one' });
  fireKey('a');
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'approve' }]);
  queueOne({ id: 'apr-2', risk: 'local_write', command: 'two' });
  fireKey('d');
  assert.deepEqual(sent.slice(1), [{ type: 'approval_response', id: 'apr-2', action: 'deny' }]);
  queueOne({ id: 'apr-3', risk: 'local_write', command: 'three' });
  fireKey('t');
  assert.deepEqual(sent.slice(2), [{ type: 'approval_response', id: 'apr-3', action: 'trust' }]);
  void approve;
});

test('keyboard shortcuts ignored while typing in an input', () => {
  queueOne({ friction: true });
  // Simulate keystrokes landing in the friction input (tagName INPUT).
  fireKey('a', { target: { tagName: 'INPUT' } });
  assert.deepEqual(sent, [], 'typing "a" in an input must not approve');
});

test('trust shortcut suppressed when trust is disallowed', () => {
  queueOne({ risk: 'destructive', allow_trust: false });
  fireKey('t');
  assert.deepEqual(sent, []);
  fireKey('d');
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'deny' }]);
});

// ── Queue behavior ──

test('requests queue FIFO; next card shows after answering', () => {
  approvals.queueApproval({ id: 'apr-a', risk: 'local_write', command: 'one', allow_trust: true });
  approvals.queueApproval({ id: 'apr-b', risk: 'local_write', command: 'two', allow_trust: true });
  assert.equal(S.activeApprovalId, 'apr-a');
  S.activeApprovalCard.querySelector('.approve').click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-a', action: 'approve' }]);
  assert.equal(S.activeApprovalId, 'apr-b');
  S.activeApprovalCard.querySelector('.deny').click();
  assert.deepEqual(sent, [
    { type: 'approval_response', id: 'apr-a', action: 'approve' },
    { type: 'approval_response', id: 'apr-b', action: 'deny' },
  ]);
});

test('approval_ack from another client dismisses without responding', () => {
  queueOne();
  approvals.dismissApproval('apr-1');
  assert.deepEqual(sent, [], 'dismissal must not send anything');
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.approvalQueue.length, 0);
});

// ── Session-switch teardown ──

test('clearApprovals drops queue + card + id; later requests still render', () => {
  approvals.queueApproval({ id: 'apr-a', risk: 'safe', command: 'one' });
  approvals.queueApproval({ id: 'apr-b', risk: 'safe', command: 'two' });
  approvals.clearApprovals();
  assert.equal(S.approvalQueue.length, 0);
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.activeApprovalCard, null);
  // Regression guard for the sessions.js integration: after teardown a new
  // request must still render AND answer (a stale id would suppress both).
  const { approve } = queueOne({ id: 'apr-c', risk: 'safe', command: 'three' });
  assert.equal(S.activeApprovalId, 'apr-c');
  approve.click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-c', action: 'approve' }]);
});

// ── Friction gate ──

test('friction mode: typing the word enables approve; click sends', async () => {
  const { approve } = queueOne({ friction: true, friction_approvals: 3 });
  assert.equal(approve.disabled, true, 'approve starts disabled');
  // Disabled buttons dispatch nothing (shim matches browser behavior).
  approve.fire('click');
  assert.deepEqual(sent, []);
  // The input listener attaches after the 1.5s gate.
  await new Promise((r) => setTimeout(r, 1600));
  const input = S.activeApprovalCard.querySelector('.ac-friction-input');
  input.value = 'Approve '; // case/whitespace-insensitive per spec
  input.fire('input');
  assert.equal(approve.disabled, false, 'correct word enables the button');
  approve.click();
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'approve' }]);
});

test('friction mode: wrong word keeps approve disabled', async () => {
  const { approve } = queueOne({ friction: true, friction_approvals: 4 });
  await new Promise((r) => setTimeout(r, 1600));
  const input = S.activeApprovalCard.querySelector('.ac-friction-input');
  input.value = 'yes';
  input.fire('input');
  assert.equal(approve.disabled, true);
});

test('friction mode: Enter in the input approves once the gate passes', async () => {
  queueOne({ friction: true, friction_approvals: 3 });
  await new Promise((r) => setTimeout(r, 1600));
  const input = S.activeApprovalCard.querySelector('.ac-friction-input');
  input.value = 'approve';
  input.fire('input');
  input.fire('keydown', { key: 'Enter' });
  assert.deepEqual(sent, [{ type: 'approval_response', id: 'apr-1', action: 'approve' }]);
});
