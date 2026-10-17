// Web UI frontend regression tests (functional + security rendering findings).
// Run: node --test cmd/odek/ui/js/redhunt_2026_10_17_ui.test.js
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Mini-DOM (lifecycle.test.js shape, plus data-* attribute parsing so
// completion rows rendered via innerHTML carry their dataset). ──
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
    this.value = '';
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.scrollTop = 0;
    this.scrollHeight = 100;
    this.clientHeight = 50;
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
            const idm = tok.match(/\sid="([^"]*)"/);
            if (idm) el.id = idm[1];
            for (const m of tok.matchAll(/data-([a-z-]+)="([^"]*)"/g)) {
              el.dataset[m[1].replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = m[2]
                .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
            }
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
  removeAttribute(k) { delete this._attrs[k]; }
  matches(sel) {
    if (sel.startsWith('.')) return sel.slice(1).split('.').every((c) => c && this.classList.contains(c));
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    return this.tagName === sel.toUpperCase();
  }
  closest(sel) { let el = this; while (el) { if (el.matches(sel)) return el; el = el.parentNode; } return null; }
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
    if (c.parentNode) { const i = c.parentNode.children.indexOf(c); if (i >= 0) c.parentNode.children.splice(i, 1); }
    c.parentNode = this; this.children.push(c); return c;
  }
  get parentElement() { return this.parentNode; }
  append(...cs) { cs.forEach(c => this.appendChild(c)); }
  insertAdjacentElement(_pos, node) { return this.appendChild(node); }
  insertBefore(c, ref) {
    if (c.parentNode) { const i = c.parentNode.children.indexOf(c); if (i >= 0) c.parentNode.children.splice(i, 1); }
    c.parentNode = this;
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i >= 0) this.children.splice(i, 0, c); else this.children.push(c);
    return c;
  }
  remove() {
    if (this.parentNode) { const i = this.parentNode.children.indexOf(this); if (i >= 0) this.parentNode.children.splice(i, 1); this.parentNode = null; }
  }
  contains(n) { let el = n; while (el) { if (el === this) return true; el = el.parentNode; } return false; }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  dispatchEvent(ev) { this.dispatch(ev.type, ev); return true; }
  dispatch(type, ev = {}) {
    let el = this;
    while (el) {
      for (const fn of [...(el._listeners[type] || [])]) {
        fn({ target: this, preventDefault() {}, stopPropagation() {}, ...ev, type });
      }
      el = el.parentNode;
    }
  }
  focus() {}
  click() { if (!this.disabled) this.dispatch('click'); }
}

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
// Prompt history persisted by a previous page load.
store.set('odek_history', JSON.stringify(['first prompt', 'second prompt']));
globalThis.sessionStorage = {
  getItem: () => null, setItem: () => {}, removeItem: () => {},
};

const byId = {};
const bySelector = {};
globalThis.document = {
  getElementById: (id) => (byId[id] ||= Object.assign(new FakeEl('div'), { id })),
  createElement: (tag) => new FakeEl(tag),
  querySelector: (sel) => (bySelector[sel] ||= new FakeEl('div')),
  querySelectorAll: (sel) => (bySelector[sel] ? [bySelector[sel]] : []),
  addEventListener: () => {},
  body: new FakeEl('body'),
  activeElement: null,
  hidden: false,
};
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.window = globalThis;
globalThis.Event = class { constructor(type, opts) { this.type = type; Object.assign(this, opts || {}); } };
globalThis.WebSocket = class FakeWebSocket {
  static get OPEN() { return 1; }
  constructor() { this.readyState = 1; this.sent = []; }
  send(m) { this.sent.push(m); }
  close() { this.readyState = 3; }
};
let fetchImpl = async () => ({ ok: true, status: 200, headers: { get: () => null }, text: async () => '', json: async () => ({ messages: [] }) });
globalThis.fetch = (...a) => fetchImpl(...a);
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

const S = (await import('./state.js')).S;
const render = await import('./render.js');
await import('./input.js');
const sessions = await import('./sessions.js');
const ws = await import('./ws.js');
const health = await import('./health.js');
await import('./supervision.js').catch(() => {});
const { markdownToHtml } = await import('./markdown.js');
const { classifyToolResult } = await import('./tools.js');
const { toolView } = await import('./toolviews.js');

function deliver(event) { S.ws.onmessage({ data: JSON.stringify(event) }); }
function collectText(el, acc = []) {
  if (!el) return acc;
  if (el.textContent) acc.push(el.textContent);
  for (const c of el.children) collectText(c, acc);
  return acc;
}
const wait = (ms) => new Promise(r => setTimeout(r, ms));

beforeEach(() => {
  ws.connect();
  S.ws.onopen();
  health.stopHeartbeat();
  byId.messages.children.length = 0;
  render.resetTurnState();
  S.currentTurnId = null;
  S.busy = false;
  S.queuePaused = false;
  S.promptQueue.length = 0;
  S.sessionId = null;
  S.recoveryReason = '';
  byId.prompt.value = '';
  byId.completion.className = '';
  byId.completion.innerHTML = '';
});

// The index starts at history.length, so after a page load (this test runs first:
// any send resets the index) ArrowUp on an empty composer recalls nothing (idx > 0 is false)
// until the first send resets the index.
test('RED: ArrowUp after a page load recalls the most recent persisted prompt', () => {
  assert.deepEqual(S.history.slice(0, 2), ['first prompt', 'second prompt'], 'history loaded from localStorage');
  const prompt = byId.prompt;
  prompt.value = '';
  prompt.dispatch('keydown', { key: 'ArrowUp', altKey: false, shiftKey: false });
  assert.equal(prompt.value, 'second prompt', 'ArrowUp on an empty composer recalled ' + JSON.stringify(prompt.value));
});

// Finding: @-completion replaces from the last '@' up to the CURSOR, but the
// query is only the first word after '@'. Text typed after an earlier
// @-reference keeps re-opening the popup, and Enter (meant to send) accepts
// the completion and deletes everything typed after the reference.
test('RED: Enter after text typed past an @-reference must not delete that text', async () => {
  fetchImpl = async (url) => {
    if (String(url).startsWith('/api/resources')) {
      return { ok: true, status: 200, headers: { get: () => 'application/json' },
        json: async () => [{ id: '@main.go', type: 'file', label: 'main.go', detail: 'Go source' }] };
    }
    return { ok: true, status: 200, headers: { get: () => null }, text: async () => '', json: async () => ({}) };
  };
  const prompt = byId.prompt;
  prompt.value = 'look at @main.go and explain what it does';
  prompt.selectionStart = prompt.selectionEnd = prompt.value.length;
  prompt.dispatch('input');
  await wait(200); // completion debounce (150ms) + fetch
  await wait(0);
  // Operator presses Enter to send the prompt.
  prompt.dispatch('keydown', { key: 'Enter', shiftKey: false });
  await wait(0);
  const sent = S.ws.sent.map(m => JSON.parse(m)).filter(m => m.type === 'prompt');
  const visible = prompt.value;
  assert.ok(
    sent.some(m => m.content.includes('and explain what it does')) || visible.includes('and explain what it does'),
    'typed text after the @-reference was destroyed; prompt is now ' + JSON.stringify(visible),
  );
});

// Finding: after "New session" mid-run (confirm → cancel), the cancelled
// turn's 'cancelled' and 'turn_settled' frames still carry the OLD session id,
// but the client only compares session ids when S.sessionId is set — it is
// null in a new conversation — so the old run's stop narration and recovery
// state land in the fresh, empty conversation.
test('RED: old turn cancel/settle frames must not paint into a new session after New session', () => {
  S.sessionId = 'sess-old';
  deliver({ type: 'turn_started', turn_id: 't_old', session_id: 'sess-old', initiated: 'operator' });
  assert.equal(S.busy, true);
  globalThis.confirm = () => true;
  sessions.newSession();
  assert.equal(S.sessionId, null);
  byId.messages.children.length = 0; // the new conversation starts empty
  S.recoveryReason = '';
  const pausedByNewSession = !!S.queuePaused;
  // The server answers the cancel issued by newSession for the OLD session.
  deliver({ type: 'cancelled', session_id: 'sess-old', requested: true });
  deliver({ type: 'turn_settled', turn_id: 't_old', session_id: 'sess-old', status: 'cancelled', run_id: 'r1' });
  const text = collectText(byId.messages).join(' ');
  assert.doesNotMatch(text, /Stop accepted|Execution stopped/, 'old session stop narration rendered into the new conversation: ' + text);
  assert.equal(S.recoveryReason || '', '', 'new conversation flagged as needing recovery for the old session');
  assert.equal(!!S.queuePaused, pausedByNewSession, 'new conversation queue paused by the old turn');
});

// Finding: reloaded history renders the server-side EXPANDED prompt
// (content) for user messages. An @-reference is persisted as the full
// referenced file/session body; the server keeps what the operator typed in
// principal_prompt, which the renderer ignores — reopening a session dumps
// every referenced file into the user's bubbles.
test('RED: reloaded user message shows what the operator typed, not the expanded @-reference body', () => {
  byId.messages.children.length = 0;
  const expanded = 'look at \n\n--- @main.go ---\n<untrusted_content_abcd1234 source="resource:@main.go">\npackage main // FILE_BODY_MARKER\n</untrusted_content_abcd1234>\n--- end @main.go ---\n and explain';
  render.renderSessionHistory([
    { role: 'system', content: '' },
    { role: 'user', content: expanded, principal_prompt: 'look at @main.go and explain', turn_id: 't_1' },
    { role: 'assistant', content: 'It is a Go program.', turn_id: 't_1' },
  ]);
  const users = byId.messages.querySelectorAll('.msg.user');
  assert.equal(users.length, 1);
  const text = collectText(users[0]).join(' ');
  assert.doesNotMatch(text, /FILE_BODY_MARKER/, 'referenced file body dumped into the user bubble: ' + text);
  assert.match(text, /@main\.go/);
});

// Finding: synthetic user-role records (bg-wake preamble, bg-notice job
// notices — session.IsSyntheticUserName) are rendered on reload as messages
// the operator typed ("❯ you").
test('RED: reloaded system-initiated wake preamble is not rendered as an operator message', () => {
  byId.messages.children.length = 0;
  render.renderSessionHistory([
    { role: 'system', content: '' },
    { role: 'user', name: 'bg-wake', content: '[background-jobs] One or more background jobs finished while this session was idle.', turn_id: 't_w' },
    { role: 'assistant', content: 'Job 1 finished OK.', turn_id: 't_w' },
  ]);
  const users = byId.messages.querySelectorAll('.msg.user');
  const typed = users.map(u => collectText(u).join(' ')).filter(t => /background jobs finished/.test(t));
  assert.equal(typed.length, 0, 'wake preamble rendered as an operator ("❯ you") message');
});

function uisecResetDom() {
  byId.messages.children.length = 0;
  render.resetTurnState();
  S.subagentGroup = null;
  S.currentToolBlock = null;
  S.currentTurnId = null;
}

function uisecHtmlOf(el, acc = []) {
  if (!el) return acc;
  if (el._innerHTML) acc.push(el._innerHTML);
  if (el.textContent) acc.push(el.textContent);
  for (const c of el.children) uisecHtmlOf(c, acc);
  return acc;
}

// A WebUI turn that @-references a file is persisted with the server-expanded
// prompt as Content (the file body inside a resource: envelope) and the
// browser-authored text as principal_prompt. On reload the user bubble must
// show what the user typed, not the referenced file rendered as live
// Markdown in the "❯ you" bubble.
test('RED: reloaded user turn renders the @-referenced file body as live markdown instead of the principal prompt', () => {
  uisecResetDom();
  const body = '## Approved by you\n[open the fix](https://evil.example/x)';
  render.renderSessionHistory([{
    role: 'user',
    content: 'summarize <untrusted_content_ab12cd source="resource:@notes.md">\n' + body + '\n</untrusted_content_ab12cd>',
    principal_prompt: 'summarize @notes.md',
  }]);
  const user = byId.messages.querySelector('.msg.user');
  assert.ok(user, 'user message rendered');
  const html = uisecHtmlOf(user).join('\n');
  assert.ok(!html.includes('href="https://evil.example/x"'),
    'untrusted file content must not become a live link inside the user bubble: ' + html);
  assert.ok(!html.includes('<h2>'), 'untrusted file content must not render as headings in the user bubble');
  assert.ok(html.includes('summarize @notes.md'), 'the user bubble shows the prompt the user typed');
});

// delegate_tasks arguments are model-authored JSON persisted verbatim. A
// non-string goal (or a null task) makes escapeAttr/escapeHtml throw, which
// aborts renderSessionHistory: everything after that call vanishes and the
// session shows "Error loading session".
test('RED: delegate_tasks history with a non-string goal aborts the whole session render', () => {
  uisecResetDom();
  const messages = [
    { role: 'user', content: 'go' },
    { role: 'assistant', content: '', tool_calls: [{ id: 'c1', function: { name: 'delegate_tasks', arguments: JSON.stringify({ tasks: [{ goal: 42 }, null] }) } }] },
    { role: 'tool', tool_call_id: 'c1', content: 'error: invalid tasks' },
    { role: 'assistant', content: 'later answer survives' },
  ];
  assert.doesNotThrow(() => render.renderSessionHistory(messages));
  const text = uisecHtmlOf(byId.messages).join('\n');
  assert.ok(text.includes('later answer survives'), 'messages after the malformed call still render');
});

test('RED: live delegate_tasks tool_call with a non-string goal throws in the frame handler', () => {
  uisecResetDom();
  assert.doesNotThrow(() => render.addSubagentGroup(JSON.stringify({ tasks: [{ goal: { x: 1 } }] })));
});

// The inline tokenizer re-runs ANGLE_AUTO / BARE_URL against the rest of the
// text at every '<' / 'h' whose candidate is rejected; the `[^>\s]+` run
// scans to the end each time, so the cost is quadratic in the input. The
// streaming path re-renders the whole answer every frame, and reloaded user
// turns render expanded @-reference bodies (up to 12 MiB) through it too.
// Render time must scale linearly: quadrupling the input may cost roughly 4x,
// a quadratic tokenizer costs 16x. Best-of-three timings per size keep the
// ratio stable; the bound sits between the two regimes.
function uiScalingRatio(unit, small) {
  const best = (n) => {
    let min = Infinity;
    for (let k = 0; k < 5; k++) {
      const t = process.hrtime.bigint();
      markdownToHtml(unit.repeat(n));
      min = Math.min(min, Number(process.hrtime.bigint() - t));
    }
    return min;
  };
  // A quadratic tokenizer exceeds the bound on every attempt; scheduler noise only on some.
  let ratio = Infinity;
  for (let attempt = 0; attempt < 3 && ratio >= 12; attempt++) ratio = Math.min(ratio, best(small * 4) / Math.max(best(small), 1));
  return ratio;
}

test('RED: markdownToHtml is quadratic on unclosed <http: autolinks (hostile text freezes the tab)', () => {
  const ratio = uiScalingRatio('<http:', 8000);
  assert.ok(ratio < 12, 'render cost grew ' + ratio.toFixed(1) + 'x for 4x input');
});

test('RED: markdownToHtml is quadratic on rejected bare URLs (backslash)', () => {
  const ratio = uiScalingRatio('http://\\', 8000);
  assert.ok(ratio < 12, 'render cost grew ' + ratio.toFixed(1) + 'x for 4x input');
});

for (const unit of ['**_a', '[', '[a](', '![', '![a](', '~~', '* a*b', '_ a_b', '`', '**', '*', '_']) {
  test('RED: markdownToHtml scales linearly on repeated ' + JSON.stringify(unit), () => {
    const ratio = uiScalingRatio(unit, Math.ceil(150000 / unit.length));
    assert.ok(ratio < 12, 'render cost grew ' + ratio.toFixed(1) + 'x for 4x input');
  });
}

test('inline constructs still render after the linear tokenizer', () => {
  assert.equal(markdownToHtml('**b** and *i* and ~~s~~ `c`'), '<p><strong>b</strong> and <em>i</em> and <s>s</s> <code>c</code></p>');
  assert.match(markdownToHtml('[x](https://a.b/c) ![y](https://a.b/i.png)'), /<a href="https:\/\/a\.b\/c"[^>]*>x<\/a> <a [^>]*md-img[^>]*>y<\/a>/);
  assert.equal(markdownToHtml('snake_case_name and 2*3*4'), '<p>snake_case_name and 2*3*4</p>');
  assert.equal(markdownToHtml('**unclosed *a'), '<p>**unclosed *a</p>');
  assert.equal(markdownToHtml('[x](javascript:alert)'), '<p>x</p>');
});

test('autolinks still link ordinary URLs and reject over-long candidates', () => {
  const ok = 'https://example.com/' + 'a'.repeat(1500);
  assert.match(markdownToHtml('see ' + ok + ' now'), /<a href="https:\/\/example\.com\/a+"/);
  assert.match(markdownToHtml('<' + ok + '>'), /<a href=/);
  const long = 'https://example.com/' + 'a'.repeat(3000);
  assert.doesNotMatch(markdownToHtml('see ' + long), /<a /);
  assert.doesNotMatch(markdownToHtml('<' + long + '>'), /<a /);
});

// pytest and jest print failures before passes ("3 failed, 10 passed");
// GO_TEST only looks for "failed" AFTER "passed", so the chip is a green
// "✓ 10 passed". TAP "ok N - name" lines count as Go packages passed.
test('RED: pytest "3 failed, 10 passed" summary renders a green pass chip', () => {
  const chips = classifyToolResult('shell', '=========== 3 failed, 10 passed in 1.20s ===========').filter(c => c.kind === 'test');
  assert.ok(chips.length > 0, 'a test chip is shown');
  assert.ok(chips.every(c => c.tone !== 'ok'), 'a run with failures must not get an ok chip: ' + JSON.stringify(chips));
});

test('RED: TAP output with a failing test renders a green "packages passed" chip', () => {
  const out = 'TAP version 13\nok 1 - a\nok 2 - b\nnot ok 3 - c\n# tests 3\n# pass 2\n# fail 1\n';
  const chips = classifyToolResult('shell', out).filter(c => c.kind === 'test');
  assert.ok(chips.every(c => c.tone !== 'ok'), 'a TAP run with a failure must not get an ok chip: ' + JSON.stringify(chips));
});

// search_files/glob/tree titles go through displayLabel; read_file,
// write_file and patch titles (model/tool-supplied paths) do not, so a
// bidi override reorders the file name shown in the result card.
test('RED: write_file/patch/read_file result titles show raw bidi overrides in the path', () => {
  const rlo = '‮';
  const wf = toolView('write_file', JSON.stringify({ success: true, path: 'notes' + rlo + 'txt.sh' }), '{}');
  assert.ok(wf && wf.items.length, 'write_file view');
  assert.ok(!wf.items[0].title.includes(rlo), 'write_file title must escape U+202E: ' + JSON.stringify(wf.items[0].title));
  const rf = toolView('read_file', JSON.stringify({ content: 'x', total_lines: 1 }), JSON.stringify({ path: 'cfg' + rlo + 'lmy.sh' }));
  assert.ok(rf && rf.items.length, 'read_file view');
  assert.ok(!rf.items[0].title.includes(rlo), 'read_file title must escape U+202E: ' + JSON.stringify(rf.items[0].title));
});

test('RED: escapeHtml/escapeAttr map null, undefined and false to empty and stringify numbers', async () => {
  const { escapeHtml, escapeAttr } = await import('./escape.js');
  for (const f of [escapeHtml, escapeAttr]) {
    assert.equal(f(null), '');
    assert.equal(f(undefined), '');
    assert.equal(f(false), '');
    assert.equal(f(''), '');
    assert.equal(f(42), '42');
  }
});

test('RED: legacy attachment chip escapes bidi/control characters in the file name', async () => {
  const { stripAttachmentBodies } = await import('./untrusted.js');
  const env = '<untrusted_content_ab12cd source="attachment:a‮b.txt">\nbody\n</untrusted_content_ab12cd>';
  assert.ok(!stripAttachmentBodies(env).includes('‮'), JSON.stringify(stripAttachmentBodies(env)));
});

test('RED: stop frames for a session set outside the frame handler do not leak after New session', () => {
  // The id is assigned directly (upload path / loadSession), never seen in a frame.
  S.sessionId = 'sess-uploaded';
  globalThis.confirm = () => true;
  sessions.newSession();
  assert.equal(S.sessionId, null);
  byId.messages.children.length = 0;
  S.recoveryReason = '';
  deliver({ type: 'cancelled', session_id: 'sess-uploaded', requested: true });
  deliver({ type: 'turn_settled', turn_id: 't_x', session_id: 'sess-uploaded', status: 'cancelled' });
  const text = collectText(byId.messages).join(' ');
  assert.doesNotMatch(text, /Stop accepted|Execution stopped/, text);
  assert.equal(S.recoveryReason || '', '');
});
