// E2E tests for the client API layer (api.js): every endpoint function is
// exercised against a mocked fetch + document, asserting the exact request
// the browser would send — method, path+query, token headers, session-token
// passthrough, JSON bodies, and error normalization. Run:
//   node --test cmd/odek/ui/js/
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

// ── Minimal browser shims (api.js → net.js touches document + fetch) ──
const captured = [];
let nextResponse = { status: 200, body: '{}', headers: { 'content-type': 'application/json' } };

globalThis.document = {
  querySelector: (sel) => {
    if (sel === 'meta[name="odek-ws-token"]') {
      return { getAttribute: () => 'test-instance-token' };
    }
    return null;
  },
};
globalThis.fetch = async (url, init = {}) => {
  captured.push({ url: String(url), init });
  const r = nextResponse;
  return {
    ok: r.status >= 200 && r.status < 300,
    status: r.status,
    headers: { get: (h) => r.headers[h.toLowerCase()] || null },
    json: async () => JSON.parse(r.body),
    text: async () => r.body,
  };
};

const api = await import('./api.js');

function last() { return captured[captured.length - 1]; }
function reset(next = { status: 200, body: '{}', headers: { 'content-type': 'application/json' } }) {
  captured.length = 0;
  nextResponse = next;
}

beforeEach(() => reset());

// ── Header contract ──

test('every request carries the instance token header', async () => {
  await api.getHealth();
  const h = last().init.headers;
  assert.equal(h['X-Odek-Ws-Token'], 'test-instance-token');
  assert.equal(h['X-Odek-Ws-Token'], 'test-instance-token');
});

test('session token is passed as X-Session-Token', async () => {
  await api.getSession('sess-1', 'tok-1');
  assert.equal(last().init.headers['X-Session-Token'], 'tok-1');
  assert.equal(last().url, '/api/sessions/sess-1');
});

test('bodies set Content-Type and serialize JSON', async () => {
  await api.addMemoryFact('user', 'likes tea');
  const req = last();
  assert.equal(req.init.method, 'POST');
  assert.equal(req.init.headers['Content-Type'], 'application/json');
  assert.equal(req.init.body, JSON.stringify({ target: 'user', content: 'likes tea' }));
});

// ── Endpoint shapes ──

test('listSessions builds the pagination query', async () => {
  await api.listSessions({ q: 'deploy', limit: 25, offset: 50 });
  const url = new URL(last().url, 'http://x');
  assert.equal(url.pathname, '/api/sessions');
  assert.equal(url.searchParams.get('q'), 'deploy');
  assert.equal(url.searchParams.get('limit'), '25');
  assert.equal(url.searchParams.get('offset'), '50');
});

test('deleteSession uses DELETE', async () => {
  await api.deleteSession('sess-9', 'tok');
  assert.equal(last().init.method, 'DELETE');
  assert.equal(last().url, '/api/sessions/sess-9');
});

test('renameSession posts the name', async () => {
  await api.renameSession('s', 'new name', 'tok');
  assert.equal(last().init.body, JSON.stringify({ name: 'new name' }));
});

test('pinSession posts the pinned flag', async () => {
  await api.pinSession('s', true, 'tok');
  assert.equal(last().init.body, JSON.stringify({ pinned: true }));
});

test('exportSessionUrl builds the format query', () => {
  assert.equal(api.exportSessionUrl('s1', 'json'), '/api/sessions/s1/export?format=json');
  assert.equal(api.exportSessionUrl('s1'), '/api/sessions/s1/export?format=md');
});

test('cancelSession posts to the session-scoped endpoint', async () => {
  await api.cancelSession('abc', 'tok');
  const req = last();
  assert.equal(req.init.method, 'POST');
  assert.equal(req.url, '/api/cancel?session_id=abc');
  assert.equal(req.init.headers['X-Session-Token'], 'tok');
});

test('getModels hits /api/models', async () => {
  await api.getModels();
  assert.equal(last().url, '/api/models');
});

test('getEvents carries limit and filters', async () => {
  await api.getEvents({ limit: 5, runId: 'r1', sessionId: 's1' });
  const url = new URL(last().url, 'http://x');
  assert.equal(url.searchParams.get('limit'), '5');
  assert.equal(url.searchParams.get('run_id'), 'r1');
  assert.equal(url.searchParams.get('session_id'), 's1');
});

test('run endpoints use the right verbs and paths', async () => {
  await api.listRuns(7);
  assert.equal(last().url, '/api/runs?limit=7');
  await api.getRun('run-1');
  assert.equal(last().url, '/api/runs/run-1');
  await api.cancelRun('run-1');
  assert.equal(last().init.method, 'POST');
  assert.equal(last().url, '/api/runs/run-1/cancel');
  await api.answerRunApproval('run-1', 'apr-9', 'deny');
  const req = last();
  assert.equal(req.init.method, 'POST');
  assert.equal(req.url, '/api/runs/run-1/approvals/apr-9');
  assert.equal(req.init.body, JSON.stringify({ action: 'deny' }));
});

test('jobs and subagents hit their endpoints', async () => {
  await api.listJobs('tok');
  assert.equal(last().url, '/api/jobs');
  assert.equal(last().init.headers['X-Session-Token'], 'tok');
  await api.getJobOutput('j1', 'tok', { since: 10, limit: 32 });
  const out = new URL(last().url, 'http://x');
  assert.equal(out.pathname, '/api/jobs/j1/output');
  assert.equal(out.searchParams.get('since'), '10');
  await api.stopJob('j1', 'tok');
  assert.equal(last().init.method, 'POST');
  assert.equal(last().url, '/api/jobs/j1/stop');
  await api.listSubagents('rk');
  assert.equal(last().url, '/api/subagents?key=rk');
});

test('promote, consolidate, kick, shutdown hit their endpoints', async () => {
  await api.promoteSkill('demo', true);
  assert.equal(last().url, '/api/skills/promote');
  assert.equal(last().init.body, JSON.stringify({ name: 'demo', force: true }));
  await api.consolidateMemory('user');
  assert.equal(last().url, '/api/memory/consolidate');
  await api.kickConnection('c1');
  assert.equal(last().init.method, 'DELETE');
  assert.equal(last().url, '/api/connections/c1');
  await api.shutdownServer();
  assert.equal(last().init.method, 'POST');
  assert.equal(last().url, '/api/shutdown');
});

test('memory mutations hit their endpoints', async () => {
  await api.removeMemoryFact('env', 'old');
  const req = last();
  assert.equal(req.init.method, 'DELETE');
  assert.equal(req.url, '/api/memory/facts');
  assert.equal(req.init.body, JSON.stringify({ target: 'env', old_text: 'old' }));
  await api.promoteEpisode('2026-x');
  assert.equal(last().url, '/api/memory/episodes/promote');
  assert.equal(last().init.body, JSON.stringify({ session_id: '2026-x' }));
});

// ── Response handling ──

test('204 responses resolve to null', async () => {
  reset({ status: 204, body: '', headers: {} });
  assert.equal(await api.getHealth(), null);
});

test('non-2xx responses throw with the server message and status', async () => {
  reset({ status: 401, body: 'invalid session token', headers: {} });
  await assert.rejects(
    () => api.getSession('s'),
    (err) => err.status === 401 && /invalid session token/.test(err.message),
  );
});

test('long error bodies are truncated', async () => {
  reset({ status: 500, body: 'x'.repeat(500), headers: {} });
  await assert.rejects(
    () => api.getHealth(),
    (err) => err.message.length <= 202 && err.message.endsWith('…'),
  );
});
