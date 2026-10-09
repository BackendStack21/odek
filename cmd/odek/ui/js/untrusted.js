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
// "untrusted_content" substring, so non-greedy matching cannot terminate
// early inside a body.

// Nonce-backreferenced: the closing tag must repeat the opening nonce, so a
// forged/mismatched envelope is treated as plain text rather than parsed.
const RE_UNTRUSTED =
  /<untrusted_content_([0-9a-f]+) source="([^"]*)">\n?([\s\S]*?)\n?<\/untrusted_content_\1>/g;

// parseUntrusted splits text into segments: wrapped envelopes become
// { source, body } (body trimmed of the envelope's framing newlines) and any
// surrounding plain text becomes { source: null, body }. Empty plain-text
// gaps between envelopes are omitted.
export function parseUntrusted(text) {
  if (!text) return [];
  const segments = [];
  let last = 0;
  RE_UNTRUSTED.lastIndex = 0;
  let m;
  while ((m = RE_UNTRUSTED.exec(text)) !== null) {
    if (m.index > last) {
      segments.push({ source: null, body: text.slice(last, m.index) });
    }
    segments.push({ source: m[2], body: m[3] });
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    segments.push({ source: null, body: text.slice(last) });
  }
  if (segments.length === 0) {
    segments.push({ source: null, body: text });
  }
  return segments;
}

// Envelopes nested inside an outer envelope reach the client neutralised:
// the server rewrites their "untrusted_content" to "untrusted·content" so an
// inner tag can never close the outer one. Tool fields (a search match path,
// a read_file body) carry such inner envelopes. For display only, the
// neutralised framing is removed too — the body is still shown as escaped
// untrusted text, so stripping the tags grants it nothing.
//
// Hostile bodies can carry the neutralised form themselves, so this is a
// linear scan, not a backtracking regex: closers are indexed once, and each
// opener takes the first matching closer after it or stays literal. Nonces
// and source attributes are length-bounded so no step rescans the input.
const NEUTRAL_OPEN = '<untrusted·content_';
const RE_NEUTRAL_OPENER = /<untrusted·content_([0-9a-f]{1,64}) source="[^"\n]{0,512}">\n?/y;
const RE_NEUTRAL_CLOSER = /<\/untrusted·content_([0-9a-f]{1,64})>/g;

function stripNeutralised(text) {
  if (!text.includes('</untrusted·content_')) return text;
  const closers = new Map(); // nonce -> ascending closer offsets
  RE_NEUTRAL_CLOSER.lastIndex = 0;
  for (let m; (m = RE_NEUTRAL_CLOSER.exec(text)) !== null;) {
    if (!closers.has(m[1])) closers.set(m[1], []);
    closers.get(m[1]).push(m.index);
  }
  const cursor = new Map(); // nonce -> next unused index into closers
  let out = '';
  let i = 0;
  for (;;) {
    const j = text.indexOf(NEUTRAL_OPEN, i);
    if (j < 0) { out += text.slice(i); break; }
    RE_NEUTRAL_OPENER.lastIndex = j;
    const m = RE_NEUTRAL_OPENER.exec(text);
    const list = m && closers.get(m[1]);
    let k = -1;
    if (list) {
      let c = cursor.get(m[1]) || 0;
      while (c < list.length && list[c] < RE_NEUTRAL_OPENER.lastIndex) c++;
      cursor.set(m[1], c);
      if (c < list.length) k = list[c];
    }
    if (k < 0) { // no envelope here: keep the opener text literally
      out += text.slice(i, j + NEUTRAL_OPEN.length);
      i = j + NEUTRAL_OPEN.length;
      continue;
    }
    out += text.slice(i, j) + text.slice(RE_NEUTRAL_OPENER.lastIndex, k).replace(/\n$/, '');
    i = k + ('</untrusted·content_' + m[1] + '>').length;
    cursor.set(m[1], (cursor.get(m[1]) || 0) + 1);
  }
  return out;
}

export function unwrapForDisplay(text) {
  if (!text) return '';
  return stripNeutralised(unwrapUntrusted(text));
}

// displayLabel renders an untrusted single-line label (a file name or path)
// faithfully: line breaks, control characters and bidi/invisible format
// characters become visible escapes, so a name can neither add lines to an
// outline nor visually reorder or hide text.
export function displayLabel(text) {
  return String(text == null ? '' : text).replace(
    /[\u0000-\u001f\u007f-\u009f\u061c\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff]/g,
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
  RE_UNTRUSTED.lastIndex = 0;
  return RE_UNTRUSTED.test(text);
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
