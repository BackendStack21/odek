import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeThinking, seedThinking, persistThinking, THINKING_LEVELS } from './thinking.js';

test('normalizeThinking maps aliases and rejects unknown', () => {
  assert.equal(normalizeThinking(''), '');
  assert.equal(normalizeThinking('  LOW '), 'low');
  assert.equal(normalizeThinking('medium'), 'medium');
  assert.equal(normalizeThinking('high'), 'high');
  assert.equal(normalizeThinking('disabled'), 'disabled');
  assert.equal(normalizeThinking('mid'), 'medium');
  assert.equal(normalizeThinking('enabled'), 'medium');
  assert.equal(normalizeThinking('on'), 'medium');
  assert.equal(normalizeThinking('true'), 'medium');
  assert.equal(normalizeThinking('1'), 'medium');
  assert.equal(normalizeThinking('max'), 'high');
  assert.equal(normalizeThinking('off'), 'disabled');
  assert.equal(normalizeThinking('false'), 'disabled');
  assert.equal(normalizeThinking('0'), 'disabled');
  assert.equal(normalizeThinking('banana'), '');
  assert.deepEqual(THINKING_LEVELS, ['disabled', 'low', 'medium', 'high']);
});

test('seedThinking prefers stored, then config string, then disabled', () => {
  assert.equal(seedThinking('low', 'high'), 'low');
  assert.equal(seedThinking('', 'high'), 'high');
  assert.equal(seedThinking('', 'enabled'), 'medium');
  assert.equal(seedThinking('', true), 'medium');
  assert.equal(seedThinking('', false), 'disabled');
  assert.equal(seedThinking('', ''), 'disabled');
  assert.equal(seedThinking('', null), 'disabled');
  assert.equal(seedThinking('nope', 'medium'), 'medium');
});

test('persistThinking writes canonical localStorage', () => {
  const store = new Map();
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  assert.equal(persistThinking('enabled'), 'medium');
  assert.equal(store.get('odek_thinking'), 'medium');
  assert.equal(persistThinking('banana'), 'disabled');
  assert.equal(store.get('odek_thinking'), 'disabled');
});
