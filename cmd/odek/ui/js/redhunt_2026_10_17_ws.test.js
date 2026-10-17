// "New session" must tell the server to detach the connection from the
// previous session (session_new frame), so the previous session's memory
// buffer, token totals and background wakes do not carry into the new view.
// Run: node --test cmd/odek/ui/js/
import { test } from 'node:test';
import assert from 'node:assert/strict';

class WSFixClassList {
  constructor() { this.s = new Set(); }
  add(...c) { c.forEach(x => this.s.add(x)); }
  remove(...c) { c.forEach(x => this.s.delete(x)); }
  contains(c) { return this.s.has(c); }
  toggle(c, f) { const w = f === undefined ? !this.s.has(c) : !!f; if (w) this.s.add(c); else this.s.delete(c); return w; }
}

class WSFixEl {
  constructor(tag = 'div') {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.classList = new WSFixClassList();
    this.dataset = {};
    this.style = {};
    this.textContent = '';
    this.innerHTML = '';
    this.value = '';
    this.disabled = false;
    this.hidden = false;
  }
  setAttribute() {}
  getAttribute() { return null; }
  removeAttribute() {}
  addEventListener() {}
  removeEventListener() {}
  appendChild(c) { c.parentNode = this; this.children.push(c); return c; }
  append(...c) { c.forEach(x => this.appendChild(x)); }
  insertAdjacentElement(_p, n) { return this.appendChild(n); }
  insertBefore(c) { return this.appendChild(c); }
  remove() {}
  matches() { return false; }
  closest() { return null; }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  focus() {}
  blur() {}
  click() {}
  scrollIntoView() {}
  getBoundingClientRect() { return { top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0 }; }
}

const wsFixById = {};
const wsFixBySel = {};
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.sessionStorage = globalThis.localStorage;
globalThis.document = {
  getElementById: id => (wsFixById[id] ||= Object.assign(new WSFixEl('div'), { id })),
  createElement: tag => new WSFixEl(tag),
  createTextNode: t => Object.assign(new WSFixEl('#text'), { textContent: t }),
  querySelector: sel => (wsFixBySel[sel] ||= new WSFixEl('div')),
  querySelectorAll: () => [],
  addEventListener: () => {},
  body: new WSFixEl('body'),
  documentElement: new WSFixEl('html'),
  activeElement: null,
  hidden: false,
};
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.window = globalThis;
globalThis.confirm = () => true;
globalThis.WebSocket = class {
  static get OPEN() { return 1; }
  constructor() { this.readyState = 1; this.sent = []; }
  send(m) { this.sent.push(m); }
  close() { this.readyState = 3; }
};
globalThis.fetch = async () => ({ ok: true, headers: { get: () => null }, text: async () => '', json: async () => ({}) });
globalThis.requestAnimationFrame = fn => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = t => clearTimeout(t);

const { S } = await import('./state.js');
const sessions = await import('./sessions.js');

test('RED: newSession sends a session_new frame on an open socket', () => {
  const sock = new globalThis.WebSocket();
  S.ws = sock;
  S.busy = false;
  S.sessionId = 'previous-session';
  sessions.newSession();
  assert.equal(S.sessionId, null);
  const frames = sock.sent.map(m => JSON.parse(m));
  assert.ok(frames.some(f => f.type === 'session_new'), 'expected a session_new frame, got ' + JSON.stringify(frames));
});

test('RED: newSession on a closed socket sends nothing', () => {
  const sock = new globalThis.WebSocket();
  sock.readyState = 3;
  S.ws = sock;
  S.busy = false;
  S.sessionId = 'previous-session';
  sessions.newSession();
  assert.equal(sock.sent.length, 0);
});
