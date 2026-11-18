// Client-side parser for the server-side untrusted-content envelope
// (<untrusted_content_<nonce> source="...">…</untrusted_content_<nonce>>),
// mirroring the grammar produced by wrapUntrusted in cmd/odek/untrusted.go.
//
// The envelope is model-facing trust metadata, not user content: the WebUI
// unwraps it for display (body shown escaped; source is discarded) instead
// of rendering the literal tag text. Sanitization itself stays client-side —
// the server always sends raw content.
//
// Server-side neutraliseWrapperLiterals guarantees bodies contain no literal
// "untrusted_content" substring, so a real closer can never appear early
// inside a body.
//
// Envelopes are read by a linear scanner rather than a backtracking regex:
// a lazy body pattern rescans to the end for every unclosed opener, which is
// quadratic on hostile floods. Closers are indexed once per nonce, and the
// pointers that find each opener's '">' terminator only move forward.
// Nonce-matched: the closer must repeat the opener's nonce, so a forged or
// mismatched envelope stays plain text.
const MAX_NONCE = 64;

function readNonce(text, at) {
  let e = at;
  while (e < text.length && e - at < MAX_NONCE && /[0-9a-f]/.test(text[e])) e++;
  return e > at ? text.slice(at, e) : '';
}

function scanEnvelopes(text, tag) {
  const open = '<' + tag + '_';
  const close = '</' + tag + '_';
  if (!text.includes(close)) return [{ source: null, body: text }];

  const closers = new Map(); // nonce -> ascending closer offsets
  for (let c = text.indexOf(close); c >= 0; c = text.indexOf(close, c + 1)) {
    const nonce = readNonce(text, c + close.length);
    if (nonce && text[c + close.length + nonce.length] === '>') {
      if (!closers.has(nonce)) closers.set(nonce, []);
      closers.get(nonce).push(c);
    }
  }

  const segments = [];
  const cursor = new Map(); // nonce -> next unused closer index
  let plainFrom = 0; // start of pending plain text
  let from = 0;      // where to look for the next opener
  // Forward-only pointers, each recomputed only once it falls behind the
  // current source start, so every one scans the text at most once overall.
  // A source runs to the first '">' and may contain no '"', '<' or newline
  // (the server maps all three away).
  const ahead = { term: -1, quote: -1, lt: -1, nl: -1 };
  const next = (key, needle, at) => {
    if (ahead[key] < at) {
      const k = text.indexOf(needle, at);
      ahead[key] = k < 0 ? text.length : k;
    }
    return ahead[key];
  };
  for (;;) {
    const j = text.indexOf(open, from);
    if (j < 0) break;
    from = j + open.length;
    const nonce = readNonce(text, from);
    const attr = from + nonce.length;
    const list = nonce && closers.get(nonce);
    if (!list || !text.startsWith(' source="', attr)) continue;
    const srcStart = attr + 9;
    const term = next('term', '">', srcStart);
    if (term >= text.length) break; // no terminator ahead: no envelope can follow
    if (next('quote', '"', srcStart) !== term || next('lt', '<', srcStart) < term || next('nl', '\n', srcStart) < term) continue;
    let bodyStart = term + 2;
    if (text[bodyStart] === '\n') bodyStart++;
    let c = cursor.get(nonce) || 0;
    while (c < list.length && list[c] < bodyStart) c++;
    cursor.set(nonce, c);
    if (c >= list.length) continue;
    const closeAt = list[c];
    cursor.set(nonce, c + 1);
    let body = text.slice(bodyStart, closeAt);
    if (body.endsWith('\n')) body = body.slice(0, -1);
    if (j > plainFrom) segments.push({ source: null, body: text.slice(plainFrom, j) });
    segments.push({ source: text.slice(srcStart, term), body });
    plainFrom = from = closeAt + close.length + nonce.length + 1;
  }
  if (plainFrom < text.length) segments.push({ source: null, body: text.slice(plainFrom) });
  if (segments.length === 0) segments.push({ source: null, body: text });
  return segments;
}

// parseUntrusted splits text into segments: wrapped envelopes become
// { source, body } (body trimmed of the envelope's framing newlines) and any
// surrounding plain text becomes { source: null, body }. Empty plain-text
// gaps between envelopes are omitted.
export function parseUntrusted(text) {
  if (!text) return [];
  return scanEnvelopes(String(text), 'untrusted_content');
}

// JSON string fields can hold an envelope in two ways that look alike once
// parsed. json.Marshal escapes '<' as \u003c, so no neutraliser sees it and
// the engine envelope only renames "untrusted_content" to "untrusted·content":
//   - the tool wrapped that field on its own before marshalling (a read_file
//     body, a search match path). The field's text was neutralised when it
//     was wrapped, so an envelope forged inside it arrives as
//     '‹untrusted·content_…' (U+2039) and stays visible;
//   - the text itself carried an envelope: a tool that marshals first and
//     wraps its whole output (web_search, MCP tools, session_search), a field
//     the tool never wraps (a write_file path, an error quoting a URL), tool
//     arguments. That text was never neutralised, so a forged envelope
//     arrives as '<untrusted·content_…', exactly like a real one.
// The bytes cannot tell them apart, so only the fields the server wraps one
// by one are unwrapped. WRAPPED_FIELDS mirrors the per-field wrapUntrusted /
// wrapUntrustedBatch call sites in cmd/odek; a field missing from it shows
// its framing, which is the safe direction. Paths are JSON keys from the
// result root joined by '.'; '*' matches one key or array index and '**'
// any number of levels.
const WRAPPED_FIELDS = {
  read_file: ['content'],
  patch: ['diff'],
  file_info: ['path'],
  search_files: ['matches.*.path', 'matches.*.content', 'skipped.*'],
  glob: ['matches.*.path'],
  tree: ['tree.**.path'],
  head_tail: ['results.*.lines.*'],
  diff: ['hunks.*.lines.*.content'],
  json_query: ['value.**'],
  base64: ['encoded', 'decoded'],
  browser: ['title', 'url', 'content', 'error', 'elements.*.text', 'elements.*.url'],
  http_request: ['error'],
  session_search: ['task', 'sessions.*.task', 'buffer.*', 'session_messages.*.content'],
  transcribe: ['text', 'segments.*.text'],
  vision: ['description'],
};
const WRAPPED_PATHS = new Map(Object.entries(WRAPPED_FIELDS).map(([tool, paths]) => [tool, paths.map((p) => p.split('.'))]));

function matchPath(pattern, path, i = 0, j = 0) {
  for (; i < pattern.length; i++, j++) {
    if (pattern[i] === '**') {
      for (let k = j; k <= path.length; k++) if (matchPath(pattern, path, i + 1, k)) return true;
      return false;
    }
    if (j >= path.length || (pattern[i] !== '*' && pattern[i] !== String(path[j]))) return false;
  }
  return j === path.length;
}

// unwrapField returns the body of a field value that is exactly one
// envelope, the shape a per-field wrap produces; any other value, including
// text around an envelope, is returned as received. Display text is never
// unwrapped again. The '_' form is a field from a result the engine did not
// wrap again.
export function unwrapField(text) {
  const value = String(text == null ? '' : text);
  for (const tag of ['untrusted·content', 'untrusted_content']) {
    const segments = scanEnvelopes(value, tag);
    if (segments.length === 1 && segments[0].source !== null) return segments[0].body;
  }
  return value;
}

// A tool that wraps its own output (shell, bg_output, delegate_tasks, MCP
// tools) is wrapped again by the engine, so the engine envelope's body is the
// tool's envelope in neutralised form, '<' rewritten to '‹':
//   ‹untrusted·content_N source="…">\n…\n‹/untrusted·content_N>
// possibly after the guard's notice or an "error: " prefix. unwrapToolOutput
// removes exactly that shape from one real envelope's body: the tool's
// envelope must close at the very end of the body and open at the start of a
// line (or after "error: "), with matching nonces. Anything else, including
// an envelope forged inside the output, stays visible. Linear: a fixed number
// of searches.
const TOOL_OPEN = '‹untrusted·content_';
const TOOL_CLOSE = '‹/untrusted·content_';
export function unwrapToolOutput(body) {
  const text = String(body || '');
  const end = text.trimEnd();
  const c = end.lastIndexOf(TOOL_CLOSE);
  if (c < 0) return text;
  const nonce = readNonce(end, c + TOOL_CLOSE.length);
  if (!nonce || c + TOOL_CLOSE.length + nonce.length + 1 !== end.length || !end.endsWith('>')) return text;
  const opener = TOOL_OPEN + nonce + ' source="';
  const o = end.indexOf(opener);
  if (o < 0 || o >= c) return text;
  const prefix = end.slice(0, o);
  if (prefix && !prefix.endsWith('\n') && prefix !== 'error: ') return text;
  const srcStart = o + opener.length;
  const term = end.indexOf('">', srcStart);
  if (term < 0 || term > c || /["<\n]/.test(end.slice(srcStart, term))) return text;
  let bodyStart = term + 2;
  if (end[bodyStart] === '\n') bodyStart++;
  let inner = end.slice(bodyStart, c);
  if (inner.endsWith('\n')) inner = inner.slice(0, -1);
  return prefix + inner;
}

// displayJSON returns the parsed result of tool name with each field the
// tool wraps one by one unwrapped (unwrapField); every other value is kept
// as received. Nesting deeper than the bound is left as received.
export function displayJSON(value, name = '') {
  const patterns = WRAPPED_PATHS.get(name);
  if (!patterns) return value;
  const walk = (item, path) => {
    if (typeof item === 'string') return patterns.some((p) => matchPath(p, path)) ? unwrapField(item) : item;
    if (!item || typeof item !== 'object' || path.length >= 64) return item;
    if (Array.isArray(item)) return item.map((child, i) => walk(child, [...path, i]));
    // fromEntries defines own properties, so a "__proto__" key stays data.
    return Object.fromEntries(Object.entries(item).map(([key, child]) => [key, walk(child, [...path, key])]));
  };
  return walk(value, []);
}

// displayLabel renders an untrusted single-line label (a file name or path)
// faithfully: line breaks, control characters and bidi/invisible format
// characters become visible escapes, so a name can neither add lines to an
// outline nor visually reorder or hide text.
export function displayLabel(text) {
  return String(text == null ? '' : text).replace(
    /[\p{Cc}\p{Cf}\u2028\u2029]/gu, // controls, every format character (as the server's SanitizeForDisplay), line/paragraph separators
    (ch) => (ch === '\n' ? '\\n' : ch === '\r' ? '\\r' : ch === '\t' ? '\\t' : '\\u{' + ch.codePointAt(0).toString(16).toUpperCase().padStart(4, '0') + '}'),
  );
}

// unwrapUntrusted returns the concatenated bodies of every envelope in text
// (plus any non-wrapped text), with all envelope tags removed.
export function unwrapUntrusted(text) {
  return parseUntrusted(text).map((s) => s.body).join('');
}

// hasUntrustedWrapper reports whether text contains at least one complete
// nonce-matched envelope.
export function hasUntrustedWrapper(text) {
  if (!text) return false;
  return parseUntrusted(text).some((seg) => seg.source !== null);
}

// stripAttachmentBodies collapses attachment envelopes in reloaded user
// messages to chip-style placeholders so session history doesn't dump file
// bodies. The server stores each attachment as an envelope with
// source="attachment:<name>" (serve.go); this matches the 📎 chip rendering
// used at send time (input.js). All other segments pass through unwrapped
// (envelope tags removed, bodies kept).
export function stripAttachmentBodies(content) {
  if (!content) return '';
  return parseUntrusted(content).map((seg) => {
    if (seg.source && seg.source.startsWith('attachment:')) {
      return '📎 ' + displayLabel(seg.source.slice('attachment:'.length)) + '\n';
    }
    if (seg.source && seg.source.startsWith('resource:')) {
      // @-reference bodies are untrusted file/session text the server
      // expanded into the prompt; show the reference, never the body.
      return displayLabel(seg.source.slice('resource:'.length));
    }
    return seg.body;
  }).join('');
}

// historyUserText is what a reloaded user turn shows. The server persists
// the resource-expanded prompt as content and what the operator typed as
// principal_prompt; the typed text is shown (never expanded @-reference
// bodies) with attachment chips derived from the envelopes in content.
// Legacy rows without principal_prompt fall back to the collapsed content.
export function historyUserText(content, principal) {
  if (typeof principal !== 'string') return stripAttachmentBodies(content || '');
  const chips = content ? parseUntrusted(content)
    .filter((seg) => seg.source && seg.source.startsWith('attachment:'))
    .map((seg) => '📎 ' + displayLabel(seg.source.slice('attachment:'.length)) + '\n')
    .join('') : '';
  return chips + principal;
}
