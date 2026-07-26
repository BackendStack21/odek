// Markdown → HTML via a small hand-written tokenizer (zero deps).
//
// Streaming-safe: an unterminated fenced code block renders its collected
// lines as a code block anyway (EOF acts as the fence close), while
// unterminated INLINE constructs (`**bold`, `` `code ``, `~~strike~~`)
// render as literal text — never as broken markup. All input is
// HTML-escaped by default; content inside code spans and fences is escaped
// but never re-processed. The copy button carries no inline handler —
// clicks are delegated on #messages in render.js.
import { escapeHtml } from './escape.js';

// Link scheme allowlist: http(s), mailto, and relative paths only —
// javascript:/data:/vbscript: render as plain text.
const SAFE_URL = /^(https?:|mailto:|\/|#|\.\/|\.\.\/)/i;
const WORD_CHAR = /[A-Za-z0-9]/;

export function markdownToHtml(text) {
  if (!text) return '';

  const lines = text.split('\n');
  const out = [];
  let i = 0;

  const fenceOpen = (l) => /^```(\w*)\s*$/.exec(l);
  const isFenceClose = (l) => /^```\s*$/.test(l);
  const headerMatch = (l) => /^(#{1,4})\s+(.+)$/.exec(l);
  const isHr = (l) => /^(---|\*\*\*|___)$/.test(l.trim());
  const ulItem = (l) => /^\s*[-*]\s+(.+)$/.exec(l);
  const olItem = (l) => /^\s*\d+\.\s+(.+)$/.exec(l);
  const isBlockStart = (l) =>
    !!fenceOpen(l) || !!headerMatch(l) || isHr(l) || !!ulItem(l) || !!olItem(l);

  while (i < lines.length) {
    const line = lines[i];

    if (line.trim() === '') { i++; continue; }

    // Fenced code block. An unterminated fence is closed by EOF so partial
    // code still renders as a block while streaming.
    const fence = fenceOpen(line);
    if (fence) {
      const lang = fence[1] || 'code';
      const buf = [];
      i++;
      while (i < lines.length && !isFenceClose(lines[i])) { buf.push(lines[i]); i++; }
      if (i < lines.length) i++; // consume the closing fence
      out.push(codeBlockHtml(lang, buf.length ? buf.join('\n') + '\n' : ''));
      continue;
    }

    const header = headerMatch(line);
    if (header) {
      const level = header[1].length;
      out.push('<h' + level + '>' + inlineHtml(header[2]) + '</h' + level + '>');
      i++;
      continue;
    }

    if (isHr(line)) { out.push('<hr>'); i++; continue; }

    if (ulItem(line)) {
      const items = [];
      while (i < lines.length) {
        const m = ulItem(lines[i]);
        if (!m) break;
        items.push('<li>' + inlineHtml(m[1]) + '</li>');
        i++;
      }
      out.push('<ul>' + items.join('') + '</ul>');
      continue;
    }

    if (olItem(line)) {
      const items = [];
      while (i < lines.length) {
        const m = olItem(lines[i]);
        if (!m) break;
        items.push('<li>' + inlineHtml(m[1]) + '</li>');
        i++;
      }
      out.push('<ol>' + items.join('') + '</ol>');
      continue;
    }

    // Paragraph — consecutive lines up to a blank line or the next block.
    const para = [];
    while (i < lines.length && lines[i].trim() !== '' && !isBlockStart(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    out.push('<p>' + para.map(inlineHtml).join('<br>') + '</p>');
  }

  return out.join('\n');
}

function codeBlockHtml(lang, code) {
  return '<div class="code-block">' +
    '<div class="cb-header">' +
      '<span class="cb-lang">' + escapeHtml(lang) + '</span>' +
      '<button class="cb-copy">📋 copy</button>' +
    '</div>' +
    '<pre><code>' + escapeHtml(code) + '</code></pre>' +
  '</div>';
}

// Inline tokenizer: scans left to right, emitting literal runs through
// escapeHtml. Every construct requires a closing delimiter; otherwise the
// opener is emitted literally and scanning continues after it.
function inlineHtml(text) {
  let out = '';
  let lit = '';
  const flush = () => { if (lit) { out += escapeHtml(lit); lit = ''; } };
  let i = 0;

  while (i < text.length) {
    const ch = text[i];

    // Inline code — content is escaped, never re-processed.
    if (ch === '`') {
      const end = text.indexOf('`', i + 1);
      if (end > i + 1) {
        flush();
        out += '<code>' + escapeHtml(text.slice(i + 1, end)) + '</code>';
        i = end + 1;
        continue;
      }
      lit += ch; i++; continue;
    }

    // Bold.
    if (ch === '*' && text[i + 1] === '*') {
      const end = text.indexOf('**', i + 2);
      if (end > i + 2) {
        flush();
        out += '<strong>' + inlineHtml(text.slice(i + 2, end)) + '</strong>';
        i = end + 2;
        continue;
      }
      lit += '**'; i += 2; continue;
    }

    // Strikethrough.
    if (ch === '~' && text[i + 1] === '~') {
      const end = text.indexOf('~~', i + 2);
      if (end > i + 2) {
        flush();
        out += '<s>' + inlineHtml(text.slice(i + 2, end)) + '</s>';
        i = end + 2;
        continue;
      }
      lit += '~~'; i += 2; continue;
    }

    // Italic — not inside words (a*b*c stays literal): the opener must not
    // follow a word char, the closer must not precede one.
    if (ch === '*') {
      const prev = i > 0 ? text[i - 1] : '';
      if (!WORD_CHAR.test(prev)) {
        let end = -1;
        for (let j = i + 1; j < text.length; j++) {
          if (text[j] !== '*' || text[j + 1] === '*') continue;
          if (j === i + 1) continue; // empty span
          const after = j + 1 < text.length ? text[j + 1] : '';
          if (WORD_CHAR.test(after)) continue;
          end = j;
          break;
        }
        if (end > 0) {
          flush();
          out += '<em>' + inlineHtml(text.slice(i + 1, end)) + '</em>';
          i = end + 1;
          continue;
        }
      }
      lit += ch; i++; continue;
    }

    // Links — [text](url) with a scheme allowlist.
    if (ch === '[') {
      const close = text.indexOf('](', i + 1);
      if (close > i + 1) {
        const end = text.indexOf(')', close + 2);
        if (end > close + 2) {
          const label = text.slice(i + 1, close);
          const url = text.slice(close + 2, end).trim();
          flush();
          if (SAFE_URL.test(url)) {
            out += '<a href="' + url.replace(/"/g, '&quot;') +
              '" target="_blank" rel="noopener noreferrer">' + inlineHtml(label) + '</a>';
          } else {
            out += escapeHtml(label);
          }
          i = end + 1;
          continue;
        }
      }
      lit += ch; i++; continue;
    }

    lit += ch; i++;
  }

  flush();
  return out;
}
