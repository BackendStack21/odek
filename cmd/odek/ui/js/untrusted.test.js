// Golden tests for the untrusted-content envelope parser (untrusted.js).
// Mirrors the grammar produced by wrapUntrusted in cmd/odek/untrusted.go and
// pins the client-side-sanitization contract: the server sends raw content,
// the client unwraps the model-facing envelope and escapes the body itself.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  parseUntrusted, unwrapUntrusted, hasUntrustedWrapper, stripAttachmentBodies,
} from './untrusted.js';
import { escapeHtml } from './escape.js';
import { markdownToHtml } from './markdown.js';

function wrap(nonce, source, body) {
  return '<untrusted_content_' + nonce + ' source="' + source + '">\n' +
    body + '\n</untrusted_content_' + nonce + '>';
}

test('single envelope: body extracted, source captured', () => {
  const segs = parseUntrusted(wrap('a1b2c3d4e5f60718', 'shell', 'hello world'));
  assert.deepEqual(segs, [{ source: 'shell', body: 'hello world' }]);
  assert.equal(unwrapUntrusted(wrap('a1b2c3d4e5f60718', 'shell', 'hello world')), 'hello world');
});

test('multiple envelopes in one payload keep order', () => {
  const text = wrap('aaaaaaaaaaaaaaaa', 'shell', 'one') +
    '\nplain between\n' +
    wrap('bbbbbbbbbbbbbbbb', 'browser:https://example.com', 'two');
  const segs = parseUntrusted(text);
  assert.deepEqual(segs, [
    { source: 'shell', body: 'one' },
    { source: null, body: '\nplain between\n' },
    { source: 'browser:https://example.com', body: 'two' },
  ]);
});

test('nonce mismatch between open and close is not parsed', () => {
  const forged = '<untrusted_content_aaaaaaaaaaaaaaaa source="shell">\n' +
    'body\n</untrusted_content_bbbbbbbbbbbbbbbb>';
  assert.deepEqual(parseUntrusted(forged), [{ source: null, body: forged }]);
  assert.equal(hasUntrustedWrapper(forged), false);
});

test('text without envelope passes through unchanged', () => {
  assert.deepEqual(parseUntrusted('plain output'), [{ source: null, body: 'plain output' }]);
  assert.equal(unwrapUntrusted('plain output'), 'plain output');
  assert.equal(hasUntrustedWrapper('plain output'), false);
});

test('empty and nullish input', () => {
  assert.deepEqual(parseUntrusted(''), []);
  assert.equal(unwrapUntrusted(''), '');
  assert.equal(hasUntrustedWrapper(''), false);
});

test('body containing HTML survives parsing raw', () => {
  const body = '<b>bold</b> & <script>alert(1)</script>';
  const segs = parseUntrusted(wrap('0123456789abcdef', 'shell', body));
  assert.equal(segs[0].body, body);
});

test('render chain escapes unwrapped bodies (client-side contract)', () => {
  const wrapped = wrap('0123456789abcdef', 'shell', '<script>alert(1)</script>');
  const body = unwrapUntrusted(wrapped);
  assert.equal(escapeHtml(body), '&lt;script&gt;alert(1)&lt;/script&gt;');
  // markdownToHtml escapes all input by default — callers must NOT pre-escape.
  assert.equal(markdownToHtml(body), '<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>');
  // Pin the double-escape regression: pre-escaping renders &lt; literally.
  assert.equal(markdownToHtml(escapeHtml(body)), '<p>&amp;lt;script&amp;gt;alert(1)&amp;lt;/script&amp;gt;</p>');
});

test('attachment envelope collapses to a chip, bodies dropped', () => {
  const text = wrap('a1a1a1a1a1a1a1a1', 'attachment:notes.txt', '--- notes.txt ---\nfile body here') +
    '\n\nwhat is in this file?';
  assert.equal(stripAttachmentBodies(text), '📎 notes.txt\n\n\nwhat is in this file?');
});

test('non-attachment envelopes pass through unwrapped', () => {
  const text = wrap('c3c3c3c3c3c3c3c3', 'resource:@README.md', 'readme body');
  assert.equal(stripAttachmentBodies(text), 'readme body');
});

test('unwrapForDisplay strips neutralised inner envelopes for display', async () => {
  const { unwrapForDisplay } = await import('./untrusted.js');
  const inner = '<untrusted·content_93083945 source="main.go">\nmain.go\n</untrusted·content_93083945>';
  assert.equal(unwrapForDisplay(inner), 'main.go');
  assert.equal(unwrapForDisplay('path: ' + inner + ' (line 3)'), 'path: main.go (line 3)');
  // An outer envelope and a nested neutralised one both unwrap.
  const outer = '<untrusted_content_ab12 source="tool:read_file">\n' + inner + '\n</untrusted_content_ab12>';
  assert.equal(unwrapForDisplay(outer), 'main.go');
  // Mismatched nonces stay literal.
  const forged = '<untrusted·content_aa source="x">y</untrusted·content_bb>';
  assert.equal(unwrapForDisplay(forged), forged);
});

test('unwrapForDisplay stays linear on hostile opener floods', async () => {
  const { unwrapForDisplay } = await import('./untrusted.js');
  const flood = '<untrusted·content_a source="">'.repeat(60000) + 'x';
  const t = Date.now();
  assert.equal(unwrapForDisplay(flood), flood);
  assert.ok(Date.now() - t < 500, 'took ' + (Date.now() - t) + 'ms');
});

test('parseUntrusted stays linear when openers never close', () => {
  const flood = '<untrusted_content_a source="x">'.repeat(60000) + '</untrusted_content_b>';
  const t = Date.now();
  const segs = parseUntrusted(flood);
  assert.ok(Date.now() - t < 500, 'took ' + (Date.now() - t) + 'ms');
  assert.deepEqual(segs, [{ source: null, body: flood }]);
  assert.equal(hasUntrustedWrapper(flood), false);
});
