import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyToolResult, prettyToolBody, formatReceipt } from './tools.js';

test('diff output earns a +/− chip and never treats prose as a pass', () => {
  const chips = classifyToolResult('patch', '--- a\n+++ b\n+ok\n-old\n');
  const diff = chips.find((c) => c.kind === 'diff');
  assert.ok(diff);
  assert.match(diff.label, /\+1/);
  const prose = classifyToolResult('shell', 'Build passed with flying colors');
  assert.equal(prose.find((c) => c.kind === 'test'), undefined);
});

test('HTTP status and JSON pretty-print stay text-safe', () => {
  const http = classifyToolResult('shell', 'HTTP/1.1 404 Not Found');
  assert.equal(http.find((c) => c.kind === 'http').label, '404');
  assert.equal(http.find((c) => c.kind === 'http').tone, 'warn');
  assert.equal(prettyToolBody('{"a":1}'), '{\n  "a": 1\n}');
});

test('receipt formatter joins structured bits', () => {
  assert.equal(formatReceipt({ files: ['a'], plus: 2, minus: 1, tests: '✓ 3 passed', tools: 2 }),
    'touched 1 · +2 −1 · ✓ 3 passed');
});
