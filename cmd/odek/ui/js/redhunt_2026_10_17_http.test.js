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
    removeItem: (k) => { delete store[k]; },
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

test('RED: a rejected stored token is forgotten and the error tells the user what to do', async () => {
  const { apiFetch } = await import('./api.js');
  store.odek_ws_token = 'old';
  globalThis.fetch = async () => ({ ok: false, status: 403, text: async () => 'Forbidden\n' });
  await assert.rejects(apiFetch('/api/health'), (e) => /token URL/.test(e.message) && e.status === 403);
  assert.equal(store.odek_ws_token, undefined);
});

test('RED: a Host rejection does not clear the stored token', async () => {
  const { apiFetch } = await import('./api.js');
  store.odek_ws_token = 'keep';
  globalThis.fetch = async () => ({ ok: false, status: 403, text: async () => 'Host not allowed' });
  await assert.rejects(apiFetch('/api/health'));
  assert.equal(store.odek_ws_token, 'keep');
});
