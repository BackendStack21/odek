// Golden tests for the streaming-safe markdown tokenizer.
// Run: node --test cmd/odek/ui/js/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { markdownToHtml, isSafeHref } from './markdown.js';

// ── Blocks ──

test('headers h1–h4', () => {
  assert.equal(markdownToHtml('# T'), '<h1>T</h1>');
  assert.equal(markdownToHtml('## T'), '<h2>T</h2>');
  assert.equal(markdownToHtml('### T'), '<h3>T</h3>');
  assert.equal(markdownToHtml('#### T'), '<h4>T</h4>');
  // Five hashes is not a header
  assert.equal(markdownToHtml('##### T'), '<p>##### T</p>');
});

test('horizontal rules', () => {
  assert.equal(markdownToHtml('---'), '<hr>');
  assert.equal(markdownToHtml('***'), '<hr>');
  assert.equal(markdownToHtml('___'), '<hr>');
});

test('fenced code block with language', () => {
  assert.equal(
    markdownToHtml('```go\nfmt.Println("hi")\n```'),
    '<div class="code-block"><div class="cb-header"><span class="cb-lang">go</span>' +
    '<button class="cb-copy">📋 copy</button></div>' +
    '<pre><code>fmt.Println("hi")\n</code></pre></div>'
  );
});

test('fenced code block without language defaults to "code"', () => {
  const html = markdownToHtml('```\nx = 1\n```');
  assert.ok(html.includes('<span class="cb-lang">code</span>'));
  assert.ok(html.includes('<pre><code>x = 1\n</code></pre>'));
});

test('unterminated fence renders collected lines as a code block', () => {
  const html = markdownToHtml('```js\nconsole.log(1)\nconsole.log(2)');
  assert.equal(
    html,
    '<div class="code-block"><div class="cb-header"><span class="cb-lang">js</span>' +
    '<button class="cb-copy">📋 copy</button></div>' +
    '<pre><code>console.log(1)\nconsole.log(2)\n</code></pre></div>'
  );
});

test('code fence content is escaped, never re-processed', () => {
  const html = markdownToHtml('```\n**not bold** <b>x</b>\n```');
  assert.ok(html.includes('<pre><code>**not bold** &lt;b&gt;x&lt;/b&gt;\n</code></pre>'));
  assert.ok(!html.includes('<strong>'));
});

test('unordered list', () => {
  assert.equal(
    markdownToHtml('- one\n- two'),
    '<ul><li>one</li><li>two</li></ul>'
  );
  assert.equal(
    markdownToHtml('* one\n* two'),
    '<ul><li>one</li><li>two</li></ul>'
  );
});

test('ordered list', () => {
  assert.equal(
    markdownToHtml('1. one\n2. two'),
    '<ol><li>one</li><li>two</li></ol>'
  );
});

test('list items support inline markup', () => {
  assert.equal(
    markdownToHtml('- a **b** c'),
    '<ul><li>a <strong>b</strong> c</li></ul>'
  );
});

// ── Inline spans ──

test('bold', () => {
  assert.equal(markdownToHtml('a **b** c'), '<p>a <strong>b</strong> c</p>');
});

test('italic', () => {
  assert.equal(markdownToHtml('a *b* c'), '<p>a <em>b</em> c</p>');
});

test('italic not inside words', () => {
  assert.equal(markdownToHtml('a*b*c'), '<p>a*b*c</p>');
});

test('strikethrough', () => {
  assert.equal(markdownToHtml('a ~~b~~ c'), '<p>a <s>b</s> c</p>');
});

test('inline code', () => {
  assert.equal(markdownToHtml('use `npm test` now'), '<p>use <code>npm test</code> now</p>');
});

test('inline code content is never re-processed', () => {
  assert.equal(markdownToHtml('`**x**`'), '<p><code>**x**</code></p>');
});

test('unterminated bold renders literally', () => {
  assert.equal(markdownToHtml('**bold'), '<p>**bold</p>');
});

test('unterminated inline code renders literally', () => {
  assert.equal(markdownToHtml('some `code'), '<p>some `code</p>');
});

test('unterminated strikethrough renders literally', () => {
  assert.equal(markdownToHtml('~~strike'), '<p>~~strike</p>');
});

// ── Links ──

test('http link renders anchor with safe attributes', () => {
  assert.equal(
    markdownToHtml('[site](https://example.com)'),
    '<p><a href="https://example.com" target="_blank" rel="noopener noreferrer">site</a></p>'
  );
});

test('relative and anchor links are allowed', () => {
  for (const url of ['/abs/path', './rel', '../up', '#frag', 'mailto:a@b.c']) {
    const html = markdownToHtml('[x](' + url + ')');
    assert.ok(html.includes('<a href="' + url + '"'), url);
  }
});

test('javascript: link renders as plain text', () => {
  const html = markdownToHtml('[click](javascript:alert)');
  assert.ok(!html.includes('<a'));
  assert.equal(html, '<p>click</p>');
  // URL with parens: parsing stops at the first ')' (same as the old
  // regex pipeline), leaving the trailing ')' as literal text.
  const html2 = markdownToHtml('[click](javascript:alert(1))');
  assert.ok(!html2.includes('<a'));
  assert.equal(html2, '<p>click)</p>');
});

test('data: link renders as plain text', () => {
  const html = markdownToHtml('[click](data:text/html;base64,xxxx)');
  assert.ok(!html.includes('<a'));
  assert.equal(html, '<p>click</p>');
});

test('protocol-relative and token/api links render as plain text', () => {
  for (const url of ['//evil.example', '/api/shutdown', '/?token=abc', 'blob:https://x', 'javascript:alert(1)']) {
    const html = markdownToHtml('[x](' + url + ')');
    assert.ok(!html.includes('<a'), url);
  }
});

test('isSafeHref rejects api/token bypasses and encoded paths', () => {
  for (const url of [
    './api/shutdown', '../api/shutdown', '/API/health', '/%61pi/shutdown',
    '/?TOKEN=abc', '/docs?token=x', 'https://example.com/api/x?token=1',
    '\\\\evil', 'javascript:alert', 'data:text/html,x',
  ]) {
    assert.equal(isSafeHref(url), false, url);
  }
  assert.equal(isSafeHref('https://example.com'), true);
  assert.equal(isSafeHref('/abs/path'), true);
  assert.equal(isSafeHref('#frag'), true);
});

// ── Escaping & paragraphs ──

test('raw HTML in input is escaped', () => {
  assert.equal(
    markdownToHtml('<script>alert(1)</script>'),
    '<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>'
  );
});

test('paragraphs split on blank lines, single newline becomes <br>', () => {
  assert.equal(
    markdownToHtml('one\ntwo\n\nthree'),
    '<p>one<br>two</p>\n<p>three</p>'
  );
});

test('empty input', () => {
  assert.equal(markdownToHtml(''), '');
});

// ── Golden multi-feature document ──

test('plus-sign bullets are unordered lists', () => {
  assert.equal(markdownToHtml('+ one\n+ two'), '<ul><li>one</li><li>two</li></ul>');
});

test('underscore emphasis matches star emphasis', () => {
  assert.equal(markdownToHtml('a __b__ c'), '<p>a <strong>b</strong> c</p>');
  assert.equal(markdownToHtml('a _b_ c'), '<p>a <em>b</em> c</p>');
  assert.equal(markdownToHtml('a_b_c'), '<p>a_b_c</p>');
});

test('blockquote wraps quoted lines and keeps a blank between them', () => {
  assert.equal(
    markdownToHtml('> one\n> two'),
    '<blockquote><p>one<br>two</p></blockquote>'
  );
  assert.equal(
    markdownToHtml('> one\n>\n> two'),
    '<blockquote><p>one</p>\n<p>two</p></blockquote>'
  );
});

test('nested quotes recurse', () => {
  const html = markdownToHtml('> outer\n> > inner');
  assert.equal(html, '<blockquote><p>outer</p>\n<blockquote><p>inner</p></blockquote></blockquote>');
});

test('task list items stay inert and mark checked state', () => {
  assert.equal(
    markdownToHtml('- [ ] open\n- [x] done\n- [X] also'),
    '<ul class="task-list">' +
      '<li class="task"><span class="task-mark" aria-hidden="true">☐</span>open</li>' +
      '<li class="task"><span class="task-mark on" aria-hidden="true">☑</span>done</li>' +
      '<li class="task"><span class="task-mark on" aria-hidden="true">☑</span>also</li>' +
    '</ul>'
  );
});

test('GFM table renders escaped cells and alignment classes', () => {
  const html = markdownToHtml('| A | B |\n| :--- | ---: |\n| **x** | <y> |');
  assert.equal(
    html,
    '<div class="md-table-wrap"><table><thead><tr>' +
      '<th class="ta-l">A</th><th class="ta-r">B</th>' +
    '</tr></thead><tbody><tr>' +
      '<td class="ta-l"><strong>x</strong></td><td class="ta-r">&lt;y&gt;</td>' +
    '</tr></tbody></table></div>'
  );
});

test('a table header without a separator stays a paragraph (streaming)', () => {
  assert.equal(markdownToHtml('| A | B |'), '<p>| A | B |</p>');
});

test('angle and bare autolinks use the same href allowlist', () => {
  assert.equal(
    markdownToHtml('see <https://example.com> now'),
    '<p>see <a href="https://example.com" target="_blank" rel="noopener noreferrer">https://example.com</a> now</p>'
  );
  assert.equal(
    markdownToHtml('see https://example.com.'),
    '<p>see <a href="https://example.com" target="_blank" rel="noopener noreferrer">https://example.com</a>.</p>'
  );
  const bad = markdownToHtml('see <javascript:alert(1)>');
  assert.ok(!bad.includes('<a'));
});

test('images never become img tags — safe URLs are caption links', () => {
  const html = markdownToHtml('![diagram](https://example.com/a.png)');
  assert.ok(!html.includes('<img'));
  assert.equal(
    html,
    '<p><a href="https://example.com/a.png" target="_blank" rel="noopener noreferrer" class="md-img">diagram</a></p>'
  );
  const unsafe = markdownToHtml('![x](javascript:alert(1))');
  assert.ok(!unsafe.includes('<a'));
  assert.ok(!unsafe.includes('<img'));
});

test('golden document', () => {
  const doc = [
    '# Title',
    '',
    'Some **bold** and *italic* text with `code`.',
    '',
    '- one',
    '- two',
    '',
    '```js',
    'console.log("hi");',
    '```',
    '',
    'Check [link](https://example.com) out.',
  ].join('\n');

  const expected = [
    '<h1>Title</h1>',
    '<p>Some <strong>bold</strong> and <em>italic</em> text with <code>code</code>.</p>',
    '<ul><li>one</li><li>two</li></ul>',
    '<div class="code-block"><div class="cb-header"><span class="cb-lang">js</span>' +
      '<button class="cb-copy">📋 copy</button></div>' +
      '<pre><code>console.log("hi");\n</code></pre></div>',
    '<p>Check <a href="https://example.com" target="_blank" rel="noopener noreferrer">link</a> out.</p>',
  ].join('\n');

  assert.equal(markdownToHtml(doc), expected);
});
