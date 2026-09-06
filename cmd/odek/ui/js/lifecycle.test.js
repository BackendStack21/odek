// Lifecycle-fix tests (M1): F-A2 mid-run session switch guard, F-A3
// reconnect unbrick, F-B1 delegate_tasks tool_result routing, F-B2
// attachment-preserving send guards, F-B6 corrupt-history fallback, and
// F-C1 the .sa-stop delegation arm. Each test names the RED failure it
// closed in its comment. Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Mini-DOM: records listeners (delegation needs them) and parses the
// simple static innerHTML render.js injects (subagent cards, tool blocks). ──

class ClassList {
  constructor(el) { this.el = el; }
  get set() { return new Set((this.el.className || '').split(/\s+/).filter(Boolean)); }
  add(...cs) { const s = this.set; cs.forEach(c => s.add(c)); this.el.className = [...s].join(' '); }
  remove(...cs) { const s = this.set; cs.forEach(c => s.delete(c)); this.el.className = [...s].join(' '); }
  contains(c) { return this.set.has(c); }
  toggle(c, force) {
    const has = this.contains(c);
    const want = force === undefined ? !has : !!force;
    if (want) this.add(c); else this.remove(c);
    return want;
  }
}

class FakeEl {
  constructor(tag = 'div') {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.className = '';
    this.classList = new ClassList(this);
    this.dataset = {};
    this.textContent = '';
    this.title = '';
    this.disabled = false;
    this.hidden = false;
    this.scrollTop = 0;
    this.scrollHeight = 100;
    this.clientHeight = 50;
    this._listeners = {};
    this._attrs = {};
    this._innerHTML = '';
    this.style = {}; // sessions.js newSession sets promptEl.style.height
    const shim = this;
    Object.defineProperty(this, 'innerHTML', {
      get() { return shim._innerHTML; },
      set(html) {
        shim._innerHTML = html;
        shim.children = [];
        const tokens = html.match(/<\/?[a-z-]+[^>]*>|[^<]+/g) || [];
        const stack = [shim];
        for (const tok of tokens) {
          const parent = stack[stack.length - 1];
          if (tok.startsWith('</')) {
            if (stack.length > 1) stack.pop();
            continue;
          }
          if (tok.startsWith('<')) {
            const el = new FakeEl((tok.match(/^<([a-z-]+)/) || [null, 'div'])[1]);
            const cls = tok.match(/class="([^"]*)"/);
            if (cls) el.className = cls[1];
            const idm = tok.match(/id="([^"]*)"/);
            if (idm) el.id = idm[1];
            parent.appendChild(el);
            stack.push(el);
            continue;
          }
          parent.textContent += tok;
        }
      },
    });
  }
  get id() { return this._id || ''; }
  set id(v) { this._id = v; }
  setAttribute(k, v) { this._attrs[k] = String(v); if (k === 'id') this.id = v; }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  matches(sel) {
    if (sel.startsWith('.')) {
      // Compound selectors (.a.b) must match every class, not the literal
      // string 'a.b' — systemMessages() etc. query '.msg.system'.
      return sel.slice(1).split('.').every((c) => c && this.classList.contains(c));
    }
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    return this.tagName === sel.toUpperCase();
  }
  closest(sel) {
    let el = this;
    while (el) {
      if (el.matches(sel)) return el;
      el = el.parentNode;
    }
    return null;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  querySelectorAll(sel) {
    const out = [];
    const parts = sel.split(',').map(s => s.trim());
    const walk = (el) => {
      for (const c of el.children) {
        if (parts.some(p => !p.includes(':scope') && c.matches(p))) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
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
  append(...cs) { cs.forEach(c => this.appendChild(c)); }
  // Mirrors ws.test.js: position ignored, node lands as a child — enough
  // for sessions.js's top-level search-clear bootstrap to survive import.
  insertAdjacentElement(_pos, node) { return this.appendChild(node); }
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
  remove() {
    if (this.parentNode) {
      const i = this.parentNode.children.indexOf(this);
      if (i >= 0) this.parentNode.children.splice(i, 1);
      this.parentNode = null;
    }
  }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  // dispatch bubbles like a real click: listeners run from the target up.
  dispatch(type, ev = {}) {
    let el = this;
    while (el) {
      for (const fn of [...(el._listeners[type] || [])]) {
        fn({ target: this, preventDefault() {}, stopPropagation() {}, ...ev });
      }
      el = el.parentNode;
    }
  }
  focus() {}
  // Browsers don't dispatch clicks on disabled buttons; mirrors the
  // FakeElement.click() the approvals/ws harnesses use.
  click() { if (!this.disabled) this.dispatch('click'); }
}

// localStorage shim whose backing map is reachable so tests can seed
// corrupt values BEFORE the state.js import runs.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
// F-B6 seed: corrupt history present at module-load time.
store.set('odek_history', '{definitely not json');

const byId = {};
const bySelector = {};
const ids = ['messages', 'prompt', 'send-btn', 'completion', 'ws-status', 'ws-dot',
  'model-label', 'session-list', 'sidebar-search', 'empty-state', 'cancel-btn',
  'scroll-bottom-btn', 'loading-skeleton', 'sidebar-overlay', 'file-input',
  'attach-btn', 'file-chips', 'toast', 'announcer', 'model-picker',
  'custom-model-input', 'theme-btn', 'panels-btn', 'shortcuts-overlay',
  'status-group', 'ping-latency', 'stream-badge', 'sessions-more', 'sidebar-count',
  'sandbox-badge', 'plan-panel'];
ids.forEach(id => { byId[id] = new FakeEl('div'); byId[id].id = id; });

globalThis.document = {
  // Auto-create unknown ids (mirrors ws.test.js): sessions.js runs top-level
  // wiring (search-clear, hamburger) at import time; a null element would
  // crash the module load. Memoized so repeated lookups return one node.
  getElementById: (id) => (byId[id] ||= Object.assign(new FakeEl('div'), { id })),
  createElement: (tag) => new FakeEl(tag),
  // Auto-create per selector (mirrors getElementById above): sessions.js
  // binds .new-session-btn and friends at import time; a null would crash
  // the module load. Memoized so every lookup for one selector returns the
  // same node.
  querySelector: (sel) => (bySelector[sel] ||= new FakeEl('div')),
  querySelectorAll: (sel) => (bySelector[sel] ? [bySelector[sel]] : []),
  addEventListener: () => {},
  body: new FakeEl('body'),
  activeElement: null,
  hidden: false,
};
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.window = globalThis;
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
globalThis.fetch = async () => ({ ok: true, headers: { get: () => null }, text: async () => '', json: async () => ({ messages: [] }) });
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

const S = (await import('./state.js')).S;
const render = await import('./render.js');
const input = await import('./input.js');
const sessions = await import('./sessions.js');
const ws = await import('./ws.js');
const approvals = await import('./approvals.js');
const health = await import('./health.js');
const plan = await import('./plan.js');

function deliver(event) {
  S.ws.onmessage({ data: JSON.stringify(event) });
}

function collectText(el, acc = []) {
  if (!el) return acc;
  if (el.textContent) acc.push(el.textContent);
  for (const c of el.children) collectText(c, acc);
  return acc;
}

function systemMessages() {
  return byId.messages.querySelectorAll('.msg.system');
}

beforeEach(() => {
  ws.connect();
  approvalsTeardown();
  byId.messages.children.length = 0;
  // Render-module singleton state survives the DOM wipe — stale references
  // make addSubagentGroup early-return and orphan the next group.
  render.resetTurnState();
  S.subagentGroup = null;
  S.currentToolBlock = null;
  S.currentTurnId = null;
  S.busy = false;
  render.hideLoading();
  plan.stopPlanLiveIfIdle();
  S.attachedFiles.length = 0;
  promptReset();
});

function approvalsTeardown() {
  S.approvalQueue.length = 0;
  S.activeApprovalId = null;
  if (S.activeApprovalCard) { S.activeApprovalCard.remove(); S.activeApprovalCard = null; }
}

function promptReset() {
  byId.prompt.value = '';
  byId.prompt.disabled = false;
}

// ── F-B6: corrupt odek_history falls back to [] and the key is purged. ──
// RED before the fix: JSON.parse threw at state.js module load, crashing
// the whole bundle import ("Unexpected token … in JSON").
test('corrupt odek_history falls back to [] and purges the corrupt key', () => {
  assert.deepEqual(S.history, [], 'fallback to empty history');
  assert.equal(store.has('odek_history'), false, 'corrupt key purged');
  // The array must remain live-binding usable.
  S.history.push('x');
  assert.equal(S.history.length, 1);
  S.history.length = 0;
});

// ── F-B2: guards before clearAttachedFiles. ──
test('busy send queues the prompt and captures attachments', () => {
  S.attachedFiles.push({ name: 'a.txt', size: 1, content: 'x' });
  byId.prompt.value = 'hello';
  S.busy = true;
  S.promptQueue.length = 0;
  input.send();
  assert.equal(S.promptQueue.length, 1, 'busy send queues instead of dropping');
  assert.equal(S.attachedFiles.length, 0, 'attachments move onto the queued item');
  assert.equal(S.promptQueue[0].attachments.length, 1);
  S.promptQueue.length = 0;
  S.busy = false;
  S.attachedFiles.push({ name: 'b.txt', size: 1, content: 'y' });
  byId.prompt.value = 'hello';
  S.ws = null;
  input.send();
  assert.equal(S.attachedFiles.length, 1, 'attachments survive a socket-rejected send');
});

// ── F-A2: mid-run session switch / new session guard. ──
test('newSession mid-run asks for confirmation and cancels via the stop control', () => {
  S.busy = true;
  let confirmMsg = '';
  globalThis.confirm = (msg) => { confirmMsg = msg; return true; };
  const cancels = [];
  byId['cancel-btn'].addEventListener('click', () => cancels.push(1));
  sessions.newSession();
  assert.match(confirmMsg, /still running/, 'the user is asked before tearing down a live turn');
  assert.equal(cancels.length, 1, 'cancel goes through the same control as the stop button');
  assert.equal(S.busy, false, 'switch proceeds after confirmed cancel');
});

test('declining the mid-run confirm is a full no-op for newSession', () => {
  S.busy = true;
  byId.messages.appendChild(new FakeEl('div'));
  const before = byId.messages.children.length;
  globalThis.confirm = () => false;
  let cancels = 0;
  byId['cancel-btn'].addEventListener('click', () => { cancels++; });
  sessions.newSession();
  assert.equal(S.busy, true, 'S.busy must never be silently forced off');
  assert.equal(cancels, 0, 'no cancel fired on decline');
  assert.equal(byId.messages.children.length, before, 'transcript not wiped on decline');
});

test('loadAndRenderSession mid-run declines to a no-op without fetching', async () => {
  S.busy = true;
  globalThis.confirm = () => false;
  let fetched = 0;
  globalThis.fetch = async () => { fetched++; return { ok: true, headers: { get: () => null }, text: async () => '', json: async () => ({ messages: [] }) }; };
  await sessions.loadAndRenderSession('sid-1');
  assert.equal(fetched, 0, 'declined switch must not even bootstrap the session');
  assert.equal(S.busy, true);
});

test('loadAndRenderSession mid-run proceeds after confirmed cancel', async () => {
  S.busy = true;
  globalThis.confirm = () => true;
  let cancels = 0;
  byId['cancel-btn'].addEventListener('click', () => { cancels++; });
  await sessions.loadAndRenderSession('sid-2');
  assert.equal(cancels, 1, 'run cancelled through the stop control');
  assert.equal(S.sessionId, 'sid-2', 'switch proceeded after the confirmed cancel');
});

// ── F-A3: reconnect unbricks the input; first connect stays silent. ──
test('reconnect resets busy, re-enables the prompt, and tells the user', () => {
  const sock1 = S.ws;
  sock1.onopen(); // first connect — silent
  assert.equal(systemMessages().length, 0, 'no restore notice on the first connect');
  assert.equal(S.busy, false);

  // Simulate a turn in flight when the socket dies.
  S.busy = true;
  byId.prompt.disabled = true;
  sock1.onopen(); // same handler shape a reconnecting socket gets
  assert.equal(S.busy, false, 'busy reset on reconnect');
  assert.equal(byId.prompt.disabled, false, 'prompt re-enabled on reconnect');
  const msgs = systemMessages();
  assert.ok(msgs.length >= 1, 'restore notice appended');
  assert.match(collectText(msgs[msgs.length - 1]).join(' '), /Connection restored/);
  health.stopHeartbeat(); // don't leak the heartbeat interval into the suite
});

// ── F-B1: delegate_tasks tool_result must not route into other tool blocks. ──
test('delegate_tasks tool_result completes the group without touching other blocks', () => {
  deliver({ type: 'tool_call', name: 'shell', data: '"ls"' });
  deliver({
    type: 'tool_call', name: 'delegate_tasks',
    data: JSON.stringify({ tasks: [{ goal: 'g0' }] }),
  });
  deliver({ type: 'tool_result', name: 'delegate_tasks', data: 'SECRET-DELEGATE-PAYLOAD' });

  const shellBlocks = byId.messages.querySelectorAll('.tool-block');
  assert.ok(shellBlocks.length >= 1, 'shell tool block rendered');
  const text = collectText(byId.messages).join(' ');
  assert.ok(!text.includes('SECRET-DELEGATE-PAYLOAD'),
    'delegate_tasks result must not leak into other tool blocks — the subagent group renders it');
  const group = byId.messages.querySelector('.subagent-group');
  assert.ok(group, 'subagent group present');
});

function spine() {
  return byId.messages.children.map((c) => {
    if (c.classList.contains('thinking-block')) return 'thinking';
    if (c.classList.contains('tool-block')) return 'tool';
    if (c.classList.contains('subagent-group')) return 'subagent';
    if (c.classList.contains('approval-card')) return 'approval';
    if (c.classList.contains('msg') && c.classList.contains('assistant')) return 'answer';
    if (c.classList.contains('msg') && c.classList.contains('user')) return 'user';
    return c.className || c.tagName;
  });
}

// The model emits answer tokens in the same LLM message as tool_calls.
// token_delta must not win the append race — live spine is thinking → tools → answer.
test('token then tool_call paints tools before the answer', () => {
  deliver({ type: 'turn_started', turn_id: 't-order-1' });
  deliver({ type: 'token_delta', turn_id: 't-order-1', content: 'Running.' });
  deliver({ type: 'tool_call', turn_id: 't-order-1', name: 'shell', data: '{"command":"echo hi"}' });
  assert.deepEqual(spine(), ['tool', 'answer']);
});

test('thinking, token, tool_call stays thinking → tools → answer', () => {
  deliver({ type: 'turn_started', turn_id: 't-order-2' });
  deliver({ type: 'thinking_delta', turn_id: 't-order-2', content: 'plan' });
  deliver({ type: 'token_delta', turn_id: 't-order-2', content: 'Running.' });
  deliver({ type: 'tool_call', turn_id: 't-order-2', name: 'read_file', data: '{"path":"a.go"}' });
  assert.deepEqual(spine(), ['thinking', 'tool', 'answer']);
});

test('late first thinking slides in front of tools that raced ahead', () => {
  deliver({ type: 'turn_started', turn_id: 't-order-3' });
  deliver({ type: 'tool_call', turn_id: 't-order-3', name: 'shell', data: '{}' });
  deliver({ type: 'thinking_delta', turn_id: 't-order-3', content: 'late' });
  assert.deepEqual(spine(), ['thinking', 'tool']);
});

test('a second iteration keeps answer last: think → tool → think → tool → answer', () => {
  deliver({ type: 'turn_started', turn_id: 't-order-4' });
  deliver({ type: 'thinking_delta', turn_id: 't-order-4', content: 'one' });
  deliver({ type: 'token_delta', turn_id: 't-order-4', content: 'Running 1.' });
  deliver({ type: 'tool_call', turn_id: 't-order-4', name: 'shell', data: '{}' });
  render.endThinking();
  deliver({ type: 'thinking_delta', turn_id: 't-order-4', content: 'two' });
  deliver({ type: 'token_delta', turn_id: 't-order-4', content: 'Running 2.' });
  deliver({ type: 'tool_call', turn_id: 't-order-4', name: 'read_file', data: '{}' });
  assert.deepEqual(spine(), ['thinking', 'tool', 'thinking', 'tool', 'answer']);
});

test('latest assistant reply is never folded; the previous long one is', () => {
  render.addMessage('assistant', 'first long answer');
  const first = byId.messages.children.find((c) => c.classList.contains('assistant'));
  first.querySelector('.content').scrollHeight = 800;
  render.addMessage('assistant', 'second long answer');
  const all = byId.messages.children.filter((c) => c.classList.contains('assistant'));
  assert.equal(all.length, 2);
  assert.ok(all[0].querySelector('.bubble').classList.contains('collapsible'), 'older reply folded');
  assert.ok(all[0].querySelector('.collapse-toggle'), 'Show more on the older reply');
  assert.equal(all[1].querySelector('.bubble').classList.contains('collapsible'), false, 'latest stays open');
  assert.equal(all[1].querySelector('.collapse-toggle'), null, 'no fold on the latest');
});

// ── F-C1: the dead .sa-stop control gets its delegation arm. ──
test('.sa-stop click delegates to requestSubagentStop for that card', () => {
  render.addSubagentGroup(JSON.stringify({ tasks: [{ goal: 'g0' }, { goal: 'g1' }] }));
  render.updateSubagentState({ task_idx: 1, phase: 'started', status: 'running', task_id: 'tid-7' });
  const sent = [];
  S.onSubagentStop = (taskID) => sent.push(taskID);

  const grid = byId.messages.querySelector('#sa-grid');
  const cards = grid.querySelectorAll('.subagent-card');
  const stopBtn = cards[1].querySelector('.sa-stop');
  assert.equal(stopBtn.disabled, false, 'armed once the task_id is known');

  stopBtn.dispatch('click'); // bubbles to the messagesEl delegation switch
  assert.deepEqual(sent, ['tid-7'], 'requestSubagentStop called with the card task');
  assert.equal(cards[1].dataset.stopping, '1', 'card marked stopping');
  assert.equal(cards[1].querySelector('.sa-stop').disabled, true, 'button shows its disabled state');
  assert.equal(cards[1].querySelector('.sa-status').textContent, 'stopping…', 'status shows stopping…');
});

// ── F-A1: approval deadline stamping, countdown, expiry sweep. ──
test('approval_request without timeout_seconds stamps the 60s default deadline', () => {
  deliver({ type: 'approval_request', id: 'apr-60', risk: 'local_write', command: 'echo hi', allow_trust: true });
  assert.equal(S.activeApprovalId, 'apr-60');
  const countdown = S.activeApprovalCard.querySelector('.ac-deadline');
  assert.ok(countdown, 'countdown element rendered on the card');
  assert.match(countdown.textContent, /expires in 60s/, 'absent frame field falls back to 60s');
  assert.equal(countdown.classList.contains('urgent'), false, 'not urgent with a fresh deadline');
});

test('approval_request honors the frame timeout_seconds for the countdown', () => {
  deliver({ type: 'approval_request', id: 'apr-25', risk: 'local_write', command: 'echo hi', allow_trust: true, timeout_seconds: 25 });
  assert.match(S.activeApprovalCard.querySelector('.ac-deadline').textContent, /expires in 25s/);
});

// Polls cond until truthy (20 ms cadence). Replaces fixed real-time sleeps,
// which raced the 1s sweep interval and the 1.5s friction gate under CI load
// (ui-js failed 2/3 runs on 2026-09-02).
async function waitFor(cond, label, timeoutMs = 15000) {
  const start = Date.now();
  for (;;) {
    if (cond()) return;
    if (Date.now() - start > timeoutMs) {
      assert.ok(cond(), `timed out after ${timeoutMs}ms waiting for: ${label}`);
    }
    await new Promise((r) => setTimeout(r, 20));
  }
}

test('sweep auto-closes only the expired card and shows the next one', async () => {
  deliver({ type: 'approval_request', id: 'apr-fast', risk: 'safe', command: 'sleep', allow_trust: true, timeout_seconds: 1 });
  deliver({ type: 'approval_request', id: 'apr-slow', risk: 'safe', command: 'echo', allow_trust: true });
  assert.equal(S.activeApprovalId, 'apr-fast');

  // The sweep ticks on a real 1s interval — wait for the state, not the clock.
  await waitFor(() => S.activeApprovalId === 'apr-slow', 'expired head autoclosed, next card shown');

  assert.equal(S.activeApprovalId, 'apr-slow', 'expired head autoclosed, next card shown');
  assert.equal(S.approvalQueue.length, 1, 'the unexpired request survives the sweep');

  // approval_expired for an id the sweep already closed is a no-op — the
  // card must not resurrect.
  deliver({ type: 'approval_expired', id: 'apr-fast' });
  assert.equal(S.activeApprovalId, 'apr-slow');

  deliver({ type: 'approval_expired', id: 'apr-slow' });
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.approvalQueue.length, 0);
  assert.equal(S.activeApprovalCard, null);

  // A late ack for an expired id must not resurrect anything either.
  deliver({ type: 'approval_ack', id: 'apr-fast' });
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.activeApprovalCard, null);
});

test('approval_expired closes the active card idempotently (duplicate frames included)', () => {
  deliver({ type: 'approval_request', id: 'apr-x', risk: 'safe', command: 'one', allow_trust: true });
  deliver({ type: 'approval_expired', id: 'apr-x' });
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.activeApprovalCard, null);

  deliver({ type: 'approval_expired', id: 'apr-x' }); // duplicate frame — no-op
  deliver({ type: 'approval_ack', id: 'apr-x' });     // late ack — no-op
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.approvalQueue.length, 0);
});

// ── F-B3: approval delivery is guarded by the socket state. ──
test('approval sent while the socket is down keeps the card and reports the failure', () => {
  deliver({ type: 'approval_request', id: 'apr-d', risk: 'local_write', command: 'echo hi', allow_trust: true });
  const sock = S.ws;
  sock.readyState = 3; // CLOSED
  S.activeApprovalCard.querySelector('.approve').dispatch('click');
  assert.deepEqual(sock.sent, [], 'no approval_response may go out on a dead socket');
  assert.equal(S.activeApprovalId, 'apr-d', 'card stays up — the decision was not delivered');
  assert.equal(S.approvalQueue.length, 1, 'queue untouched on failed delivery');
  const notices = systemMessages();
  assert.ok(notices.length >= 1, 'the failure must be surfaced to the user');
  assert.match(collectText(notices[notices.length - 1]).join(' '), /approval not delivered/);
});

test('approval sent on a live socket still delivers and closes the card', () => {
  deliver({ type: 'approval_request', id: 'apr-u', risk: 'local_write', command: 'echo hi', allow_trust: true });
  const sock = S.ws;
  S.activeApprovalCard.querySelector('.approve').dispatch('click');
  // FakeWebSocket records the raw JSON string — parse before comparing.
  assert.deepEqual(JSON.parse(sock.sent[0]), { type: 'approval_response', id: 'apr-u', action: 'approve' });
  assert.equal(S.activeApprovalId, null);
  assert.equal(S.approvalQueue.length, 0);
});

// ── error frames tear down approvals (parity with cancelled). ──
test('error frame clears pending approvals like cancelled does', () => {
  deliver({ type: 'approval_request', id: 'apr-e', risk: 'safe', command: 'one', allow_trust: true });
  deliver({ type: 'error', message: 'run failed' });
  assert.equal(S.approvalQueue.length, 0);
  assert.equal(S.activeApprovalCard, null);
  assert.equal(S.activeApprovalId, null);
});

// ── F-B4: the global 'a' shortcut honors the friction 1.5s cool-down. ──
test('friction approve via keyboard path waits out the 1.5s cool-down', () => {
  deliver({ type: 'approval_request', id: 'apr-f', risk: 'system_write', command: 'rm x', friction: true, allow_trust: false });
  const card = S.activeApprovalCard;
  assert.ok(card, 'friction card shown');
  card.querySelector('.ac-friction-input').value = 'approve';

  const sock = S.ws;
  const before = sock.sent.length;
  approvals.sendApproval('approve'); // inside the cool-down window
  assert.equal(sock.sent.length, before, 'blocked within the 1.5s window');
  assert.equal(S.approvalQueue.length, 1, 'card not consumed by blocked send');

  card.dataset.shownAt = String(Date.now() - 2000); // cool-down elapsed
  approvals.sendApproval('approve');
  assert.equal(sock.sent.length, before + 1, 'delivered after the window');
  assert.equal(S.approvalQueue.length, 0);
});

// ── F-B5: stale @-completion responses never render. ───
test('late @-completion response for changed input is dropped', async () => {
  const prompt = byId.prompt;
  prompt.value = '@se';
  prompt.selectionStart = 3;

  let resolveFetch;
  const oldFetch = globalThis.fetch;
  globalThis.fetch = () => new Promise((res) => { resolveFetch = res; });

  try {
    byId.prompt.dispatch('input');
    // Debounce is 150ms — wait it out before the fetch promise is issued.
    await new Promise((r) => setTimeout(r, 200));
    assert.ok(resolveFetch, 'completion fetch issued');

    // User keeps typing while the request is in flight.
    prompt.value = '@sea';
    prompt.selectionStart = 4;
    resolveFetch({ ok: true, json: async () => [{ id: 'f:sea', type: 'file', label: 'sea.txt', detail: '' }] });
    await new Promise((r) => setTimeout(r, 30));

    assert.equal(byId.completion.classList.contains('visible'), false, 'stale popup suppressed');
  } finally {
    globalThis.fetch = oldFetch;
    prompt.value = '';
  }
});

test('non-2xx @-completion response hides the popup instead of throwing', async () => {
  const prompt = byId.prompt;
  prompt.value = '@bad';
  prompt.selectionStart = 4;
  const oldFetch = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: false, status: 500, json: async () => ({ error: 'boom' }) });
  try {
    byId.prompt.dispatch('input');
    await new Promise((r) => setTimeout(r, 0));
    assert.equal(byId.completion.classList.contains('visible'), false, 'popup hidden on error');
  } finally {
    globalThis.fetch = oldFetch;
    prompt.value = '';
  }
});
