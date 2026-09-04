// Centralized REST client for the /api/* management surface. Every module
// goes through these helpers so the instance-token header, session-token
// handling, and error normalization live in exactly one place.
import { apiHeaders } from './net.js';

// apiFetch performs a token-authenticated JSON request and returns the parsed
// body. Throws an Error carrying the server's message on non-2xx responses.
export async function apiFetch(path, opts = {}) {
  const resp = await fetch(path, {
    ...opts,
    headers: apiHeaders({
      ...(opts.body ? { 'Content-Type': 'application/json' } : {}),
      ...(opts.sessionToken ? { 'X-Session-Token': opts.sessionToken } : {}),
      ...(opts.headers || {}),
    }),
  });
  if (!resp.ok) {
    let msg = 'request failed (' + resp.status + ')';
    try {
      const text = await resp.text();
      if (text) msg = text.length > 200 ? text.slice(0, 200) + '…' : text;
    } catch { /* keep default */ }
    const err = new Error(msg);
    err.status = resp.status;
    throw err;
  }
  if (resp.status === 204) return null;
  const ct = resp.headers.get('Content-Type') || '';
  return ct.includes('application/json') ? resp.json() : resp.text();
}

// ── Health ──
export function getHealth() {
  return apiFetch('/api/health');
}

// ── Sessions ──
// listSessions returns {sessions, offset, limit, count, query} when called
// with params (the server's paginated envelope) — always called that way by
// the sidebar so callers get one shape.
export function listSessions({ q = '', limit = 50, offset = 0 } = {}) {
  const params = new URLSearchParams();
  if (q) params.set('q', q);
  params.set('limit', String(limit));
  params.set('offset', String(offset));
  return apiFetch('/api/sessions?' + params.toString());
}

export function getSession(id, sessionToken) {
  return apiFetch('/api/sessions/' + encodeURIComponent(id), { sessionToken });
}

// getSessionPlan fetches the read-only structured plan view for a session
// (docs/PLANNING.md). Returns {session_id, version, steps, found}; found=false
// (still HTTP 200) means the transcript carries no parseable plan message.
// Auth mirrors the sibling GET endpoints: session token via apiFetch.
export function getSessionPlan(id, sessionToken) {
  return apiFetch('/api/sessions/' + encodeURIComponent(id) + '/plan', { sessionToken });
}

export function renameSession(id, name, sessionToken) {
  return apiFetch('/api/sessions/' + encodeURIComponent(id), {
    method: 'POST',
    body: JSON.stringify({ name }),
    sessionToken,
  });
}

export function deleteSession(id, sessionToken) {
  return apiFetch('/api/sessions/' + encodeURIComponent(id), {
    method: 'DELETE',
    sessionToken,
  });
}

// exportSessionUrl builds the download URL for a transcript export. The
// browser navigates to it (fetch + blob would also work, but a navigation
// gets the Content-Disposition filename for free). Session token travels in
// the query string only if the cookie is unavailable — prefer the header
// path via downloadExport below.
export function exportSessionUrl(id, format) {
  return '/api/sessions/' + encodeURIComponent(id) + '/export?format=' + encodeURIComponent(format || 'md');
}

// downloadExport fetches the export with proper auth headers and triggers a
// file download from the blob (keeps tokens out of URLs and history).
export async function downloadExport(id, format, sessionToken) {
  const resp = await fetch(exportSessionUrl(id, format), {
    headers: apiHeaders(sessionToken ? { 'X-Session-Token': sessionToken } : {}),
  });
  if (!resp.ok) throw new Error('export failed (' + resp.status + ')');
  const blob = await resp.blob();
  const cd = resp.headers.get('Content-Disposition') || '';
  const m = cd.match(/filename=([^;]+)/);
  const name = m ? m[1].trim().replace(/^"|"$/g, '') : 'odek-session.' + (format === 'json' ? 'json' : 'md');
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

// ── Models / limits ──
export function getModels() {
  return apiFetch('/api/models');
}

export function getLimits() {
  return apiFetch('/api/limits');
}

// ── Memory ──
export function getMemory() {
  return apiFetch('/api/memory');
}

export function addMemoryFact(target, content) {
  return apiFetch('/api/memory/facts', {
    method: 'POST',
    body: JSON.stringify({ target, content }),
  });
}

export function removeMemoryFact(target, oldText) {
  return apiFetch('/api/memory/facts', {
    method: 'DELETE',
    body: JSON.stringify({ target, old_text: oldText }),
  });
}

export function promoteEpisode(sessionId) {
  return apiFetch('/api/memory/episodes/promote', {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId }),
  });
}

// ── Skills / Tools ──
export function getSkills() {
  return apiFetch('/api/skills');
}

export function getTools() {
  return apiFetch('/api/tools');
}

// ── Cancel (REST fallback for the WS cancel) ──
export function cancelSession(id, sessionToken) {
  return apiFetch('/api/cancel?session_id=' + encodeURIComponent(id), {
    method: 'POST',
    sessionToken,
  });
}

// ── Session pinning ──
export function pinSession(id, pinned, sessionToken) {
  return apiFetch('/api/sessions/' + encodeURIComponent(id), {
    method: 'POST',
    body: JSON.stringify({ pinned }),
    sessionToken,
  });
}

// ── Headless runs ──
export function listRuns(limit = 50) {
  return apiFetch('/api/runs?limit=' + limit);
}

export function getRun(id) {
  return apiFetch('/api/runs/' + encodeURIComponent(id));
}

export function cancelRun(id) {
  return apiFetch('/api/runs/' + encodeURIComponent(id) + '/cancel', { method: 'POST' });
}

export function answerRunApproval(runId, approvalId, action) {
  return apiFetch('/api/runs/' + encodeURIComponent(runId) + '/approvals/' + encodeURIComponent(approvalId), {
    method: 'POST',
    body: JSON.stringify({ action }),
  });
}

// ── Events / usage / config / mcp ──
export function getEvents({ limit = 100, runId = '', sessionId = '' } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (runId) params.set('run_id', runId);
  if (sessionId) params.set('session_id', sessionId);
  return apiFetch('/api/events?' + params.toString());
}

export function getUsage() {
  return apiFetch('/api/usage');
}

export function getConfig() {
  return apiFetch('/api/config');
}

export function getMCPServers() {
  return apiFetch('/api/mcp');
}

export function getConnections() {
  return apiFetch('/api/connections');
}
