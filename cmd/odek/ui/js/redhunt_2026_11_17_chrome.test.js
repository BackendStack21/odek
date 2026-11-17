// Web UI chrome regressions: shortcut sheet, session drawer, top bar, queue,
// scroll button, status rail wording, plurals, metrics, palette destinations.
// Run: node --test cmd/odek/ui/js/redhunt_2026_11_17_chrome.test.js
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// ── Mini-DOM: one shared event object per dispatch (so preventDefault is
// visible to later listeners), bubbling to document listeners, focus
// tracking and data-* parsing for innerHTML-rendered rows. ──
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

const docListeners = {};
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
    this.open = false;
    this.isContentEditable = false;
    this.selectionStart = 0;
    this.selectionEnd = 0;
    this.scrollTop = 0;
    this.scrollHeight = 100;
    this.clientHeight = 100;
    this.scrolledIntoView = 0;
    this._listeners = {};
    this._attrs = {};
    this._innerHTML = '';
    this.style = { setProperty() {} };
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
            if (!/\/>$/.test(tok) && !/^<(input|br|img)\b/.test(tok)) stack.push(el);
            continue;
          }
          parent.textContent += tok;
        }
      },
    });
  }
  get id() { return this._id || ''; }
  set id(v) { this._id = v; }
  get isConnected() { return true; }
  setAttribute(k, v) { this._attrs[k] = String(v); if (k === 'id') this.id = v; }
  getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; }
  removeAttribute(k) { delete this._attrs[k]; }
  matches(sel) {
    if (sel.startsWith('.')) return sel.slice(1).split('.').every((c) => c && this.classList.contains(c));
    if (sel.startsWith('#')) return this.id === sel.slice(1);
    return this.tagName === sel.toUpperCase();
  }
  closest(sel) { let el = this; while (el) { if (el.matches?.(sel)) return el; el = el.parentNode; } return null; }
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
  append(...cs) { cs.forEach(c => this.appendChild(typeof c === 'string' ? Object.assign(new FakeEl('#text'), { textContent: c }) : c)); }
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
  dispatch(type, init = {}) { return fire(this, type, init); }
  scrollIntoView() { this.scrolledIntoView++; }
  scrollTo(opts) { this.scrollTop = opts?.top ?? this.scrollTop; }
  getBoundingClientRect() { return { top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0 }; }
  focus() { document.activeElement = this; }
  blur() { if (document.activeElement === this) document.activeElement = document.body; }
  click() { if (!this.disabled) this.dispatch('click'); }
}

// fire dispatches one shared event object through the element chain and
// then the document listeners, as a bubbling DOM event would.
function fire(target, type, init = {}) {
  const ev = {
    type, target, key: '', shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, isComposing: false,
    defaultPrevented: false, propagationStopped: false,
    preventDefault() { this.defaultPrevented = true; },
    stopPropagation() { this.propagationStopped = true; },
    getModifierState() { return false; },
    ...init,
  };
  let el = target;
  while (el && !ev.propagationStopped) {
    ev.currentTarget = el;
    for (const fn of [...(el._listeners?.[type] || [])]) fn(ev);
    el = el.parentNode;
  }
  if (!ev.propagationStopped) {
    ev.currentTarget = document;
    for (const fn of [...(docListeners[type] || [])]) fn(ev);
  }
  return ev;
}

const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
};
globalThis.sessionStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const byId = {};
const bySelector = {};
const body = new FakeEl('body');
globalThis.document = {
  getElementById: (id) => (byId[id] ||= Object.assign(new FakeEl(id === 'prompt' ? 'textarea' : id === 'sidebar-search' ? 'input' : 'div'), { id })),
  createElement: (tag) => new FakeEl(tag),
  createTextNode: (text) => Object.assign(new FakeEl('#text'), { textContent: text }),
  querySelector: (sel) => (bySelector[sel] ||= new FakeEl(sel.includes('button') || sel.startsWith('.new-session') ? 'button' : 'div')),
  querySelectorAll: (sel) => (bySelector[sel] ? [bySelector[sel]] : []),
  addEventListener: (type, fn) => { (docListeners[type] ||= []).push(fn); },
  removeEventListener: (type, fn) => { const l = docListeners[type] || []; const i = l.indexOf(fn); if (i >= 0) l.splice(i, 1); },
  body,
  documentElement: new FakeEl('html'),
  activeElement: body,
  hidden: false,
};
globalThis.location = { protocol: 'http:', host: 'test.local' };
globalThis.window = globalThis;
const windowListeners = {};
globalThis.addEventListener = (type, fn) => { (windowListeners[type] ||= []).push(fn); };
globalThis.Event = class { constructor(type, opts) { this.type = type; Object.assign(this, opts || {}); } };
globalThis.CSS = { escape: (s) => String(s) };
globalThis.confirm = () => true;
// Viewport width queries: narrow unless a test widens it.
let wideViewport = false;
const mediaListeners = [];
globalThis.matchMedia = (q) => ({
  get matches() { return /min-width:\s*1100px/.test(q) ? wideViewport : false; },
  media: q,
  addEventListener(_t, fn) { mediaListeners.push(fn); },
  removeEventListener() {},
});
function setWide(on) { wideViewport = on; mediaListeners.forEach((fn) => fn()); }
globalThis.WebSocket = class FakeWebSocket {
  static get OPEN() { return 1; }
  constructor() { this.readyState = 1; this.sent = []; }
  send(m) { this.sent.push(m); }
  close() { this.readyState = 3; }
};
let fetchImpl = async () => ({ ok: true, status: 200, headers: { get: () => null }, text: async () => '', json: async () => ({}) });
globalThis.fetch = (...a) => fetchImpl(...a);
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 0);
globalThis.cancelAnimationFrame = (t) => clearTimeout(t);

const S = (await import('./state.js')).S;
const utils = await import('./utils.js');
const render = await import('./render.js');
const input = await import('./input.js');
const sessions = await import('./sessions.js');
const ws = await import('./ws.js');
const health = await import('./health.js');
await import('./workspace.js');
const panels = await import('./panels.js');

const $ = (id) => document.getElementById(id);
const wait = (ms) => new Promise(r => setTimeout(r, ms));
const press = (target, init) => fire(target, 'keydown', init);
function deliver(event) { S.ws.onmessage({ data: JSON.stringify(event) }); }
function sentOfType(type) { return S.ws.sent.map(m => JSON.parse(m)).filter(m => m.type === type); }

beforeEach(() => {
  ws.connect();
  S.ws.onopen();
  health.stopHeartbeat();
  if (utils.isDialogOpen()) utils.closeDialog();
  byId.messages.children.length = 0;
  render.resetTurnState();
  S.currentTurnId = null;
  S.busy = false;
  S.queuePaused = false;
  S.promptQueue.length = 0;
  S.sessionId = null;
  S.recoveryReason = '';
  byId.prompt.value = '';
  document.activeElement = body;
});

// ── Shortcut sheet ──
// `?` is Shift+/ on US and most layouts, so a handler that requires
// !shiftKey never fires; the sheet must also open when focus is outside
// the composer, without stealing `?` typed into a field.
test('Shift+? on the empty composer toggles the shortcut sheet', () => {
  const overlay = $('shortcuts-overlay');
  byId.prompt.focus();
  const ev = press(byId.prompt, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), true, 'sheet opened from the empty composer');
  assert.equal(ev.defaultPrevented, true, 'the ? is not typed into the composer');
  press(document.activeElement, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, '? closes the open sheet');
});

test('? with focus outside any text field opens the shortcut sheet', () => {
  const overlay = $('shortcuts-overlay');
  press(body, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), true, 'sheet opened from the page');
  utils.closeDialog();
  const button = $('palette-btn');
  button.tagName = 'BUTTON';
  press(button, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), true, 'sheet opened while a button holds focus');
});

test('? typed into a field with content or another text field is left alone', () => {
  const overlay = $('shortcuts-overlay');
  byId.prompt.value = 'what is this';
  let ev = press(byId.prompt, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, 'composer with text keeps the ?');
  assert.equal(ev.defaultPrevented, false);
  byId.prompt.value = '';
  ev = press($('sidebar-search'), { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, 'empty search input keeps the ?');
  assert.equal(ev.defaultPrevented, false);
  const area = new FakeEl('textarea');
  ev = press(area, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, 'other textarea keeps the ?');
  const sel = new FakeEl('select');
  press(sel, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, 'select keeps the ?');
  const editable = new FakeEl('div');
  editable.isContentEditable = true;
  ev = press(editable, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(overlay), false, 'contenteditable keeps the ?');
  assert.equal(ev.defaultPrevented, false);
});

test('Cmd/Ctrl chords and IME composition never toggle the sheet; AltGr does', () => {
  const overlay = $('shortcuts-overlay');
  press(body, { key: '?', shiftKey: true, metaKey: true });
  press(body, { key: '?', shiftKey: true, ctrlKey: true });
  press(body, { key: '?', isComposing: true });
  assert.equal(utils.isDialogOpen(overlay), false);
  // AltGr reports Ctrl+Alt on Windows layouts that need it for '?'.
  press(body, { key: '?', ctrlKey: true, altKey: true });
  assert.equal(utils.isDialogOpen(overlay), true);
});

test('? does not replace another open dialog', () => {
  const confirmOverlay = $('confirm-overlay');
  utils.openDialog(confirmOverlay);
  press(body, { key: '?', shiftKey: true });
  assert.equal(utils.isDialogOpen(confirmOverlay), true, 'the confirm dialog stays open');
  assert.equal(utils.isDialogOpen($('shortcuts-overlay')), false);
  utils.closeDialog();
});

// ── Session drawer ──
// Below the wide-rail breakpoint the session rail is a drawer over the
// transcript; picking a session (or starting a new one) must close it.
function drawerState() {
  return {
    hidden: $('session-rail').hidden,
    expanded: $('hamburger-btn').getAttribute('aria-expanded'),
    open: document.body.classList.contains('sessions-open'),
  };
}
function sessionRow(id) {
  const list = $('session-list');
  if (list.parentNode !== $('sidebar')) $('sidebar').appendChild(list);
  list.innerHTML = '<div class="session-item" data-id="' + id + '"><button class="si-body" type="button">row</button></div>';
  return list.querySelector('.si-body');
}
function openDrawer() {
  if (!S.sessionRailOpen) S.toggleSessionRail();
  assert.deepEqual(drawerState(), { hidden: false, expanded: 'true', open: true });
}

test('picking a session closes the drawer below the wide-rail width and returns focus to its opener', async () => {
  setWide(false);
  openDrawer();
  const row = sessionRow('sess-picked');
  row.focus();
  fire(row, 'click');
  await wait(0);
  assert.deepEqual(drawerState(), { hidden: true, expanded: 'false', open: false });
  await wait(10);
  assert.equal(document.activeElement, $('hamburger-btn'), 'focus leaves the hidden drawer for the menu button');
});

test('tapping the already-open session row also closes the drawer', () => {
  setWide(false);
  S.sessionId = 'sess-current';
  openDrawer();
  fire(sessionRow('sess-current'), 'click');
  assert.deepEqual(drawerState(), { hidden: true, expanded: 'false', open: false });
});

test('New session closes the drawer below the wide-rail width', () => {
  setWide(false);
  openDrawer();
  fire(document.querySelector('.new-session-btn'), 'click');
  assert.deepEqual(drawerState(), { hidden: true, expanded: 'false', open: false });
});

test('the docked rail stays open when a session is picked at the wide width', async () => {
  setWide(true);
  openDrawer();
  fire(sessionRow('sess-wide'), 'click');
  await wait(0);
  fire(document.querySelector('.new-session-btn'), 'click');
  assert.deepEqual(drawerState(), { hidden: false, expanded: 'true', open: true });
  S.toggleSessionRail();
  setWide(false);
  await wait(10);
});

test('cancelling the busy-turn confirmation keeps the drawer open', () => {
  setWide(false);
  openDrawer();
  S.busy = true;
  globalThis.confirm = () => false;
  try {
    fire(sessionRow('sess-other'), 'click');
    fire(document.querySelector('.new-session-btn'), 'click');
  } finally { globalThis.confirm = () => true; }
  assert.deepEqual(drawerState(), { hidden: false, expanded: 'true', open: true });
  S.busy = false;
  S.toggleSessionRail();
});

test('deleting the open session from the drawer keeps the drawer open', async () => {
  setWide(false);
  S.sessionId = 'sess-doomed';
  S.sessionPages = [{ id: 'sess-doomed', task: 'doomed' }];
  openDrawer();
  S.pendingDeleteId = 'sess-doomed';
  await sessions.executeDeleteSession();
  assert.equal(S.sessionId, null, 'the deleted session was left for a new one');
  assert.deepEqual(drawerState(), { hidden: false, expanded: 'true', open: true }, 'the operator keeps managing sessions');
  S.toggleSessionRail();
  S.sessionPages = [];
  await wait(250);
});

test('closing an inspector that is not open leaves focus where it is', () => {
  $('prompt').focus();
  S.closePanels();
  assert.equal(document.activeElement, $('prompt'));
  setWide(false);
  openDrawer();
  $('sidebar-search').focus();
  fire(document.querySelector('.new-session-btn'), 'click');
  assert.equal(document.activeElement, $('prompt'), 'a new session leaves focus in the composer');
  panels.togglePanels(true);
  $('ptab-now').focus();
  panels.togglePanels(false);
  assert.notEqual(document.activeElement, $('ptab-now'), 'closing an open inspector still restores its opener');
});
