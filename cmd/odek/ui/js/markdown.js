// Markdown → HTML via a small hand-written tokenizer (zero deps).
//
// CommonMark-ish + the GFM bits models actually emit (tables, quotes,
// task lists, underscore emphasis, autolinks). No marked / highlight.js /
// CDN — syntax stays monochrome. Streaming-safe: an unterminated fenced
// code block renders its collected lines as a code block anyway (EOF acts
// as the fence close), while unterminated INLINE constructs (`**bold`,
// `` `code ``, `~~strike~~`) render as literal text — never as broken
// markup. All input is HTML-escaped by default; content inside code spans
// and fences is escaped but never re-processed. The copy button carries
// no inline handler — clicks are delegated on #messages in render.js.
// Images never become <img> (CSP + tracking): a safe URL is a caption link.
import { escapeHtml, escapeAttr } from './escape.js';

// Link allowlist: http(s), mailto, #, ./, ../, and same-origin paths.
// Protocol-relative (//evil), javascript:/data:/blob:/vbscript:,
// backslashes, /api/*, and ?token= are rejected — render as plain text.
function hrefLooksInternal(u) {
  if (/[?&]token=/i.test(u)) return true;
  if (/^(?:https?:|mailto:)/i.test(u)) return false;
  const path = String(u || '').split(/[?#]/)[0];
  return /(^|\/)api(?:\/|$)/i.test(path);
}

export function isSafeHref(url) {
  const u = String(url || '').trim();
  if (!u || /[\s\\\x00-\x1F]/.test(u)) return false;
  const lower = u.toLowerCase();
  if (lower.startsWith('javascript:') || lower.startsWith('data:') ||
      lower.startsWith('vbscript:') || lower.startsWith('blob:')) return false;
  if (u.startsWith('//')) return false;
  if (/^[a-z][a-z0-9+.-]*:/i.test(u) && !/^(https?:|mailto:)/i.test(u)) return false;
  if (hrefLooksInternal(u)) return false;
  if (/%[0-9a-f]{2}/i.test(u)) {
    try {
      if (hrefLooksInternal(decodeURIComponent(u))) return false;
    } catch {
      return false;
    }
  }
  return /^(https?:|mailto:|#|\.\/|\.\.\/|\/)/i.test(u);
}

const WORD_CHAR = /[A-Za-z0-9]/;
const TASK_ITEM = /^\[([ xX])\]\s+(.+)$/;
const ANGLE_AUTO = /^<((?:https?:|mailto:)[^>\s]+)>/i;
const BARE_URL = /^(https?:\/\/[^\s<>]+)/i;
const TRAIL_PUNCT = /[),.;:!?]+$/;

export function markdownToHtml(text) {
  if (!text) return '';
  return parseBlocks(text.split('\n'));
}

function parseBlocks(lines) {
  const out = [];
  let i = 0;

  const fenceOpen = (l) => /^```(\w*)\s*$/.exec(l);
  const isFenceClose = (l) => /^```\s*$/.test(l);
  const headerMatch = (l) => /^(#{1,4})\s+(.+)$/.exec(l);
  const isHr = (l) => /^(---|\*\*\*|___)$/.test(l.trim());
  const ulItem = (l) => /^\s*[-*+]\s+(.+)$/.exec(l);
  const olItem = (l) => /^\s*\d+\.\s+(.+)$/.exec(l);
  const isQuote = (l) => /^>\s?/.test(l);
  const isTableAt = (idx) =>
    idx + 1 < lines.length && isTableRow(lines[idx]) && isTableSep(lines[idx + 1]);
  const isBlockStart = (l, idx) =>
    !!fenceOpen(l) || !!headerMatch(l) || isHr(l) || !!ulItem(l) || !!olItem(l) ||
    isQuote(l) || isTableAt(idx);

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

    if (isQuote(line)) {
      const inner = [];
      while (i < lines.length) {
        if (isQuote(lines[i])) {
          inner.push(lines[i].replace(/^>\s?/, ''));
          i++;
          continue;
        }
        // A blank line between quoted lines stays inside the quote.
        if (lines[i].trim() === '' && i + 1 < lines.length && isQuote(lines[i + 1])) {
          inner.push('');
          i++;
          continue;
        }
        break;
      }
      out.push('<blockquote>' + parseBlocks(inner) + '</blockquote>');
      continue;
    }

    if (isTableAt(i)) {
      const rendered = renderTable(lines, i);
      out.push(rendered.html);
      i = rendered.next;
      continue;
    }

    if (ulItem(line)) {
      const items = [];
      let tasks = 0;
      while (i < lines.length) {
        const m = ulItem(lines[i]);
        if (!m) break;
        const task = TASK_ITEM.exec(m[1]);
        if (task) {
          tasks++;
          const on = task[1] !== ' ';
          items.push(
            '<li class="task">' +
            '<span class="task-mark' + (on ? ' on' : '') + '" aria-hidden="true">' +
            (on ? '☑' : '☐') + '</span>' + inlineHtml(task[2]) + '</li>'
          );
        } else {
          items.push('<li>' + inlineHtml(m[1]) + '</li>');
        }
        i++;
      }
      out.push('<' + (tasks ? 'ul class="task-list"' : 'ul') + '>' + items.join('') + '</ul>');
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
    while (i < lines.length && lines[i].trim() !== '' && !isBlockStart(lines[i], i)) {
      para.push(lines[i]);
      i++;
    }
    out.push('<p>' + para.map(inlineHtml).join('<br>') + '</p>');
  }

  return out.join('\n');
}

function splitRow(line) {
  let s = String(line || '').trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|')) s = s.slice(0, -1);
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '\\' && s[i + 1] === '|') { cur += '|'; i++; continue; }
    if (s[i] === '|') { cells.push(cur.trim()); cur = ''; continue; }
    cur += s[i];
  }
  cells.push(cur.trim());
  return cells;
}

function isTableRow(line) {
  const s = String(line || '').trim();
  if (!s.includes('|')) return false;
  if (/^[-*+]\s/.test(s) || /^\d+\.\s/.test(s) || /^>\s?/.test(s)) return false;
  return splitRow(s).length >= 2;
}

function isTableSep(line) {
  const cells = splitRow(line);
  if (cells.length < 2) return false;
  return cells.every((c) => /^:?-+:?$/.test(c.replace(/\s+/g, '')));
}

function cellAlign(spec) {
  const s = spec.replace(/\s+/g, '');
  const left = s.startsWith(':');
  const right = s.endsWith(':');
  if (left && right) return 'ta-c';
  if (right) return 'ta-r';
  if (left) return 'ta-l';
  return '';
}

function renderTable(lines, start) {
  const head = splitRow(lines[start]);
  const aligns = splitRow(lines[start + 1]).map(cellAlign);
  let i = start + 2;
  const body = [];
  while (i < lines.length && isTableRow(lines[i]) && !isTableSep(lines[i])) {
    body.push(splitRow(lines[i]));
    i++;
  }
  const cell = (tag, text, col) => {
    const a = aligns[col];
    const attr = a === 'ta-c' ? ' class="ta-c"' : a === 'ta-r' ? ' class="ta-r"' : a === 'ta-l' ? ' class="ta-l"' : '';
    return '<' + tag + attr + '>' + inlineHtml(text) + '</' + tag + '>';
  };
  let html = '<div class="md-table-wrap"><table><thead><tr>';
  head.forEach((c, col) => { html += cell('th', c, col); });
  html += '</tr></thead><tbody>';
  body.forEach((row) => {
    html += '<tr>';
    for (let col = 0; col < head.length; col++) html += cell('td', row[col] || '', col);
    html += '</tr>';
  });
  html += '</tbody></table></div>';
  return { html, next: i };
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

function anchorHtml(url, inner, extraClass) {
  return '<a href="' + escapeAttr(url) +
    '" target="_blank" rel="noopener noreferrer"' +
    (extraClass ? ' class="' + extraClass + '"' : '') + '>' + inner + '</a>';
}

function takeEmphasis(text, i, delim, tag) {
  const n = delim.length;
  const prev = i > 0 ? text[i - 1] : '';
  if (n === 1 && WORD_CHAR.test(prev)) return null;
  if (text.slice(i, i + n) !== delim) return null;
  let end = -1;
  for (let j = i + n; j < text.length; j++) {
    if (text.slice(j, j + n) !== delim) continue;
    if (j === i + n) continue; // empty span
    if (n === 1 && text[j + 1] === delim) continue; // leave ** / __ to the pair arm
    if (n === 1) {
      const after = j + n < text.length ? text[j + n] : '';
      if (WORD_CHAR.test(after)) continue;
    }
    end = j;
    break;
  }
  if (end < 0) return null;
  return {
    html: '<' + tag + '>' + inlineHtml(text.slice(i + n, end)) + '</' + tag + '>',
    next: end + n,
  };
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

    if (ch === '*' && text[i + 1] === '*') {
      const hit = takeEmphasis(text, i, '**', 'strong');
      if (hit) { flush(); out += hit.html; i = hit.next; continue; }
      lit += '**'; i += 2; continue;
    }

    if (ch === '_' && text[i + 1] === '_') {
      const hit = takeEmphasis(text, i, '__', 'strong');
      if (hit) { flush(); out += hit.html; i = hit.next; continue; }
      lit += '__'; i += 2; continue;
    }

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

    if (ch === '*') {
      const hit = takeEmphasis(text, i, '*', 'em');
      if (hit) { flush(); out += hit.html; i = hit.next; continue; }
      lit += ch; i++; continue;
    }

    if (ch === '_') {
      const hit = takeEmphasis(text, i, '_', 'em');
      if (hit) { flush(); out += hit.html; i = hit.next; continue; }
      lit += ch; i++; continue;
    }

    // Images — caption link only, never <img> (CSP + no remote fetch).
    if (ch === '!' && text[i + 1] === '[') {
      const close = text.indexOf('](', i + 2);
      if (close > i + 1) {
        const end = text.indexOf(')', close + 2);
        if (end > close + 2) {
          const label = text.slice(i + 2, close);
          const url = text.slice(close + 2, end).trim();
          flush();
          const inner = inlineHtml(label) || escapeHtml(url);
          out += isSafeHref(url) ? anchorHtml(url, inner, 'md-img') : inner;
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
          out += isSafeHref(url) ? anchorHtml(url, inlineHtml(label)) : escapeHtml(label);
          i = end + 1;
          continue;
        }
      }
      lit += ch; i++; continue;
    }

    if (ch === '<') {
      const m = ANGLE_AUTO.exec(text.slice(i));
      if (m && isSafeHref(m[1])) {
        flush();
        out += anchorHtml(m[1], escapeHtml(m[1]));
        i += m[0].length;
        continue;
      }
    }

    if ((ch === 'h' || ch === 'H') && BARE_URL.test(text.slice(i))) {
      const m = BARE_URL.exec(text.slice(i));
      let url = m[1];
      const trail = (url.match(TRAIL_PUNCT) || [''])[0];
      if (trail) url = url.slice(0, -trail.length);
      if (isSafeHref(url)) {
        flush();
        out += anchorHtml(url, escapeHtml(url));
        i += url.length;
        continue;
      }
    }

    lit += ch; i++;
  }

  flush();
  return out;
}
