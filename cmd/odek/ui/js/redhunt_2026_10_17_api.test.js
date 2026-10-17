// Skill promotion requests from the WebUI carry the reviewed file's identity.
import { test } from 'node:test';
import assert from 'node:assert/strict';

const captured = [];
globalThis.document = globalThis.document || {
  querySelector: (sel) => (sel === 'meta[name="odek-ws-token"]' ? { getAttribute: () => 'tok' } : null),
};
globalThis.fetch = async (url, init = {}) => {
  captured.push({ url: String(url), init });
  return {
    ok: true,
    status: 204,
    headers: { get: () => null },
    json: async () => ({}),
    text: async () => '',
  };
};

const api = await import('./api.js');

test('RED: skill promote sends the reviewed path and content hash', async () => {
  await api.promoteSkill('b', true, { path: '.odek/skills/a/SKILL.md', sha256: 'ab'.repeat(32) });
  const req = captured[captured.length - 1];
  assert.equal(req.url, '/api/skills/promote');
  assert.deepEqual(JSON.parse(req.init.body), {
    name: 'b', force: true, path: '.odek/skills/a/SKILL.md', sha256: 'ab'.repeat(32),
  });
});

test('RED: skill promote without a review binding sends the name only', async () => {
  await api.promoteSkill('b', false);
  assert.deepEqual(JSON.parse(captured[captured.length - 1].init.body), { name: 'b', force: false });
});
