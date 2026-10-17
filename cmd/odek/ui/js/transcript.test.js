// Transcript DOM budget: long sessions keep #messages bounded. Covers the
// unit-based trim (rows inside a long turn count), live-work protection,
// the reading-up deferral, windowed session-history load with
// "Show earlier", lazy tool-result bodies, one-time collapse measurement,
// and the reasoning / sub-agent log row caps. Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

class ClassList {
  constructor(el) { this.el = el; }
  get set() { return new Set((this.el.className || '').split(/\s+/).filter(Boolean)); }
  add(...cs) { const s = this.set; cs.forEach(c => s.add(c)); this.el.className = [...s].join(' '); }
  remove(...cs) { const s = this.set; cs.forEach(c => s.delete(c)); this.el.className = [...s].join(' '); }
  contains(c) { return this.set.has(c); }
  toggle(c, force) {
    const want = force === undefined ? !this.contains(c) : !!force;
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
    this.clientHeight = 50;
    this._scrollHeight = 100;
    this._listeners = {};
    this._attrs = {};
    this._innerHTML = '';
    this.style = {};
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
          if (tok.startsWith('</')) { if (stack.length > 1) stack.pop(); continue; }
          if (tok.startsWith('<')) {
            const el = new FakeEl((tok.match(/^<([a-z-]+)/) || [null, 'div'])[1]);
            const cls = tok.match(/class="([^"]*)"/);
            if (cls) el.className = cls[1];
            const idm = tok.match(/id="([^"]*)"/);
            if (idm) el.id = idm[1];
            parent.appendChild(el);
            if (!/^<(br|input|img)\b/.test(tok)) stack.push(el);
            continue;
          }
          parent.textContent += tok;
        }
      },
    });
  }
  get scrollHeight() { return this._scrollHeight; }
  set scrollHeight(v) { this._scrollHeight = v; }
  get id() { return this._id || ''; }
  set id(v) { this._id = v; }
  get isConnected() { return true; }
  setAttribute(k, v) { this._attrs[k] = String(v); }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  removeAttribute(k) { delete this._attrs[k]; }
  matches(sel) {
    if (sel.startsWith('.')) return sel.slice(1).split('.').every((c) => c && this.classList.contains(c));
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    if (sel.startsWith('[')) return false;
    return this.tagName === sel.toUpperCase();
  }
  closest(sel) {
    for (let el = this; el; el = el.parentNode) if (el.matches && el.matches(sel)) return el;
    return null;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  querySelectorAll(sel) {
    const out = [];
    const parts = sel.split(',').map(s => s.trim());
    const walk = (el) => {
      for (const c of el.children) {
        if (parts.some(p => c.matches(p))) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  get lastElementChild() { return this.children[this.children.length - 1] || null; }
  appendChild(c) {
    if (c.parentNode) c.remove();
    c.parentNode = this;
    this.children.push(c);
    return c;
  }
  get parentElement() { return this.parentNode; }
  append(...cs) { cs.forEach(c => this.appendChild(c)); }
  insertAdjacentElement(_pos, node) { return this.appendChild(node); }
  insertBefore(c, ref) {
    if (c.parentNode) c.remove();
    c.parentNode = this;
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i >= 0) this.children.splice(i, 0, c); else this.children.push(c);
    return c;
  }
  remove() {
    if (!this.parentNode) return;
    const i = this.parentNode.children.indexOf(this);
    if (i >= 0) this.parentNode.children.splice(i, 1);
    this.parentNode = null;
  }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  dispatch(type, ev = {}) {
    for (let el = this; el; el = el.parentNode) {
      for (const fn of [...(el._listeners[type] || [])]) {
        fn({ target: this, preventDefault() {}, stopPropagation() {}, ...ev });
      }
    }
  }
  focus() {}
  click() { if (!this.disabled) this.dispatch('click'); }
}

const byId = {};
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.sessionStorage = { getItem: () => null, setItem() {}, removeItem() {} };
globalThis.document = {
  getElementById: (id) => (byId[id] ||= Object.assign(new FakeEl('div'), { id })),
  createElement: (tag) => new FakeEl(tag),
  querySelector: () => new FakeEl('div'),
  querySelectorAll: () => [],
  addEventListener: () => {},
  body: new FakeEl('body'),
  activeElement: null,
  hidden: false,
};
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.window = globalThis;
globalThis.WebSocket = class { static get OPEN() { return 1; } };
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

const S = (await import('./state.js')).S;
const render = await import('./render.js');
const transcript = await import('./transcript.js');
const messages = byId.messages;

// Height follows the rendered row count so scroll compensation is observable.
Object.defineProperty(messages, 'scrollHeight', { get: () => transcript.countUnits() * 10 });

function rows() {
  const out = [];
  for (const el of messages.children) {
    if (el.classList.contains('turn-stream')) out.push(...el.children);
    else if (!el.classList.contains('transcript-trim')) out.push(el);
  }
  return out;
}
function textOf(el) {
  return [el.textContent, ...el.children.map(textOf)].join(' ');
}
function marker() { return messages.querySelector('.transcript-trim'); }
function finishedTool(i) {
  render.addToolCall('shell', JSON.stringify({ command: 'echo ' + i }), 'c' + i);
  render.addToolResult('shell', 'out ' + i, 'c' + i, 'completed');
}
// A session of `turns` turns, each: user prompt → assistant reply with one
// tool call → tool result → final reply. Five rows per turn.
function session(turns) {
  const list = [{ role: 'system', content: '' }];
  for (let t = 0; t < turns; t++) {
    list.push({ role: 'user', content: 'q' + t, turn_id: 't' + t });
    list.push({ role: 'assistant', content: 'looking ' + t, reasoning_content: 'think ' + t, tool_calls: [{ id: 'k' + t, function: { name: 'shell', arguments: '{}' } }], turn_id: 't' + t });
    list.push({ role: 'tool', tool_call_id: 'k' + t, content: 'result ' + t, tool_outcome: 'completed' });
    list.push({ role: 'assistant', content: 'answer ' + t, turn_id: 't' + t });
  }
  return list;
}
function hidxOrder() {
  const seen = [];
  rows().forEach((r) => { const h = Number(r.dataset.hidx); if (seen[seen.length - 1] !== h) seen.push(h); });
  return seen;
}

beforeEach(() => {
  messages.children.length = 0;
  messages.scrollTop = 0;
  render.resetTurnState();
  transcript.resetTranscriptTrim();
  S.subagentGroup = null;
  S.currentToolBlock = null;
  S.currentTurnId = null;
  S.loadingEl = null;
  S.busy = false;
  S.stickBottom = true;
  S.sessionId = 'sess-1';
  S.recordResult = undefined;
});

test('rows inside one long turn count against the budget and the oldest are trimmed', () => {
  for (let i = 0; i < transcript.MAX_UNITS + 50; i++) finishedTool(i);
  assert.equal(messages.children.filter(c => c.classList.contains('turn-stream')).length, 1, 'one turn stream');
  assert.equal(transcript.countUnits(), transcript.MAX_UNITS, 'trimmed to the budget');
  const first = rows()[0];
  assert.equal(first.dataset.callId, 'c50', 'the oldest steps left first');
  assert.ok(marker(), 'a trim marker tells the operator rows are hidden');
  assert.equal(marker().querySelector('.tt-more').dataset.action, 'reload');
});

test('live work is never trimmed: pending tools and the streaming answer survive', () => {
  render.addToolCall('shell', '{"command":"slow"}', 'pending');
  for (let i = 0; i < transcript.MAX_UNITS + 20; i++) finishedTool(i);
  const pending = rows().find(r => r.dataset.callId === 'pending');
  assert.ok(pending, 'the step still waiting for its result stays');
  assert.equal(S.toolBlockQueues.get('pending')[0], pending);
  render.addToolResult('shell', 'finally', 'pending', 'completed');
  assert.equal(pending.dataset.outcome, 'completed', 'its late result still lands');
});

test('while the operator reads up the trim waits for the hard ceiling and holds the view', () => {
  S.stickBottom = false;
  for (let i = 0; i < transcript.MAX_UNITS + 40; i++) finishedTool(i);
  assert.equal(transcript.countUnits(), transcript.MAX_UNITS + 40, 'nothing trimmed below the hard ceiling');
  assert.equal(marker(), null);
  messages.scrollTop = 1000;
  for (let i = transcript.countUnits(); i <= transcript.HARD_UNITS; i++) finishedTool('x' + i);
  assert.equal(transcript.countUnits(), transcript.MAX_UNITS, 'the hard ceiling trims back to the budget');
  assert.ok(messages.scrollTop < 1000, 'scroll position compensates for rows removed above the view');
});

test('marker reload waits for the running turn', () => {
  S.busy = true;
  for (let i = 0; i < transcript.MAX_UNITS + 5; i++) finishedTool(i);
  const btn = marker().querySelector('.tt-more');
  assert.equal(btn.disabled, true, 'no reload mid-turn');
  let reloads = 0;
  S.reloadSession = () => { reloads++; };
  S.busy = false;
  render.endStream('completed');
  assert.equal(btn.disabled, false, 'turn end re-enables reload');
  btn.click();
  assert.equal(reloads, 1);
});

test('historyWindowStart snaps to a turn start and falls back inside one oversized turn', () => {
  const list = session(100);
  const start = render.historyWindowStart(list, list.length, 20);
  assert.equal(list[start].role, 'user', 'window opens on a turn');
  const units = list.slice(start).reduce((n, m) => n + render.historyUnits(m), 0);
  assert.ok(units <= 20 && units >= 10, 'window within budget: ' + units);

  const huge = [{ role: 'user', content: 'go' }, ...Array.from({ length: 50 }, (_, i) => ({ role: 'assistant', tool_calls: [{ id: 'h' + i, function: { name: 'shell', arguments: '{}' } }] }))];
  const s2 = render.historyWindowStart(huge, huge.length, 10);
  assert.equal(huge.length - s2, 10, 'a turn larger than the budget renders its newest messages');
  assert.equal(render.historyWindowStart(huge.slice(0, 3), 3, 10), 0, 'small sessions render whole');
});

test('a long session loads only its newest window; Show earlier restores the rest in order', () => {
  const list = session(120); // 600 rows
  render.renderSessionHistory(list);
  assert.ok(transcript.countUnits() <= transcript.HISTORY_UNITS, 'initial render bounded: ' + transcript.countUnits());
  const m = marker();
  assert.ok(m, 'marker offers the earlier messages');
  assert.equal(m.querySelector('.tt-more').dataset.action, 'earlier');
  assert.equal(textOf(rows()[rows().length - 1]).includes('answer 119'), true, 'newest message rendered');

  let guard = 0;
  while (marker() && guard++ < 20) marker().querySelector('.tt-more').click();
  assert.equal(marker(), null, 'marker gone once everything is shown');
  const order = hidxOrder();
  const expected = list.map((msg, i) => (render.historyUnits(msg) ? i : -1)).filter(i => i >= 0);
  assert.deepEqual(order, expected, 'every message rendered once, in transcript order');
  assert.equal(messages.children.filter(c => c.classList.contains('turn-stream')).length, 120, 'split turn streams rejoined');
});

test('Show earlier keeps the visible rows in place', () => {
  render.renderSessionHistory(session(60));
  messages.scrollTop = 5;
  const before = transcript.countUnits();
  render.showEarlierHistory();
  const added = transcript.countUnits() - before;
  assert.ok(added > 0);
  assert.equal(messages.scrollTop, 5 + added * 10, 'scroll offset grows by the inserted height');
});

test('history rows trimmed by the budget return to the backlog as whole messages', () => {
  render.renderSessionHistory(session(40)); // 200 rows, window of ~150
  for (let i = 0; i < transcript.MAX_UNITS; i++) finishedTool(i);
  const state = transcript.trimState();
  assert.equal(state.backlog.end, state.backlog.messages.length, 'every history row was trimmed back to the backlog');
  assert.equal(rows().some(r => r.dataset.hidx), false);

  messages.children.length = 0;
  transcript.resetTranscriptTrim();
  const list = session(40);
  render.renderSessionHistory(list);
  for (let i = 0; i < 160; i++) finishedTool(i);
  const first = rows().find(r => r.dataset.hidx);
  const end = transcript.trimState().backlog.end;
  assert.equal(Number(first.dataset.hidx), end, 'backlog resumes at the first remaining message');
  const same = rows().filter(r => r.dataset.hidx === first.dataset.hidx);
  const expectedRows = render.historyUnits(list[end]);
  assert.equal(same.length, expectedRows, 'no message is left half-rendered');
});

test('tool result bodies render on first expand, once', () => {
  render.addToolCall('shell', '{"command":"ls"}', 'lazy');
  const block = S.currentToolBlock;
  const inspect = document.createElement('button');
  inspect.className = 'result-inspect';
  S.recordResult = (b) => b.querySelector('.tb-body').appendChild(inspect);
  render.addToolResult('shell', 'a\nb\nc', 'lazy', 'completed');
  assert.equal(block.querySelector('.tb-result'), null, 'collapsed result has no rendered body');
  const header = block.querySelector('.tb-header');
  header.click();
  const body = block.querySelector('.tb-body');
  assert.ok(body.classList.contains('open'));
  const results = block.querySelectorAll('.tb-result');
  assert.equal(results.length, 1, 'rendered on expand');
  assert.ok(body.children.indexOf(results[0]) < body.children.indexOf(inspect), 'result sits before the inspect action');
  header.click(); header.click();
  assert.equal(block.querySelectorAll('.tb-result').length, 1, 'never rendered twice');
});

test('older answers are measured for folding once, not on every new reply', () => {
  let reads = 0;
  render.addMessage('assistant', 'first');
  const content = messages.querySelector('.msg.assistant').querySelector('.content');
  Object.defineProperty(content, 'scrollHeight', { get: () => { reads++; return 10; } });
  for (let i = 0; i < 5; i++) render.addMessage('assistant', 'reply ' + i);
  assert.equal(reads, 1, 'scrollHeight read once for the older bubble');
});

test('a reasoning block keeps its newest rows behind a trim note', () => {
  S.currentTurnId = 't-think';
  const total = render.MAX_THINKING_LINES + 25;
  for (let i = 0; i < total; i++) render.streamThinking((i ? '\n' : '') + 'line ' + i);
  const host = messages.querySelector('.thinking-content');
  const lines = host.querySelectorAll('.thinking-line');
  assert.equal(lines.length, render.MAX_THINKING_LINES);
  assert.equal(lines[lines.length - 1].textContent, 'line ' + (total - 1));
  assert.match(host.children[0].textContent, /25 earlier lines not shown/);

  messages.children.length = 0;
  render.renderSessionHistory([{ role: 'assistant', reasoning_content: Array.from({ length: total }, (_, i) => 'h' + i).join('\n') }]);
  const hist = messages.querySelector('.thinking-content');
  assert.equal(hist.querySelectorAll('.thinking-line').length, render.MAX_THINKING_LINES);
  assert.match(hist.children[0].textContent, /25 earlier lines not shown/);
});

test('a sub-agent chip log keeps only its recent lines', () => {
  render.addSubagentGroup(JSON.stringify({ tasks: [{ goal: 'g' }] }));
  for (let i = 0; i < render.MAX_SUBAGENT_LOG_LINES + 30; i++) render.appendSubagentLog(0, { event: 'tool_call', name: 'shell', data: 'n' + i });
  const log = messages.querySelector('.sa-log');
  assert.equal(log.children.length, render.MAX_SUBAGENT_LOG_LINES);
  assert.match(log.children[log.children.length - 1].textContent, /n229/);
});

test('results outside the loaded window still reach the Activity panel in order; Show earlier does not re-record', () => {
  const recorded = [];
  S.recordResult = (block, output) => recorded.push({ output, attached: !!block.parentNode, outcome: block.dataset.outcome });
  const list = session(60);
  render.renderSessionHistory(list);
  assert.equal(recorded.length, 60, 'every historical result recorded once');
  assert.deepEqual(recorded.map(r => r.output), list.filter(m => m.role === 'tool').map(m => m.content), 'transcript order');
  assert.ok(recorded.slice(0, 10).every(r => !r.attached && r.outcome === 'completed'), 'pre-window results use detached stubs');
  render.showEarlierHistory();
  assert.equal(recorded.length, 60, 'earlier windows are not recorded again');
});

test('Activity results repaint once per frame and keep a bounded byte total', async () => {
  await import('./workspace.js');
  const list = byId['output-list'];
  let paints = 0;
  const origAppend = list.appendChild.bind(list);
  list.appendChild = (c) => { if (c.classList.contains('output-item')) paints++; return origAppend(c); };
  S.clearResults();
  await new Promise((r) => setTimeout(r, 5));
  paints = 0;
  for (let i = 0; i < 5; i++) {
    const block = document.createElement('div');
    block.dataset.toolName = 'shell';
    block.dataset.toolArgs = '{}';
    S.recordResult(block, 'output ' + i);
  }
  assert.equal(paints, 0, 'no synchronous repaint per result');
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(paints, 5, 'one coalesced repaint lists every result');
  S.clearResults();
});

test('the trim marker reload is refused mid-turn and reloads the session when idle', async () => {
  await import('./sessions.js');
  const fetched = [];
  globalThis.fetch = async (url) => { fetched.push(String(url)); return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ messages: [] }), text: async () => '' }; };
  S.busy = true;
  S.reloadSession();
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(fetched.length, 0, 'no reload while a turn runs');
  S.busy = false;
  S.reloadSession();
  await new Promise((r) => setTimeout(r, 5));
  assert.ok(fetched.some((u) => u.includes('sess-1')), 'idle reload fetches the persisted session: ' + fetched.join(','));
});

test('steps settled by a stopped or interrupted turn become trimmable', () => {
  render.addToolCall('shell', '{"command":"sleep"}', 'orphan');
  const block = S.currentToolBlock;
  render.requestTurnStop();
  assert.equal(block.dataset.outcome, 'running', 'a stopping step can still receive its result');
  render.endStream('cancelled');
  assert.equal(block.dataset.outcome, 'unknown', 'after the turn ends nothing can arrive');
  for (let i = 0; i < transcript.MAX_UNITS + 5; i++) finishedTool(i);
  assert.equal(block.parentNode, null, 'the settled step is trimmed like any finished one');
});

test('Show earlier steps keep their inspect action without re-recording', () => {
  const recorded = [];
  const inspected = [];
  S.recordResult = (block, output) => recorded.push(output);
  S.attachInspect = (block, output) => inspected.push(output);
  render.renderSessionHistory(session(60));
  const n = recorded.length;
  render.showEarlierHistory();
  assert.equal(recorded.length, n, 'not recorded again');
  assert.ok(inspected.length > 0, 'inspect action attached to re-rendered steps');
  S.attachInspect = undefined;
});

test('holdView corrects only what browser scroll anchoring left over', () => {
  const anchor = new FakeEl('div');
  let top = 400;
  anchor.getBoundingClientRect = () => ({ top });
  messages.scrollTop = 1000;
  // The browser already anchored: the row did not move, so nothing to add.
  transcript.holdView(anchor, () => {});
  assert.equal(messages.scrollTop, 1000);
  // No anchoring: rows inserted above pushed the anchor down by 250px.
  transcript.holdView(anchor, () => { top = 650; });
  assert.equal(messages.scrollTop, 1250);
});

test('while reading up, rows on screen and below are never trimmed', () => {
  S.stickBottom = false;
  messages.getBoundingClientRect = () => ({ top: 0 });
  try {
    for (let i = 0; i < transcript.HARD_UNITS; i++) finishedTool(i);
    // Rows 0..99 lie above the view; row 100 onward is on screen or below.
    rows().forEach((row, i) => { row.getBoundingClientRect = () => ({ top: (i - 100) * 10, bottom: (i - 99) * 10 }); });
    const reading = rows()[100];
    finishedTool('over');
    assert.ok(reading.parentNode, 'the row being read survives the ceiling trim');
    assert.equal(rows()[0], reading, 'only rows above the view were trimmed');
    assert.equal(transcript.countUnits(), transcript.HARD_UNITS + 1 - 100);
  } finally {
    delete messages.getBoundingClientRect;
  }
});
