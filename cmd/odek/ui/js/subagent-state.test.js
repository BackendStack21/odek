// DOM-shim tests for the sub-agent card lifecycle (telemetry M4):
// subagent_state transitions must drive per-task card pills/status
// individually, and the batch result must fill details without
// overwriting per-task pills. Runs:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Mini-DOM: enough of a browser for render.js sub-agent cards ──

class ClassList {
  constructor(el) { this.el = el; }
  get set() { return new Set((this.el.className || '').split(/\s+/).filter(Boolean)); }
  add(...cs) { const s = this.set; cs.forEach(c => s.add(c)); this.el.className = [...s].join(' '); }
  remove(...cs) { const s = this.set; cs.forEach(c => s.delete(c)); this.el.className = [...s].join(' '); }
  contains(c) { return this.set.has(c); }
  toggle(c) { this.contains(c) ? this.remove(c) : this.add(c); }
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
    this.innerHTML = '';
    this._innerHTML = '';
    const shim = this;
    Object.defineProperty(this, 'innerHTML', {
      get() { return shim._innerHTML; },
      set(html) {
        shim._innerHTML = html;
        shim.children = [];
        // Minimal parser for the simple static markup render.js injects:
        // nested <div class="…" id="…"> with text content.
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
    this.title = '';
    this.scrollTop = 0;
    this.scrollHeight = 100;
    this.clientWidth = 50;
  }
  get id() { return this._id || ''; }
  set id(v) { this._id = v; }
  matches(sel) {
    if (sel.startsWith('.')) return this.classList.contains(sel.slice(1));
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    return this.tagName === sel.toUpperCase();
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
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  insertBefore(c, ref) {
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
  addEventListener() {}
}

const byId = {};
const ids = ['messages', 'prompt', 'send-btn', 'completion', 'ws-status', 'ws-dot',
  'model-label', 'session-list', 'sidebar-search', 'empty-state', 'cancel-btn',
  'scroll-bottom-btn', 'loading-skeleton', 'sidebar-overlay', 'file-input',
  'attach-btn', 'file-chips', 'think-btn'];
ids.forEach(id => { byId[id] = new FakeEl('div'); byId[id].id = id; });

globalThis.document = {
  getElementById: (id) => byId[id] || null,
  createElement: (tag) => new FakeEl(tag),
  querySelector: (sel) => (sel === '#messages' ? byId.messages : null),
  addEventListener: () => {},
  body: new FakeEl('body'),
};
globalThis.localStorage = (() => {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
})();
globalThis.window = globalThis;
if (!globalThis.navigator) globalThis.navigator = {};
if (!globalThis.navigator.clipboard) globalThis.navigator.clipboard = { writeText: async () => {} };
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

const S = (await import('./state.js')).S;
const render = await import('./render.js');

function cards() {
  const grid = byId.messages.querySelector('#sa-grid');
  return grid ? grid.querySelectorAll('.subagent-card') : [];
}

function delegateTwoTasks() {
  render.addSubagentGroup(JSON.stringify({ tasks: [{ goal: 'task alpha' }, { goal: 'task beta' }] }));
  const cs = cards();
  assert.equal(cs.length, 2, 'two cards expected after addSubagentGroup');
  return cs;
}

beforeEach(() => {
  byId.messages.children.length = 0;
  S.subagentGroup = null;
});

// ── started / active ──

test('started keeps the card running; active shows tool + step', () => {
  const cs = delegateTwoTasks();

  render.updateSubagentState({ task_idx: 0, phase: 'started', status: 'running' });
  assert.equal(cs[0].querySelector('.sa-status').textContent, 'running');
  assert.equal(cs[0].dataset.finalized, undefined);

  render.updateSubagentState({ task_idx: 0, phase: 'active', step: 3, tool: 'read_file' });
  assert.equal(cs[0].querySelector('.sa-status').textContent, '⟳ read_file · 3');

  // Sibling untouched.
  assert.equal(cs[1].querySelector('.sa-status').textContent, 'running');
});

test('finished flips only its own card and updates the wave header', () => {
  const cs = delegateTwoTasks();

  render.updateSubagentState({
    task_idx: 0, phase: 'finished', status: 'success',
    iterations: 4, tokens_used: 900, duration_seconds: 12.5,
  });

  assert.equal(cs[0].querySelector('.sa-status').textContent, 'done');
  assert.ok(cs[0].classList.contains('completed'), 'finished card carries completed class');
  assert.equal(cs[0].dataset.finalized, '1');
  assert.equal(cs[1].dataset.finalized, undefined, 'sibling not finalized');

  const header = byId.messages.querySelector('.sg-header');
  assert.ok(header.textContent.includes('1/2 complete'), 'header shows 1/2, got: ' + header.textContent);
});

test('failed finish marks the card error and opens details', () => {
  const cs = delegateTwoTasks();
  render.updateSubagentState({ task_idx: 1, phase: 'finished', status: 'timeout' });

  assert.ok(cs[1].classList.contains('error'), 'timeout card carries error class');
  assert.equal(cs[1].querySelector('.sa-icon').textContent, '✗');
  assert.equal(cs[1].querySelector('.sa-status').textContent, 'timeout');
  assert.equal(cs[1].dataset.finalized, '1');
});

test('state for an unknown task_idx is ignored', () => {
  delegateTwoTasks();
  // No throw — off-range index is a no-op.
  render.updateSubagentState({ task_idx: 9, phase: 'finished', status: 'success' });
});

// ── batch result interplay ──

test('batch result fills details without overwriting per-task pills', () => {
  const cs = delegateTwoTasks();
  render.updateSubagentState({ task_idx: 0, phase: 'finished', status: 'success', iterations: 2 });

  render.completeSubagents(
    '📋 Sub-agent results:\n\n' +
    '─── Task 1: alpha ───\n' +
    '{"status":"success","summary":"alpha summary","tokens_used":100,"iterations":2}\n\n' +
    '─── Task 2: beta ───\n' +
    '{"status":"error","summary":"beta blew up","tokens_used":50,"iterations":1}'
  );

  // Card 0: per-task pill (done) survives; details filled from the result.
  assert.equal(cs[0].querySelector('.sa-status').textContent, 'done');
  assert.ok(cs[0].classList.contains('completed'));
  // Card 1: never finalized by state — the batch result flips it to error.
  assert.equal(cs[1].querySelector('.sa-status').textContent, 'error');
  assert.equal(cs[1].querySelector('.sa-summary').textContent, 'beta blew up', 'error summary rendered');
  assert.equal(cs[1].dataset.finalized, '1');
});

test('finished without a goal-matching group is a no-op', () => {
  S.subagentGroup = null;
  render.updateSubagentState({ task_idx: 0, phase: 'finished', status: 'success' });
  assert.ok(true, 'no throw without an active group');
});

// ── stop button / per-sub-agent cancellation ──

test('running cards render a stop button, disabled until task_id arrives', () => {
  const cs = delegateTwoTasks();

  const stop0 = cs[0].querySelector('.sa-stop');
  assert.ok(stop0, 'stop button rendered on the card');
  assert.equal(stop0.disabled, true, 'disabled without a task_id (task may not have spawned yet)');

  render.updateSubagentState({ task_idx: 0, phase: 'started', status: 'running', task_id: 'tid-1' });
  assert.equal(cs[0].dataset.taskId, 'tid-1', 'card remembers the correlation id');
  assert.equal(cs[0].querySelector('.sa-stop').disabled, false, 'armed once task_id is known');

  // Sibling without a started record stays disarmed.
  assert.equal(cs[1].querySelector('.sa-stop').disabled, true);
});

test('requestSubagentStop sends the task id once and marks the card stopping', () => {
  const cs = delegateTwoTasks();
  render.updateSubagentState({ task_idx: 0, phase: 'started', status: 'running', task_id: 'tid-9' });

  const sent = [];
  S.onSubagentStop = (taskID) => sent.push(taskID);

  render.requestSubagentStop(0);
  assert.deepEqual(sent, ['tid-9'], 'stop request carries the correlation id');
  assert.equal(cs[0].dataset.stopping, '1', 'card flagged stopping');
  assert.equal(cs[0].querySelector('.sa-status').textContent, 'stopping…');
  assert.equal(cs[0].querySelector('.sa-stop').disabled, true, 'button disarmed while stopping');

  // Second click is suppressed — one request per card.
  render.requestSubagentStop(0);
  assert.deepEqual(sent, ['tid-9']);
});

test('requestSubagentStop is a no-op without task_id or on finalized cards', () => {
  const cs = delegateTwoTasks();
  const sent = [];
  S.onSubagentStop = (taskID) => sent.push(taskID);

  // No task_id yet.
  render.requestSubagentStop(0);
  assert.deepEqual(sent, []);

  // Finalized card.
  render.updateSubagentState({ task_idx: 1, phase: 'started', status: 'running', task_id: 'tid-2' });
  render.updateSubagentState({ task_idx: 1, phase: 'finished', status: 'success' });
  render.requestSubagentStop(1);
  assert.deepEqual(sent, []);
});

test('cancelled finish marks the card stopped and removes the stop button', () => {
  const cs = delegateTwoTasks();
  render.updateSubagentState({ task_idx: 1, phase: 'started', status: 'running', task_id: 'tid-2' });
  render.updateSubagentState({
    task_idx: 1, phase: 'finished', status: 'cancelled', duration_seconds: 3.2,
  });

  assert.equal(cs[1].querySelector('.sa-icon').textContent, '⊘');
  assert.equal(cs[1].querySelector('.sa-status').textContent, 'stopped');
  assert.ok(cs[1].classList.contains('stopped'), 'card carries the stopped class');
  assert.equal(cs[1].dataset.finalized, '1');
  assert.equal(cs[1].querySelector('.sa-stop'), null, 'stop button removed after finalize');
});

test('cancelled card keeps its pill when the batch result lands later', () => {
  const cs = delegateTwoTasks();
  render.updateSubagentState({ task_idx: 0, phase: 'started', status: 'running', task_id: 'tid-a' });
  render.updateSubagentState({ task_idx: 0, phase: 'finished', status: 'cancelled' });

  render.completeSubagents(
    '📋 Sub-agent results:\n\n' +
    '─── Task 1: alpha ───\n' +
    '{"status":"cancelled","summary":"stopped by user"}\n\n' +
    '─── Task 2: beta ───\n' +
    '{"status":"success","summary":"beta ok"}'
  );

  assert.equal(cs[0].querySelector('.sa-icon').textContent, '⊘', 'cancelled pill survives the batch result');
  assert.equal(cs[0].querySelector('.sa-status').textContent, 'stopped');
  assert.equal(cs[1].querySelector('.sa-status').textContent, 'done');
});
