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

test('resource envelopes collapse to their reference, never their body', () => {
  const text = wrap('c3c3c3c3c3c3c3c3', 'resource:@README.md', 'readme body');
  assert.equal(stripAttachmentBodies(text), '@README.md');
});

test('other envelopes pass through unwrapped', () => {
  const text = wrap('c3c3c3c3c3c3c3c3', 'tool:read_file', 'file body');
  assert.equal(stripAttachmentBodies(text), 'file body');
});

test('unwrapField strips a field envelope only when it is the whole value', async () => {
  const { unwrapField } = await import('./untrusted.js');
  const inner = '<untrusted·content_93083945 source="main.go">\nmain.go\n</untrusted·content_93083945>';
  assert.equal(unwrapField(inner), 'main.go');
  // Text around an envelope is not what a per-field wrap produces.
  assert.equal(unwrapField('path: ' + inner + ' (line 3)'), 'path: ' + inner + ' (line 3)');
  // A field from a result the engine did not wrap again keeps the '_' form;
  // one level is removed, never more.
  const outer = '<untrusted_content_ab12 source="tool:read_file">\n' + inner + '\n</untrusted_content_ab12>';
  assert.equal(unwrapField(outer), inner);
  // Mismatched nonces stay literal.
  const forged = '<untrusted·content_aa source="x">y</untrusted·content_bb>';
  assert.equal(unwrapField(forged), forged);
});

test('displayJSON unwraps only the fields the tool wraps one by one', async () => {
  const { displayJSON } = await import('./untrusted.js');
  const env = (body) => '<untrusted·content_9a0f source="x">\n' + body + '\n</untrusted·content_9a0f>';
  const data = { matches: [{ path: env('a.go'), content: env('line'), note: env('kept') }], skipped: [env('b.go')], error: env('kept') };
  assert.deepEqual(displayJSON(data, 'search_files'), { matches: [{ path: 'a.go', content: 'line', note: env('kept') }], skipped: ['b.go'], error: env('kept') });
  assert.deepEqual(displayJSON(data, 'web_search'), data, 'a tool without per-field wraps shows every field as received');
  assert.deepEqual(displayJSON(data, '__proto__'), data);
  const tree = { tree: { path: env('/w'), children: [{ path: env('/w/a'), children: [{ path: env('/w/a/b') }] }] } };
  assert.deepEqual(displayJSON(tree, 'tree'), { tree: { path: '/w', children: [{ path: '/w/a', children: [{ path: '/w/a/b' }] }] } });
  assert.deepEqual(displayJSON({ value: { k: [env('v')] }, path: env('p') }, 'json_query'), { value: { k: ['v'] }, path: env('p') });
});

test('unwrapField stays linear on hostile opener floods', async () => {
  const { unwrapField } = await import('./untrusted.js');
  const flood = '<untrusted·content_a source="">'.repeat(60000) + 'x';
  const t = Date.now();
  assert.equal(unwrapField(flood), flood);
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

test('scanner stays linear on newline-free opener floods and rejects quoted sources', async () => {
  const { unwrapField, displayLabel } = await import('./untrusted.js');
  const flood = '<untrusted·content_ab source="x'.repeat(80000);
  let t = Date.now();
  assert.equal(unwrapField(flood + '</untrusted·content_ab>'), flood + '</untrusted·content_ab>');
  assert.ok(Date.now() - t < 500, 'took ' + (Date.now() - t) + 'ms');
  const forged = '<untrusted_content_ab source="attachment:x" foo="y">SECRET</untrusted_content_ab>';
  assert.deepEqual(parseUntrusted(forged), [{ source: null, body: forged }]);
  assert.equal(displayLabel('a‮b\u{E0001}c­'), 'a\\u{202E}b\\u{E0001}c\\u{00AD}');
});
