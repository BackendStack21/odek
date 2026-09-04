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

// loadHistory reads the persisted prompt history defensively (F-B6): a
// corrupt odek_history value used to throw at module load and crash the
// whole client bundle. Corrupt (unparseable) or non-array values fall back
// to [] and the bad key is purged so the next write starts clean.
function loadHistory() {
  const raw = localStorage.getItem('odek_history');
  if (raw == null) return [];
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) return parsed;
  } catch { /* fall through to purge */ }
  localStorage.removeItem('odek_history');
  return [];
}

export const S = {
  // ── Connection / session ──
  ws: null,
  sessionId: null,
  sessionTokens: {}, // session id -> auth token
  busy: false,
  health: null,      // last server info snapshot (health.js)
  history: loadHistory(),
  historyIdx: -1,
  attachedFiles: [], // {name, size, content}
  currentModel: localStorage.getItem('odek_model') || '',
  availableModels: [], // GET /api/models (ListModels + configured)
  // Per-query thinking toggle. Persisted so it survives page refresh.
  thinkingEnabled: localStorage.getItem('odek_thinking') === '1',

  // ── Sessions sidebar (server-side search + pagination) ──
  sessionSearch: '',   // current server-side query
  sessionOffset: 0,    // next page offset
  sessionPages: [],    // accumulated sessions across pages
  sessionsExhausted: false,

  // ── Current run ──
  runStartedAt: 0,   // ms timestamp while a prompt is executing
  runIterations: 0,  // LLM iterations seen via usage events

  // ── Streaming ──
  streamBubbleEl: null,
  streamContentEl: null,
  streamBuffer: '',   // unflushed fragments awaiting the next rAF
  streamText: '',     // full accumulated answer text for this turn
  streamCursorEl: null,
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
