// DOM-shim tests for the clarify card.
// Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach, afterEach, mock } from 'node:test';
import assert from 'node:assert/strict';

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
    this.value = '';
    this._attrs = {};
    if (this.tagName === 'TEXTAREA') {
      // Faithful to HTMLTextAreaElement: 'type' is a readonly IDL property.
      // Assigning it throws in strict-mode module code, like a real browser.
      Object.defineProperty(this, 'type', { get() { return 'textarea'; } });
    }
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
        if ((cls && c.classList.contains(cls)) || (idSel && c.id === idSel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  get classList() {
    const self = this;
    const read = () => new Set(String(self.className || '').split(/\s+/).filter(Boolean));
    const write = (s) => { self.className = [...s].join(' '); };
    return {
      contains: (c) => read().has(c),
      add: (...cs) => { const s = read(); cs.forEach((c) => s.add(c)); write(s); },
      remove: (...cs) => { const s = read(); cs.forEach((c) => s.delete(c)); write(s); },
      toggle: (c, force) => {
        const s = read();
        const on = force === undefined ? !s.has(c) : !!force;
        if (on) s.add(c); else s.delete(c);
        write(s);
        return on;
      },
    };
  }
}

const els = new Map();
function el(id) {
  if (!els.has(id)) els.set(id, new FakeElement());
  return els.get(id);
}

globalThis.document = {
  getElementById: (id) => el(id),
  createElement: (tag) => new FakeElement(tag),
  addEventListener: () => {},
  querySelector: () => null,
  body: new FakeElement('body'),
  activeElement: null,
};
globalThis.WebSocket = class { static get OPEN() { return 1; } };
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };

const S = (await import('./state.js')).S;
const clarify = await import('./clarify.js');

let sent;
beforeEach(() => {
  sent = [];
  S.ws = { readyState: 1, send: (m) => sent.push(JSON.parse(m)) };
  clarify.clearClarify();
  while (el('messages').children.length) el('messages').children[0].remove();
});

test('clarify card sends clarify_response with the typed answer', () => {
  clarify.queueClarify({ id: 'clr-1', question: 'Which approach?', timeout_seconds: 300 });
  const card = el('messages').querySelector('.approval-card');
  assert.ok(card, 'card rendered');
  assert.match(card.querySelector('.ac-command').textContent, /Which approach/);
  const input = card.querySelector('.ac-friction-input');
  input.value = 'the first one';
  card.querySelector('.approve').click();
  assert.deepEqual(sent, [{ type: 'clarify_response', id: 'clr-1', answer: 'the first one',action:'answer' }]);
  assert.ok(el('messages').querySelector('.approval-card'),'waits for acknowledgement');
  clarify.dismissClarify('clr-1','answer');
  assert.equal(el('messages').querySelector('.approval-card'), null);
});

test('empty answer does not send', () => {
  clarify.queueClarify({ id: 'clr-2', question: 'q' });
  el('messages').querySelector('.approve').click();
  assert.deepEqual(sent, []);
});

test('clearClarify drops the card', () => {
  clarify.queueClarify({ id: 'clr-3', question: 'q' });
  clarify.clearClarify();
  assert.equal(el('messages').querySelector('.approval-card'), null);
});

afterEach(()=>clarify.clearClarify());

test('multiline answers use explicit keyboard submission and respect IME',()=>{
 clarify.queueClarify({id:'multi',question:'Describe the change'});
 const input=el('messages').querySelector('.ac-friction-input');input.value='First line\nSecond line';
 input.fire('keydown',{key:'Enter'});assert.equal(sent.length,0,'Enter is a newline');
 input.fire('keydown',{key:'Enter',ctrlKey:true,isComposing:true});assert.equal(sent.length,0,'IME composition cannot submit');
 input.fire('keydown',{key:'Enter',ctrlKey:true});assert.equal(sent.length,1);assert.equal(sent[0].answer,'First line\nSecond line');
});

test('queueClarify survives readonly textarea.type',()=>{
 // Regression (v2.32.0): clarify.js assigned input.type='text' on a
 // <textarea>, whose type is a readonly IDL property — strict mode threw
 // "Attempted to assign to readonly property" and killed renderCard before
 // the card reached the DOM. The shim models the readonly getter; every
 // render assertion in this file depends on it.
 assert.doesNotThrow(()=>clarify.queueClarify({id:'clr-ro',question:'readonly?'}));
 assert.ok(el('messages').querySelector('.approval-card'),'card must render');
 clarify.clearClarify();
});

test('skip is an acknowledged decision with no invented answer',()=>{
 clarify.queueClarify({id:'skip',question:'Which option?'});
 const card=el('messages').querySelector('.approval-card');card.children.find(child=>child.className==='ac-actions').children.find(child=>child.textContent==='Skip question').click();
 assert.deepEqual(sent,[{type:'clarify_response',id:'skip',answer:'',action:'skip'}]);
 assert.ok(el('messages').querySelector('.approval-card'));
 clarify.dismissClarify('skip','skip');assert.equal(el('messages').querySelector('.approval-card'),null);
});

const card = () => el('messages').querySelector('.approval-card');
const button = (label) => card().querySelector('.ac-actions').children.find((b) => b.textContent === label);
function recordDecisions() {
  const decisions = [];
  S.recordDecision = (d) => decisions.push(d);
  return decisions;
}
afterEach(() => { delete S.recordDecision; mock.timers.reset(); });

test('countdown ticks down, turns urgent, then expires the card', () => {
  mock.timers.enable({ apis: ['setInterval', 'Date'], now: 1_000_000 });
  const decisions = recordDecisions();
  clarify.queueClarify({ id: 'tick', question: 'q', timeout_seconds: 15 });
  const deadline = card().querySelector('.ac-deadline');
  assert.equal(deadline.textContent, 'expires in 15s');
  assert.equal(deadline.classList.contains('urgent'), false);
  mock.timers.tick(6000);
  assert.equal(deadline.textContent, 'expires in 9s');
  assert.equal(deadline.classList.contains('urgent'), true);
  mock.timers.tick(9000);
  assert.equal(card(), null, 'expired card is removed');
  assert.deepEqual(decisions, [{ id: 'tick', kind: 'question', command: 'q', state: 'expired' }]);
  sent = [];
  mock.timers.tick(5000);
  assert.deepEqual(sent, [], 'sweep stops after expiry');
});

test('missing or non-positive timeout falls back to the default deadline', () => {
  mock.timers.enable({ apis: ['setInterval', 'Date'], now: 0 });
  clarify.queueClarify({ id: 'dflt', question: 'q', timeout_seconds: 0 });
  assert.equal(card().querySelector('.ac-deadline').textContent, 'expires in 300s');
  mock.timers.tick(299_000);
  assert.ok(card(), 'still pending before the default deadline');
  mock.timers.tick(1000);
  assert.equal(card(), null);
});

test('undelivered answer keeps the card actionable for a retry', () => {
  S.ws = { readyState: 3, send: () => assert.fail('closed socket must not send') };
  clarify.queueClarify({ id: 'down', question: 'q' });
  card().querySelector('.ac-friction-input').value = 'answer';
  button('send answer').click();
  assert.ok(el('messages').children.some((m) => m.className === 'msg system' && /not delivered/.test(m.innerHTML)));
  assert.equal(button('send answer').disabled, false, 'buttons stay enabled');
  S.ws = { readyState: 1, send: (m) => sent.push(JSON.parse(m)) };
  button('send answer').click();
  assert.deepEqual(sent, [{ type: 'clarify_response', id: 'down', answer: 'answer', action: 'answer' }]);
});

test('a pending answer blocks duplicate submissions', () => {
  clarify.queueClarify({ id: 'dup', question: 'q' });
  const input = card().querySelector('.ac-friction-input');
  input.value = '  padded  ';
  input.fire('keydown', { key: 'Enter', metaKey: true });
  assert.deepEqual(sent, [{ type: 'clarify_response', id: 'dup', answer: 'padded', action: 'answer' }]);
  assert.ok(card().querySelectorAll('button').every((b) => b.disabled));
  assert.match(card().querySelector('.management-note').textContent, /waiting for server acceptance/);
  input.fire('keydown', { key: 'Enter', ctrlKey: true });
  assert.equal(sent.length, 1, 'keyboard resubmit is ignored while pending');
});

test('whitespace-only answer does not send', () => {
  clarify.queueClarify({ id: 'ws', question: 'q' });
  card().querySelector('.ac-friction-input').value = '   \n ';
  button('send answer').click();
  assert.deepEqual(sent, []);
});

test('clear records interrupted vs not-confirmed decisions', () => {
  const decisions = recordDecisions();
  clarify.queueClarify({ id: 'int', question: 'unanswered' });
  clarify.clearClarify();
  clarify.queueClarify({ id: 'nc', question: 'sent' });
  card().querySelector('.ac-friction-input').value = 'x';
  button('send answer').click();
  clarify.clearClarify();
  assert.deepEqual(decisions.map((d) => [d.id, d.state]), [['int', 'interrupted'], ['nc', 'not confirmed']]);
});

test('dismiss records the pending action and ignores foreign ids', () => {
  const decisions = recordDecisions();
  clarify.queueClarify({ id: 'mine', question: 'q' });
  clarify.dismissClarify('other');
  clarify.expireClarify('other');
  assert.ok(card(), 'foreign ids leave the card alone');
  assert.deepEqual(decisions, []);
  button('Skip question').click();
  clarify.dismissClarify('mine');
  assert.equal(card(), null);
  assert.deepEqual(decisions, [{ id: 'mine', kind: 'question', command: 'q', action: 'skip', state: 'accepted' }]);
  clarify.clearClarify();
  assert.equal(decisions.length, 1, 'a recorded decision is not re-recorded on clear');
});

test('a later question replaces the active card', () => {
  clarify.queueClarify({ id: 'first', question: 'one' });
  clarify.queueClarify({ id: 'second', question: 'two' });
  assert.equal(el('messages').querySelectorAll('.approval-card').length, 1);
  assert.match(card().querySelector('.ac-command').textContent, /two/);
  card().querySelector('.ac-friction-input').value = 'a';
  button('send answer').click();
  assert.equal(sent[0].id, 'second');
});

test('card exposes itself to supervision while active', () => {
  let refreshes = 0;
  S.refreshSupervision = () => { refreshes++; };
  clarify.queueClarify({ id: 'sup', question: 'q' });
  assert.equal(S.activeClarifyCard, card());
  clarify.clearClarify();
  assert.equal(S.activeClarifyCard, null);
  assert.ok(refreshes >= 2);
  delete S.refreshSupervision;
});

test('stop work delegates to the cancel button', () => {
  let cancelled = 0;
  el('cancel-btn').addEventListener('click', () => { cancelled++; });
  clarify.queueClarify({ id: 'stop', question: 'q' });
  button('Stop work').click();
  assert.equal(cancelled, 1);
  assert.deepEqual(sent, [], 'stopping sends no answer');
});
