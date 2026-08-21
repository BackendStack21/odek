// Module-graph smoke test: executes the real main.js import chain under a
// minimal DOM shim. Catches missing cross-module exports, duplicate
// declarations, and other load-time errors that leave the page a dead
// static shell — none of which per-file syntax checks can see.
import { test } from 'node:test';
import assert from 'node:assert/strict';
const el = () => ({
  addEventListener: () => {}, classList: { add(){}, remove(){}, toggle(){}, contains(){ return false; } },
  style: {}, dataset: {}, setAttribute(){}, getAttribute(){ return null; },
  appendChild(){ return arguments[0]; }, append(){}, remove(){},
  insertAdjacentElement(){ return arguments[1]; }, insertBefore(){ return arguments[0]; },
  querySelector(){ return null; }, querySelectorAll(){ return []; }, closest(){ return null; },
  focus(){}, select(){}, click(){},
  innerHTML: '', textContent: '', value: '', contains(){ return false; },
});
globalThis.document = {
  getElementById: () => el(),
  querySelector: () => el(),
  querySelectorAll: () => [],
  createElement: () => el(),
  addEventListener: () => {},
  body: el(),
};
globalThis.localStorage = { getItem: () => null, setItem(){}, removeItem(){} };
globalThis.window = globalThis;
globalThis.WebSocket = class { constructor(){} send(){} close(){} };
globalThis.requestAnimationFrame = (fn) => setTimeout(fn, 16);
globalThis.cancelAnimationFrame = clearTimeout;
globalThis.location = { protocol: 'http:', host: '127.0.0.1:1' };
globalThis.performance = { now: () => Date.now() };
globalThis.fetch = async () => { throw new Error('offline'); };

test('UI module graph loads without errors', async () => {
  const base = new URL(import.meta.url);
  await assert.doesNotReject(import(new URL('main.js', base)));
});
