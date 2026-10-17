// "New session" must tell the server to detach the connection from the
// previous session (session_new frame), so the previous session's memory
// buffer, token totals and background wakes do not carry into the new view.
// Run: node --test cmd/odek/ui/js/
import { test, mock } from 'node:test';
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

// The composer allows 10 MiB of attachments plus the prompt; a frame that
// still exceeds the server's WebSocket cap (JSON escaping) is refused before
// sending, keeping the prompt and attachments, instead of being dropped by
// the server.
const input = await import('./input.js');

test('RED: promptFrameBytes measures the encoded frame', () => {
  assert.equal(typeof input.promptFrameBytes, 'function');
  assert.equal(input.WS_MESSAGE_LIMIT_BYTES, 16 * 1024 * 1024);
  const small = input.promptFrameBytes('hi', []);
  assert.ok(small > 0 && small < 8192);
  // JSON escaping and multi-byte characters both count.
  assert.ok(input.promptFrameBytes('', [{ name: 'a', content: '\n'.repeat(1000) }]) >= 2000);
  assert.ok(input.promptFrameBytes('日'.repeat(1000), []) >= 3000);
});

test('RED: an over-cap frame is not sent and the attachments stay', () => {
  const sock = new globalThis.WebSocket();
  S.ws = sock;
  S.busy = false;
  S.uploading = false;
  S.sessionId = null;
  const prompt = document.getElementById('prompt');
  prompt.value = 'summarise';
  // 9 MiB of control characters: within the composer caps, but each one
  // escapes to six bytes in JSON.
  S.attachedFiles.length = 0;
  S.attachedFiles.push({ name: 'ctl.bin', size: 9 * 1024 * 1024, content: '\u0001'.repeat(9 * 1024 * 1024) });
  input.send();
  assert.equal(sock.sent.filter(m => JSON.parse(m).type === 'prompt').length, 0, 'no prompt frame sent');
  assert.equal(S.attachedFiles.length, 1, 'attachments kept for editing');
  assert.equal(prompt.value, 'summarise', 'prompt kept');
  S.attachedFiles.length = 0;
});

// Until the server acknowledges session_new, frames of the session being
// left (a wake turn queued before the detach) must not re-adopt it in the
// fresh view.
const wsMod = await import('./ws.js');

test('RED: frames of the left session before session_new_ok are ignored', (t) => {
  // Heartbeat and jobs-watch intervals start on open/session; keep them fake.
  mock.timers.enable({ apis: ['setInterval', 'setTimeout'] });
  t.after(() => mock.timers.reset());
  wsMod.connect();
  const sock = S.ws;
  sock.onopen?.();
  S.busy = false;
  S.sessionId = 'old-session';
  sessions.newSession();
  assert.equal(S.sessionId, null);
  sock.onmessage({ data: JSON.stringify({ type: 'session', session_id: 'old-session', auth_token: 't' }) });
  sock.onmessage({ data: JSON.stringify({ type: 'turn_started', session_id: 'old-session', turn_id: 't_wake', initiated: 'system' }) });
  assert.equal(S.sessionId, null, 'the left session was re-adopted before the ack');
  assert.notEqual(S.currentTurnId, 't_wake', 'a turn of the left session opened in the new view');
  sock.onmessage({ data: JSON.stringify({ type: 'session_new_ok' }) });
  sock.onmessage({ data: JSON.stringify({ type: 'session', session_id: 'new-session', auth_token: 't2' }) });
  assert.equal(S.sessionId, 'new-session', 'frames after the ack are handled');
});

// A session_new the server could not queue is refused with
// session_new_rejected: the client re-sends it shortly after and keeps the
// left session's frames out meanwhile; once the retries are exhausted it stops
// waiting so the next session's frames are not dropped forever.
test('RED: a rejected session_new is retried, then the wait ends', (t) => {
  mock.timers.enable({ apis: ['setInterval', 'setTimeout'] });
  t.after(() => mock.timers.reset());
  wsMod.connect();
  const sock = S.ws;
  sock.onopen?.();
  S.busy = false;
  S.sessionId = 'old-session';
  sessions.newSession();
  const sentNew = () => sock.sent.filter(m => JSON.parse(m).type === 'session_new').length;
  assert.equal(sentNew(), 1);
  sock.onmessage({ data: JSON.stringify({ type: 'session_new_rejected', message: 'busy' }) });
  assert.equal(S.pendingSessionNew, true, 'still waiting for the detach');
  mock.timers.tick(5000);
  assert.equal(sentNew(), 2, 'session_new re-sent after a refusal');
  sock.onmessage({ data: JSON.stringify({ type: 'session', session_id: 'old-session', auth_token: 't' }) });
  assert.equal(S.sessionId, null, 'left session re-adopted while retrying');
  for (let i = 0; i < 20; i++) {
    sock.onmessage({ data: JSON.stringify({ type: 'session_new_rejected', message: 'busy' }) });
    mock.timers.tick(5000);
  }
  assert.equal(S.pendingSessionNew, false, 'gave up waiting after bounded retries');
  sock.onmessage({ data: JSON.stringify({ type: 'session', session_id: 'next-session', auth_token: 't2' }) });
  assert.equal(S.sessionId, 'next-session', 'the next session is adopted once the wait ends');
});

// One pending retry at a time: a second New session while a retry is pending
// cancels it, so the old timer cannot send an extra session_new.
test('RED: New session cancels a pending session_new retry', (t) => {
  mock.timers.enable({ apis: ['setInterval', 'setTimeout'] });
  t.after(() => mock.timers.reset());
  wsMod.connect();
  const sock = S.ws;
  sock.onopen?.();
  S.busy = false;
  S.sessionId = 'old-session';
  sessions.newSession();
  const sentNew = () => sock.sent.filter(m => JSON.parse(m).type === 'session_new').length;
  sock.onmessage({ data: JSON.stringify({ type: 'session_new_rejected', message: 'busy' }) });
  sock.onmessage({ data: JSON.stringify({ type: 'session_new_rejected', message: 'busy' }) });
  sessions.newSession();
  assert.equal(sentNew(), 2);
  mock.timers.tick(5000);
  assert.equal(sentNew(), 2, 'a stale retry timer sent an extra session_new');
  sock.onmessage({ data: JSON.stringify({ type: 'session_new_ok' }) });
  assert.equal(S.sessionNewRetries || 0, 0, 'retry count reset on ack');
});
