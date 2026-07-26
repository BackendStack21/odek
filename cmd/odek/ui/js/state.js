// Central mutable client state. Every module mutates through the single
// exported object S (live-binding friendly, no setters needed). This module
// may only import from net.js — never from render/ws/sessions/input/approvals.
import { apiHeaders } from './net.js';

// Migrate legacy kode_* keys to odek_*
['history', 'model', 'theme', 'thinking'].forEach(k => {
  if (!localStorage.getItem('odek_' + k) && localStorage.getItem('kode_' + k)) {
    localStorage.setItem('odek_' + k, localStorage.getItem('kode_' + k));
    localStorage.removeItem('kode_' + k);
  }
});

export const S = {
  // ── Connection / session ──
  ws: null,
  sessionId: null,
  sessionTokens: {}, // session id -> auth token
  busy: false,
  history: JSON.parse(localStorage.getItem('odek_history') || '[]'),
  historyIdx: -1,
  attachedFiles: [], // {name, size, content}
  currentModel: localStorage.getItem('odek_model') || '',
  availableModels: [],
  // Per-query thinking toggle. Persisted so it survives page refresh.
  thinkingEnabled: localStorage.getItem('odek_thinking') === '1',

  // ── Streaming ──
  streamBubbleEl: null,
  streamContentEl: null,
  streamBuffer: '',
  streamRAF: null,
  thinkingContentEl: null, // current thinking block if any

  // ── Tool call state ──
  currentToolBlock: null,
  // FIFO queues per tool name so parallel results route to the correct block.
  // Map<string, HTMLElement[]>
  toolBlockQueues: new Map(),
  // Whether the current turn has started a "tool calls" divider group.
  inToolGroup: false,
  // Timestamps for tool latency (name → start ms, queue-based like above).
  toolStartQueues: new Map(),

  // ── Sub-agent state ──
  subagentGroup: null,

  // ── Smart scroll / toast / loading indicator ──
  scrollRAF: null,
  toastTimer: null,
  loadingEl: null,
  loadingTimer: null,

  // ── Approvals ──
  approvalQueue: [],       // approvalRequest events, FIFO
  activeApprovalId: null,  // id of the request currently rendered
  activeApprovalCard: null,

  // ── Sessions sidebar ──
  allSessions: [],
  pendingDeleteId: null,
  sessionsSig: '',

  // ── @-completion ──
  lastAtIdx: -1,
  lastCursor: -1,
  compQuery: '',

  // ── Saved nodes for restoring the empty state after clearing ──
  savedEmptyStateNode: null,
  savedScrollBtnNode: null,
};

// ── Session token persistence (odek_* localStorage keys) ──

export function getSessionToken(sid) {
  if (!sid) return '';
  return S.sessionTokens[sid] || localStorage.getItem('odek_session_token_' + sid) || '';
}

export function setSessionToken(sid, token) {
  if (!sid || !token) return;
  S.sessionTokens[sid] = token;
  localStorage.setItem('odek_session_token_' + sid, token);
}

export function clearSessionToken(sid) {
  if (!sid) return;
  delete S.sessionTokens[sid];
  localStorage.removeItem('odek_session_token_' + sid);
}

// ensureSessionToken returns a stored token for sid, bootstrapping one from
// the server (GET /api/sessions/<id>) when missing. Used by session REST
// mutations (rename/delete) whose tokens may predate this browser. Returns
// '' on failure — the server will answer 401 if a token was required.
export async function ensureSessionToken(sid) {
  let token = getSessionToken(sid);
  if (token) return token;
  try {
    const bootstrap = await fetch('/api/sessions/' + encodeURIComponent(sid), {
      headers: apiHeaders()
    });
    if (bootstrap.ok) {
      const bs = await bootstrap.json();
      token = bootstrap.headers.get('X-Session-Token') || bs.auth_token;
      if (token) setSessionToken(sid, token);
    }
  } catch { /* continue — server will return 401 if token required */ }
  return token || '';
}
