// DOM-shim tests for the clarify card.
// Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
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
  assert.deepEqual(sent, [{ type: 'clarify_response', id: 'clr-1', answer: 'the first one' }]);
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
