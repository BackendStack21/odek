// Client-side parser for the server-side untrusted-content envelope
// (<untrusted_content_<nonce> source="...">…</untrusted_content_<nonce>>),
// mirroring the grammar produced by wrapUntrusted in cmd/odek/untrusted.go.
//
// The envelope is model-facing trust metadata, not user content: the WebUI
// unwraps it for display (body shown escaped, source as a badge) instead of
// rendering the literal tag text. Sanitization itself stays client-side —
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
