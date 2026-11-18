// While the engine retries a failed model call it sends an llm_retry
// agent_signal; the status rail says the provider is being retried instead of
// showing a bare "reasoning" timer. Automatic recovery stays out of the
// transcript: no message, no toast.
// Run: node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

class SrvFixEl {
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
    this.hidden = false;
    this.disabled = false;
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
  insertBefore(c) { return this.appendChild(c); }
  append(...cs) { cs.forEach(c => this.appendChild(c)); }
  remove() {
    if (this.parentNode) {
      const i = this.parentNode.children.indexOf(this);
      if (i >= 0) this.parentNode.children.splice(i, 1);
      this.parentNode = null;
    }
  }
  setAttribute(k, v) { this._attrs[k] = String(v); }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  removeAttribute(k) { delete this._attrs[k]; }
  insertAdjacentElement(_pos, node) { return this.appendChild(node); }
  addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); }
  removeEventListener() {}
  focus() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
  closest() { return null; }
  matches() { return false; }
  scrollIntoView() {}
  getBoundingClientRect() { return { top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0 }; }
  get classList() {
    const self = this;
    const names = () => String(self.className).split(/\s+/).filter(Boolean);
    return {
      add: (...n) => { self.className = [...new Set([...names(), ...n])].join(' '); },
      remove: (...n) => { self.className = names().filter(x => !n.includes(x)).join(' '); },
      toggle: (n, force) => {
        const want = force === undefined ? !names().includes(n) : !!force;
        if (want) self.classList.add(n); else self.classList.remove(n);
        return want;
      },
      contains: n => names().includes(n),
    };
  }
}

const srvFixById = new Map();
const srvFixEl = id => {
  if (!srvFixById.has(id)) srvFixById.set(id, Object.assign(new SrvFixEl(), { id }));
  return srvFixById.get(id);
};
const srvFixBySel = new Map();
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.document = {
  getElementById: id => srvFixEl(id),
  createElement: tag => new SrvFixEl(tag),
  createTextNode: t => Object.assign(new SrvFixEl('#text'), { textContent: t }),
  querySelector: sel => {
    if (!srvFixBySel.has(sel)) srvFixBySel.set(sel, new SrvFixEl());
    return srvFixBySel.get(sel);
  },
  querySelectorAll: () => [],
  addEventListener: () => {},
  body: new SrvFixEl('body'),
  documentElement: new SrvFixEl('html'),
  activeElement: null,
  hidden: false,
};
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.requestAnimationFrame = fn => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = t => clearTimeout(t);
globalThis.WebSocket = class {
  static get OPEN() { return 1; }
  constructor() { this.readyState = 1; this.sent = []; }
  send(m) { this.sent.push(m); }
  close() { this.readyState = 3; }
};

const { S } = await import('./state.js');
const ws = await import('./ws.js');

function srvDeliver(event) {
  S.ws.onmessage({ data: JSON.stringify(event) });
}

beforeEach(() => {
  ws.connect();
  while (srvFixEl('messages').children.length) srvFixEl('messages').children[0].remove();
  srvFixEl('toast').textContent = 'unchanged';
  S.currentIntent = '';
});

test('an llm_retry signal puts the retry on the status rail of a running turn', () => {
  S.busy = true;
  srvDeliver({ type: 'agent_signal', event: 'llm_retry', detail: 'HTTP 503, attempt 2 of 8', count: 2 });
  assert.equal(S.currentIntent, 'provider error · retrying (HTTP 503, attempt 2 of 8)');
  assert.match(srvFixEl('intent-rail').textContent, /^provider error · retrying \(HTTP 503, attempt 2 of 8\)/);
  assert.equal(srvFixEl('messages').children.length, 0, 'automatic recovery adds no transcript message');
  assert.equal(srvFixEl('toast').textContent, 'unchanged', 'automatic recovery shows no toast');
  S.busy = false;
});

test('the rail returns to reasoning once the retried call streams again', () => {
  S.busy = true;
  srvDeliver({ type: 'agent_signal', event: 'llm_retry', detail: 'HTTP 500, attempt 3 of 8', count: 3 });
  assert.match(S.currentIntent, /retrying/);
  srvDeliver({ type: 'thinking_delta', content: 'thinking again' });
  assert.equal(S.currentIntent, 'reasoning');
  S.busy = false;
});

test('an llm_retry signal without detail still names the retry', () => {
  S.busy = true;
  srvDeliver({ type: 'agent_signal', event: 'llm_retry', count: 2 });
  assert.equal(S.currentIntent, 'provider error · retrying');
  S.busy = false;
});

test('an llm_retry signal after Stop keeps the stopping label', () => {
  S.busy = true;
  S.stopRequested = true;
  S.currentIntent = 'stopping';
  srvDeliver({ type: 'agent_signal', event: 'llm_retry', detail: 'HTTP 503, attempt 4 of 8', count: 4 });
  assert.equal(S.currentIntent, 'stopping');
  S.stopRequested = false;
  S.busy = false;
});

test('an llm_retry signal outside a running turn leaves the idle rail alone', () => {
  S.busy = false;
  srvDeliver({ type: 'agent_signal', event: 'llm_retry', detail: 'HTTP 503, attempt 2 of 8', count: 2 });
  assert.equal(S.currentIntent, '');
  assert.equal(srvFixEl('messages').children.length, 0);
});

// A provider failure the server already phrased (error frame marked phrased)
// is shown as sent: no substring rule rewrites it into a generic hint or cuts
// it at a quoted "message" field. Unphrased runtime errors keep their hints.
const PHRASED = 'provider openai returned HTTP 408: Request timed out. "message":"upstream" (retries exhausted after 8 attempts)';

test('a phrased provider failure is shown as the server sent it', () => {
  srvDeliver({ type: 'turn_started', turn_id: 't-phrased' });
  srvDeliver({ type: 'error', turn_id: 't-phrased', message: PHRASED, phrased: true });
  const html = srvFixEl('messages').children.at(-1).innerHTML;
  assert.ok(html.includes(PHRASED), html);
  assert.ok(!html.includes('raise llm.request_timeout_seconds'), html);
});

test('a phrased failure held while stopping is shown as sent once the run fails', async () => {
  const render = await import('./render.js');
  srvDeliver({ type: 'turn_started', turn_id: 't-phrased-stop' });
  render.requestTurnStop();
  srvDeliver({ type: 'error', turn_id: 't-phrased-stop', message: PHRASED, phrased: true });
  srvDeliver({ type: 'turn_settled', turn_id: 't-phrased-stop', status: 'failed' });
  const html = srvFixEl('messages').children.at(-1).innerHTML;
  assert.ok(html.includes(PHRASED), html);
});

test('an unphrased request deadline still gets the request timeout hint', () => {
  srvDeliver({ type: 'turn_started', turn_id: 't-deadline' });
  srvDeliver({ type: 'error', turn_id: 't-deadline', message: 'iteration 0: context deadline exceeded' });
  assert.match(srvFixEl('messages').children.at(-1).innerHTML, /raise llm\.request_timeout_seconds/);
});
