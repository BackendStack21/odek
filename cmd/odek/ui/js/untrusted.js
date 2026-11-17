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

// glyphs lists the characters an envelope tag may open with. A tag opened
// with one glyph closes only with the same glyph and nonce. All glyphs are
// read in one pass, so a matched envelope's body is never scanned again: a
// forged envelope inside a real one stays visible text.
function scanEnvelopes(text, tag, glyphs = ['<']) {
  const forms = glyphs.map((g) => ({ g, open: g + tag + '_', close: g + '/' + tag + '_', at: -1 }))
    .filter((f) => text.includes(f.close));
  if (!forms.length) return [{ source: null, body: text }];

  const closers = new Map(); // glyph + nonce -> ascending closer offsets
  for (const { g, close } of forms) {
    for (let c = text.indexOf(close); c >= 0; c = text.indexOf(close, c + 1)) {
      const nonce = readNonce(text, c + close.length);
      if (nonce && text[c + close.length + nonce.length] === '>') {
        if (!closers.has(g + nonce)) closers.set(g + nonce, []);
        closers.get(g + nonce).push(c);
      }
    }
  }
  // nextOpener returns the earliest opener at or after `at` among the forms.
  // Each form's position only moves forward, so every form scans the text
  // at most once overall.
  const nextOpener = (at) => {
    let best = null;
    for (const f of forms) {
      if (f.at < at) {
        const k = text.indexOf(f.open, at);
        f.at = k < 0 ? text.length : k;
      }
      if (f.at < text.length && (!best || f.at < best.at)) best = f;
    }
    return best;
  };

  const segments = [];
  const cursor = new Map(); // glyph + nonce -> next unused closer index
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
    const form = nextOpener(from);
    if (!form) break;
    const j = form.at;
    from = j + form.open.length;
    const nonce = readNonce(text, from);
    const attr = from + nonce.length;
    const key = form.g + nonce;
    const list = nonce && closers.get(key);
    if (!list || !text.startsWith(' source="', attr)) continue;
    const srcStart = attr + 9;
    const term = next('term', '">', srcStart);
    if (term >= text.length) break; // no terminator ahead: no envelope can follow
    if (next('quote', '"', srcStart) !== term || next('lt', '<', srcStart) < term || next('nl', '\n', srcStart) < term) continue;
    let bodyStart = term + 2;
    if (text[bodyStart] === '\n') bodyStart++;
    let c = cursor.get(key) || 0;
    while (c < list.length && list[c] < bodyStart) c++;
    cursor.set(key, c);
    if (c >= list.length) continue;
    const closeAt = list[c];
    cursor.set(key, c + 1);
    let body = text.slice(bodyStart, closeAt);
    if (body.endsWith('\n')) body = body.slice(0, -1);
    if (j > plainFrom) segments.push({ source: null, body: text.slice(plainFrom, j) });
    segments.push({ source: text.slice(srcStart, term), body });
    plainFrom = from = closeAt + form.close.length + nonce.length + 1;
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
// inner tag can never close the outer one, and the '<' that opens a tag
// becomes '‹' (U+2039). A tool's own envelope around plain-text output
// (shell, bg_output, delegate_tasks) arrives as ‹untrusted·content_N …›;
// one inside a JSON string field (a search match path, a read_file body)
// keeps its '<', which json.Marshal escaped before the neutraliser ran. For
// display only, both framings are removed — the body is still shown as
// escaped untrusted text, so stripping the tags grants it nothing. Hostile
// bodies can carry these forms themselves, which is why they share the
// linear, nonce-matched scanner, and why both glyphs are read in one pass:
// a forged envelope inside a real envelope's body is never unwrapped.
export function unwrapForDisplay(text) {
  if (!text) return '';
  return scanEnvelopes(unwrapUntrusted(text), 'untrusted·content', ['<', '‹']).map((seg) => seg.body).join('');
}

// displayJSON returns value with every string passed through
// unwrapForDisplay, so a JSON result can be shown without the envelopes its
// string fields carry. Nesting deeper than the bound is left as received.
export function displayJSON(value, depth = 0) {
  if (typeof value === 'string') return unwrapForDisplay(value);
  if (!value || typeof value !== 'object' || depth >= 64) return value;
  if (Array.isArray(value)) return value.map((item) => displayJSON(item, depth + 1));
  // fromEntries defines own properties, so a "__proto__" key stays data.
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, displayJSON(item, depth + 1)]));
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
