import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  promoteAvailability, promoteConfirmText, promoteErrorOutcome, promoteSuccessText, shortHash,
} from './episodes.js';

test('promote needs the reviewed summary hash', () => {
  assert.deepEqual(promoteAvailability({ session_id: 's1', summary: 'x', summary_sha256: 'ab'.repeat(32) }), { ok: true, reason: '' });
  const missing = promoteAvailability({ session_id: 's1', summary: 'x' });
  assert.equal(missing.ok, false);
  assert.match(missing.reason, /could not be read/);
  assert.equal(promoteAvailability({ summary_sha256: 'ab' }).ok, false, 'no session id');
  assert.equal(promoteAvailability(null).ok, false);
});

test('confirmation names what promotion does and the taint sources', () => {
  const text = promoteConfirmText({
    session_id: '2026-10-10-abc', summary: 'did things', summary_sha256: 'deadbeef'.repeat(8),
    provenance: { sources: ['browser', 'mcp:x:y'] },
  });
  assert.match(text, /trusted/);
  assert.match(text, /future sessions/);
  assert.match(text, /browser, mcp:x:y/);
  assert.match(text, /deadbeefdead/, 'shows the hash prefix the operator reviewed');
  assert.match(text, /10 chars/, 'shows the summary length so a cut-off view is noticed');
  const noSrc = promoteConfirmText({ session_id: 's', summary_sha256: 'ab'.repeat(32) });
  assert.match(noSrc, /unknown/);
});

test('a changed summary reloads the list for re-review', () => {
  const out = promoteErrorOutcome(Object.assign(new Error('episode summary changed since it was reviewed; reload and review it again'), { status: 409 }));
  assert.equal(out.reload, true);
  assert.match(out.message, /changed since you reviewed it/);
});

test('a missing-hash rejection also reloads', () => {
  const out = promoteErrorOutcome(Object.assign(new Error('summary_sha256 required'), { status: 400 }));
  assert.equal(out.reload, true);
  assert.match(out.message, /reload/i);
});

test('any other client error means the row is stale and reloads', () => {
  const notFound = promoteErrorOutcome(Object.assign(new Error('episode not found'), { status: 400 }));
  assert.equal(notFound.reload, true);
  assert.equal(notFound.message, 'promote failed: episode not found — the list was reloaded');
  assert.equal(promoteErrorOutcome(Object.assign(new Error('gone'), { status: 404 })).reload, true);
});

test('server and network failures keep the row and report the server message', () => {
  const out = promoteErrorOutcome(Object.assign(new Error('boom'), { status: 500 }));
  assert.equal(out.reload, false);
  assert.equal(out.message, 'promote failed: boom');
  assert.equal(promoteErrorOutcome(new Error('network down')).reload, false, 'no status: network failure');
  assert.equal(promoteErrorOutcome(undefined).reload, false);
});

test('success text reports what was promoted', () => {
  assert.equal(promoteSuccessText({ summary: 'abc', sources: ['browser'] }), 'episode promoted (3 chars, sources: browser)');
  assert.equal(promoteSuccessText({ summary: '', sources: [] }), 'episode promoted');
  assert.equal(promoteSuccessText(null), 'episode promoted', 'tolerates an empty body');
});

test('shortHash is a stable 12-char prefix', () => {
  assert.equal(shortHash('0123456789abcdef'), '0123456789ab');
  assert.equal(shortHash(''), '');
  assert.equal(shortHash(undefined), '');
});
