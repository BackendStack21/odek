// Decision logic for promoting a pending memory episode from the memory
// panel. Promotion turns an episode that came from a tainted session into
// trusted context that later sessions recall, so the operator must see what
// they promote and the server pins the promotion to the summary they saw
// (summary_sha256 from GET /api/memory; 409 when the stored text changed).
// Kept free of DOM access so it is unit-testable.

// shortHash returns the 12-character prefix of a summary hash, the part shown
// to the operator so the confirmation names the exact text being promoted.
export function shortHash(sha) {
  return typeof sha === 'string' ? sha.slice(0, 12) : '';
}

// promoteAvailability reports whether a pending entry can be promoted. The
// server omits summary_sha256 when the episode file could not be read; with
// nothing reviewable there is nothing to pin, so promotion is refused.
export function promoteAvailability(ep) {
  if (!ep || !ep.session_id) {
    return { ok: false, reason: 'missing session id' };
  }
  if (!ep.summary_sha256) {
    return {
      ok: false,
      reason: 'the stored episode text could not be read, so it cannot be reviewed or promoted here',
    };
  }
  return { ok: true, reason: '' };
}

function sourcesOf(list) {
  return Array.isArray(list) && list.length ? list.join(', ') : '';
}

// promoteConfirmText is the confirmation shown before a promotion.
export function promoteConfirmText(ep) {
  const sources = sourcesOf(ep && ep.provenance && ep.provenance.sources) || 'unknown';
  const length = typeof (ep && ep.summary) === 'string' ? ep.summary.length : 0;
  return 'Promote this episode to trusted memory?\n\n' +
    'Its summary will be recalled into future sessions as trusted context. ' +
    'It was held for review because the session ingested untrusted content ' +
    '(sources: ' + sources + ').\n\n' +
    'Only promote it if you have read the whole summary shown in the panel ' +
    '(' + length + ' chars, sha256 ' + shortHash(ep && ep.summary_sha256) + '…).';
}

// promoteErrorOutcome maps a failed promotion to a message and whether the
// pending list must be reloaded. 409 means the stored text changed after it
// was shown; a 400 about the hash means the panel is stale; any other 4xx
// (episode already promoted or discarded elsewhere) leaves a dead row. All
// of them reload the list so the operator reviews current state. Server and
// network failures keep the row so the operator can retry.
export function promoteErrorOutcome(err) {
  const status = err && err.status;
  const msg = (err && err.message) || 'unknown error';
  if (status === 409) {
    return { reload: true, message: 'episode summary changed since you reviewed it — the list was reloaded, review it again' };
  }
  if (status === 400 && /summary_sha256/.test(msg)) {
    return { reload: true, message: 'the panel was out of date — reload done, review the episode again before promoting' };
  }
  if (typeof status === 'number' && status >= 400 && status < 500) {
    return { reload: true, message: 'promote failed: ' + msg + ' — the list was reloaded' };
  }
  return { reload: false, message: 'promote failed: ' + msg };
}

// promoteSuccessText summarises the server's promote response
// ({session_id, summary, sources}).
export function promoteSuccessText(result) {
  if (!result || typeof result !== 'object') return 'episode promoted';
  const parts = [];
  if (typeof result.summary === 'string' && result.summary.length) {
    parts.push(result.summary.length + ' chars');
  }
  const sources = sourcesOf(result.sources);
  if (sources) parts.push('sources: ' + sources);
  return parts.length ? 'episode promoted (' + parts.join(', ') + ')' : 'episode promoted';
}
