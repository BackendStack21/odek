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

// Envelopes nested inside an outer envelope reach the client neutralised:
// the server rewrites their "untrusted_content" to "untrusted·content" so an
// inner tag can never close the outer one. Tool fields (a search match path,
// a read_file body) carry such inner envelopes. For display only, the
// neutralised framing is removed too — the body is still shown as escaped
// untrusted text, so stripping the tags grants it nothing. Hostile bodies can
// carry this form themselves, which is why it shares the linear scanner.
export function unwrapForDisplay(text) {
  if (!text) return '';
  return scanEnvelopes(unwrapUntrusted(text), 'untrusted·content').map((seg) => seg.body).join('');
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
      return '📎 ' + seg.source.slice('attachment:'.length) + '\n';
    }
    return seg.body;
  }).join('');
}
