// A clarify answer the server refuses arrives as clarify_rejected: the card
// must become actionable again (edit and resend, skip, stop) instead of
// staying "waiting for server acceptance" until the question expires.
// Run: node --test cmd/odek/ui/js/
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

afterEach(() => clarify.clearClarify());

function wsFixCard() { return el('messages').querySelector('.approval-card'); }
function wsFixButton(text) { return wsFixCard().querySelectorAll('button').find(b => b.textContent === text) || wsFixCard().querySelector('.approve'); }

test('RED: clarify_rejected re-enables the card and shows the reason', () => {
  clarify.queueClarify({ id: 'rej', question: 'q', timeout_seconds: 300 });
  wsFixCard().querySelector('.ac-friction-input').value = 'too long';
  wsFixButton('send answer').click();
  assert.equal(sent.length, 1);
  assert.ok(wsFixCard().querySelectorAll('button').every(b => b.disabled), 'pending while waiting');
  assert.equal(typeof clarify.rejectClarify, 'function', 'rejectClarify exported');
  clarify.rejectClarify('rej', 'answer exceeds 32000 characters');
  assert.ok(wsFixCard(), 'card stays: the question is still pending');
  assert.ok(wsFixCard().querySelectorAll('button').every(b => !b.disabled), 'buttons re-enabled');
  const notes = wsFixCard().querySelectorAll('.management-note');
  assert.equal(notes.length, 1, 'the waiting note is replaced, not stacked');
  assert.match(notes[0].textContent, /not accepted: answer exceeds 32000 characters/);
  wsFixCard().querySelector('.ac-friction-input').value = 'short';
  wsFixButton('send answer').click();
  assert.deepEqual(sent.at(-1), { type: 'clarify_response', id: 'rej', answer: 'short', action: 'answer' });
});

test('RED: clarify_rejected for another question id is ignored', () => {
  clarify.queueClarify({ id: 'mine', question: 'q', timeout_seconds: 300 });
  wsFixCard().querySelector('.ac-friction-input').value = 'x';
  wsFixButton('send answer').click();
  clarify.rejectClarify?.('other', 'nope');
  assert.ok(wsFixCard().querySelectorAll('button').every(b => b.disabled), 'still pending');
});
