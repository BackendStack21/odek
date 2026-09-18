import assert from 'node:assert/strict';
import { test } from 'node:test';
import { extractReferenceTokens } from './session-references.js';

test('extracts per-session tokens and deduplicates references', () => {
  const tokens = { one: 'tok-one', two: 'tok-two' };
  assert.deepEqual(
    extractReferenceTokens('read @sess:one and @sess:two, then @sess:one', id => tokens[id] || ''),
    tokens,
  );
});

test('does not substitute an unrelated token', () => {
  assert.deepEqual(extractReferenceTokens('@sess:foreign', () => ''), {});
});
