import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

let metaContent = '';
let store = {};

beforeEach(() => {
  metaContent = '';
  store = {};
  globalThis.document = {
    querySelector: (sel) => (sel.includes('odek-ws-token') ? { getAttribute: () => metaContent } : null),
  };
  globalThis.localStorage = {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
  };
});

const { getWsToken, apiHeaders } = await import('./net.js');

test('RED: token from the entry URL is kept in origin-scoped storage for later visits', () => {
  metaContent = 'tok-abc';
  assert.equal(getWsToken(), 'tok-abc');
  metaContent = '';
  assert.equal(getWsToken(), 'tok-abc');
  assert.equal(apiHeaders()['X-Odek-Ws-Token'], 'tok-abc');
});

test('RED: no token anywhere yields an empty token and no header', () => {
  assert.equal(getWsToken(), '');
  assert.equal(apiHeaders()['X-Odek-Ws-Token'], undefined);
});

test('RED: unavailable storage still returns the meta token', () => {
  globalThis.localStorage = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); } };
  metaContent = 'tok-x';
  assert.equal(getWsToken(), 'tok-x');
  metaContent = '';
  assert.equal(getWsToken(), '');
});
