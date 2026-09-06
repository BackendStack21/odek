// Sessions inspector tab: server-side search (debounced), pagination
// ("more"), session switch (WS session_switch), rename/delete, and
// transcript export (markdown / json download).
import { S, getSessionToken, clearSessionToken, ensureSessionToken } from './state.js';
import { listSessions, getSession, renameSession as renameSessionAPI, deleteSession, downloadExport, pinSession } from './api.js';
import { messagesEl, promptEl, sendBtn, sessionListEl, sidebarSearch, cancelBtn } from './dom.js';
import { escapeHtml, escapeAttr, relativeTime, formatNum, showToast, forceScrollBottom, hideCancel, announce, openDialog, closeDialog, isDialogOpen } from './utils.js';
import { resetTurnState, hideLoading, renderSessionHistory } from './render.js';
import { clearApprovals } from './approvals.js';
import { metricsFromSession, resetMetrics } from './metrics.js';
import { resetPlanPanel } from './plan.js';

const PAGE_SIZE = 50;
const moreBtn = document.getElementById('sessions-more');

// syncSidebarCount updates the header badge with the visible session count.
function syncSidebarCount() {
  const el = document.getElementById('sidebar-count');
  if (el) el.textContent = String(S.sessionPages.length);
}

// updateActiveSessionItem syncs the .active marker with the current
// sessionId without re-rendering the list.
function updateActiveSessionItem() {
  sessionListEl.querySelectorAll('.session-item').forEach(el => {
    el.classList.toggle('active', el.dataset.id === S.sessionId);
  });
}

// ── Loading (server-side search + pagination) ──

// loadSessions fetches the first page for the current search query and
// replaces the list. Called on init, refresh, and after turns complete.
export async function loadSessions() {
  S.sessionOffset = 0;
  S.sessionPages = [];
  S.sessionsExhausted = false;
  await loadSessionsPage(true);
}
S.refreshSessions = loadSessions;

// loadSessionsMore appends the next page (if any).
export async function loadSessionsMore() {
  if (S.sessionsExhausted) return;
  await loadSessionsPage(false);
}

async function loadSessionsPage(replace) {
  // Skeleton rows only on cold start (empty list); refreshes keep the
  // existing items so scroll position and hover state survive.
  if (replace && !sessionListEl.querySelector('.session-item')) {
    sessionListEl.innerHTML = '<div class="session-skel"></div>'.repeat(6);
  }
  try {
    const data = await listSessions({
      q: S.sessionSearch,
      limit: PAGE_SIZE,
      offset: S.sessionOffset,
    });
    const sessions = (data && data.sessions) || [];
    S.allSessions = sessions;

    if (replace) {
      S.sessionPages = sessions;
      sessionListEl.innerHTML = '';
    } else {
      // Dedup by id when appending (a session updated between page fetches
      // can appear twice).
      const seen = new Set(S.sessionPages.map(s => s.id));
      S.sessionPages = S.sessionPages.concat(sessions.filter(s => !seen.has(s.id)));
    }
    S.sessionOffset += sessions.length;
    S.sessionsExhausted = sessions.length < PAGE_SIZE;

    renderSessionItems();
  } catch (err) {
    sessionListEl.querySelectorAll('.session-skel').forEach(el => el.remove());
    // A silent catch here is how a stale list goes unnoticed — say it.
    showToast('Session list refresh failed');
  }
}

function renderSessionItems() {
  sessionListEl.querySelectorAll('.session-skel').forEach(el => el.remove());
  const sessions = S.sessionPages;

  // Skip the full re-render when nothing changed — this runs after every
  // turn, and re-rendering would steal scroll position and hover state.
  const sig = sessions.map(s => [s.id, s.task, s.turns, s.updated_at, s.model].join('|')).join('\n');
  if (sig === S.sessionsSig && sessionListEl.querySelector('.session-item')) {
    updateActiveSessionItem();
    syncMoreButton();
    syncSidebarCount();
    return;
  }
  S.sessionsSig = sig;

  if (!sessions.length) {
    sessionListEl.innerHTML = '<div class="sessions-empty">' +
      (S.sessionSearch ? 'no match' : 'none yet') + '</div>';
    syncMoreButton();
    syncSidebarCount();
    return;
  }

  sessionListEl.innerHTML = sessions.map(s => {
    const usage = [];
    if (s.input_tokens > 0) usage.push('<span class="si-tok" title="Session cumulative input tokens">⇥ ' + formatNum(s.input_tokens) + '</span>');
    if (s.output_tokens > 0) usage.push('<span class="si-tok" title="Session cumulative output tokens">↦ ' + formatNum(s.output_tokens) + '</span>');
    const when = relativeTime(s.updated_at);
    const whenFull = s.updated_at ? new Date(s.updated_at).toLocaleString() : '';
    return `<div class="session-item${s.id === S.sessionId ? ' active' : ''}${s.pinned ? ' pinned' : ''}" data-id="${escapeAttr(s.id)}">
        <button class="si-body" type="button" title="Open session">
          <span class="id">${escapeHtml(s.id.slice(0, 8))}</span>
          <span class="task${!s.task ? ' untitled' : ''}">${escapeHtml(s.task || 'untitled')}</span>
          <span class="meta">
            <span class="si-turns">${s.turns || 0} turn${s.turns !== 1 ? 's' : ''}</span>
            <span class="si-when"${whenFull ? ` title="${escapeAttr(whenFull)}"` : ''}>${when}</span>
            ${usage.join('')}
            ${s.model ? `<span class="model-chip">${escapeHtml(s.model)}</span>` : ''}
          </span>
        </button>
        <span class="si-actions">
          <button class="pin-btn${s.pinned ? ' on' : ''}" type="button" title="${s.pinned ? 'Unpin' : 'Pin to top'}" aria-label="${s.pinned ? 'Unpin session' : 'Pin session to top'}">📌</button>
          <button class="rename-btn" type="button" title="Rename" aria-label="Rename session">✎</button>
          <button class="export-btn" type="button" title="Export transcript (Shift: JSON)" aria-label="Export transcript">⇩</button>
          <button class="del-btn" type="button" title="Delete" aria-label="Delete session">✕</button>
        </span>
      </div>`;
  }).join('');
  updateActiveSessionItem();
  syncMoreButton();
  syncSidebarCount();
}

function syncMoreButton() {
  if (!moreBtn) return;
  moreBtn.hidden = S.sessionsExhausted || !S.sessionPages.length;
}

if (moreBtn) moreBtn.addEventListener('click', loadSessionsMore);

// ── Server-side search (debounced, with a clear button) ──
let searchTimer = null;
const searchClear = document.createElement('span');
searchClear.className = 'si-search-clear';
searchClear.textContent = '✕';
searchClear.setAttribute('role', 'button');
searchClear.setAttribute('tabindex', '0');
searchClear.title = 'Clear search';
searchClear.addEventListener('click', clearSessionSearch);
searchClear.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); clearSessionSearch(); }
});
sidebarSearch.insertAdjacentElement('afterend', searchClear);

function clearSessionSearch() {
  if (!sidebarSearch.value) return;
  sidebarSearch.value = '';
  syncSearchClear();
  S.sessionSearch = '';
  loadSessions();
  sidebarSearch.focus();
}
function syncSearchClear() {
  searchClear.classList.toggle('visible', sidebarSearch.value !== '');
}
sidebarSearch.addEventListener('input', () => {
  syncSearchClear();
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    S.sessionSearch = sidebarSearch.value.trim();
    loadSessions();
  }, 250);
});
sidebarSearch.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && sidebarSearch.value) {
    e.stopPropagation();
    clearSessionSearch();
  }
});
syncSearchClear();

// ── New Session ──
export function newSession() {
  // F-A2: a live turn owns the transcript and the socket. Ask before
  // tearing it down; the cancel goes through the same stop control the
  // user would click (main.js cancelAgent). Never silently force busy off.
  if (S.busy) {
    if (!confirm('A turn is still running. Cancel it and switch to a new session?')) return;
    if (cancelBtn) cancelBtn.click();
  }
  S.sessionId = null;
  resetMetrics();
  // Plan and jobs are session-scoped — drop both before the next attach.
  S.jobs = [];
  resetPlanPanel();

  // Reset all streaming + tool state.
  resetTurnState();
  // Any pending approval belongs to the previous session's run — drop it.
  clearApprovals();
  S.busy = false;
  hideLoading(); hideCancel();
  sendBtn.disabled = !S.ws || S.ws.readyState !== WebSocket.OPEN;
  promptEl.disabled = false;

  // Clear messages and restore empty state.
  messagesEl.innerHTML = '';
  if (S.savedScrollBtnNode) messagesEl.appendChild(S.savedScrollBtnNode);
  if (S.savedEmptyStateNode) messagesEl.appendChild(S.savedEmptyStateNode);

  sessionListEl.querySelectorAll('.session-item').forEach(s => s.classList.remove('active'));
  showToast('New session');
  promptEl.value = '';
  promptEl.style.height = 'auto';
  promptEl.focus();
  if (typeof S.closePanels === 'function') S.closePanels();
}

// switchSession tells the server to load the session into this connection's
// agent (buffer restore) via session_switch, then renders the transcript
// from the REST detail. Falls back to the legacy piggyback-on-prompt path
// when the socket is down.
export async function loadAndRenderSession(sid) {
  // F-A2: same mid-run guard as newSession — ask, cancel through the stop
  // control, and only then bootstrap the switch (which fetches).
  if (S.busy) {
    if (!confirm('A turn is still running. Cancel it and switch sessions?')) return;
    if (cancelBtn) cancelBtn.click();
  }
  try {
    // Bootstrap the session token when missing (ensureSessionToken captures
    // the server's X-Session-Token echo on its detail fetch).
    let token = getSessionToken(sid);
    if (!token) {
      token = await ensureSessionToken(sid);
    }
    const sess = await getSession(sid, token || undefined);

    // Switch session ID so the next prompt continues this session, and
    // seed the metrics cluster from the stored totals.
    S.sessionId = sid;
    metricsFromSession(sess);
    // Swap the plan panel over to the newly loaded session (clears the
    // previous session's rows synchronously, then refetches if visible).
    S.jobs = [];
    resetPlanPanel();

    // Ask the server to adopt the session on this connection (restores the
    // memory buffer into the agent). Non-fatal when the socket is closed.
    if (S.ws && S.ws.readyState === WebSocket.OPEN) {
      S.ws.send(JSON.stringify({
        type: 'session_switch',
        session_id: sid,
        auth_token: getSessionToken(sid) || undefined,
      }));
    }

    // Clear current messages and reset all streaming state.
    resetTurnState();
    // Pending approvals belong to the previous view — drop them (the
    // server-side request times out on its own).
    clearApprovals();
    S.busy = false; hideLoading(); hideCancel();
    sendBtn.disabled = !S.ws || S.ws.readyState !== WebSocket.OPEN;
    promptEl.disabled = false;

    messagesEl.innerHTML = '';
    if (S.savedScrollBtnNode) messagesEl.appendChild(S.savedScrollBtnNode);

    const messages = sess.messages || [];
    // The full transcript renders now (tool calls, thinking, sub-agents);
    // the empty check still keys on conversational messages only.
    const visible = messages.filter(m => m.role === 'user' || m.role === 'assistant');

    if (visible.length === 0) {
      if (S.savedEmptyStateNode) messagesEl.appendChild(S.savedEmptyStateNode);
      showToast('Empty session');
      return;
    }

    renderSessionHistory(messages);

    forceScrollBottom();
    showToast('Session loaded');
    announce('Session loaded');
    if (typeof S.closePanels === 'function') S.closePanels();
  } catch (err) {
    showToast('Error loading session');
  }
}

// ── Session Rename ──
// Inline edit: swaps the task label for an input; Enter commits, Esc
// cancels, blur commits. No window.prompt.
export function renameSession(sid, el) {
  const item = el.closest('.session-item');
  if (!item) return;
  const taskEl = item.querySelector('.task');
  if (!taskEl || taskEl.querySelector('.si-rename-input')) return;
  const currentName = taskEl.textContent;

  const input = document.createElement('input');
  input.className = 'si-rename-input';
  input.type = 'text';
  input.value = currentName === 'untitled' ? '' : currentName;
  input.placeholder = 'session name…';
  taskEl.textContent = '';
  taskEl.appendChild(input);
  input.focus();
  input.select();

  let done = false;
  const finish = (commit) => {
    if (done) return;
    done = true;
    const newName = input.value.trim();
    taskEl.textContent = currentName;
    if (commit && newName && newName !== currentName) {
      doRenameSession(sid, newName);
    }
  };
  input.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('click', (e) => e.stopPropagation());
  input.addEventListener('blur', () => finish(true));
}

async function doRenameSession(sid, newName) {
  try {
    const token = await ensureSessionToken(sid);
    await renameSessionAPI(sid, newName, token || undefined);
    loadSessions();
    showToast('Session renamed');
    announce('Session renamed');
  } catch {
    showToast('Failed to rename session');
  }
}

async function togglePinSession(sid) {
  try {
    const current = S.sessionPages.find(s => s.id === sid);
    const token = await ensureSessionToken(sid);
    await pinSession(sid, !(current && current.pinned), token || undefined);
    loadSessions();
  } catch {
    showToast('Failed to toggle pin');
  }
}

// ── Session Export ──
async function exportSession(sid, format) {
  try {
    const token = await ensureSessionToken(sid);
    await downloadExport(sid, format, token || undefined);
    showToast('Exported ' + (format === 'json' ? 'JSON' : 'markdown'));
    announce('Session exported');
  } catch (err) {
    showToast('Export failed: ' + (err.message || ''));
  }
}

// ── Confirm Dialog ──
export function hideConfirmDialog() {
  const overlay = document.getElementById('confirm-overlay');
  if (isDialogOpen(overlay)) closeDialog();
  else overlay.classList.remove('active');
  S.pendingDeleteId = null;
}

export async function executeDeleteSession() {
  if (!S.pendingDeleteId) return;
  const sid = S.pendingDeleteId;
  S.pendingDeleteId = null;
  hideConfirmDialog();

  // Optimistic removal: drop the row from the DOM (and the page cache)
  // immediately so the list always answers the confirmation, then confirm
  // against the server. On failure the refresh below restores it.
  const item = sessionListEl.querySelector(`.session-item[data-id="${CSS.escape(sid)}"]`);
  if (item) {
    item.classList.add('deleting');
    const height = item.offsetHeight;
    item.style.height = height + 'px';
    requestAnimationFrame(() => {
      item.classList.add('gone');
      setTimeout(() => item.remove(), 220);
    });
  }
  S.sessionPages = S.sessionPages.filter(s => s.id !== sid);
  S.sessionsSig = ''; // force the next render to rebuild
  syncSidebarCount();

  try {
    const token = await ensureSessionToken(sid);
    await deleteSession(sid, token || undefined);
    clearSessionToken(sid);
    if (S.sessionId === sid) newSession();
    await loadSessions();
    announce('Session deleted');
    showToast('Session deleted');
  } catch {
    showToast('Failed to delete session — refreshing');
    await loadSessions().catch(() => {});
  }
}

// ── Open the inspector sessions tab ──
export function toggleSidebar() {
  const drawer = document.getElementById('panels');
  const tab = drawer && drawer.querySelector('.ptab.active');
  if (drawer && drawer.classList.contains('active') && tab && tab.dataset.tab === 'sessions') {
    if (typeof S.closePanels === 'function') S.closePanels();
    return;
  }
  if (typeof S.openSessionsPanel === 'function') S.openSessionsPanel();
}

// ── Session list click delegation ──
sessionListEl.addEventListener('click', (e) => {
  const item = e.target.closest('.session-item');
  if (!item) return;
  const sid = item.dataset.id;
  if (!sid) return;

  // Delete button
  if (e.target.closest('.del-btn')) {
    e.stopPropagation();
    S.pendingDeleteId = sid;
    document.getElementById('confirm-msg').textContent = 'Delete session ' + sid.slice(0, 8) + '...?';
    openDialog(document.getElementById('confirm-overlay'));
    return;
  }

  // Rename button
  if (e.target.closest('.rename-btn')) {
    e.stopPropagation();
    renameSession(sid, e.target);
    return;
  }

  // Pin toggle — pinned sessions float to the top of the list.
  if (e.target.closest('.pin-btn')) {
    e.stopPropagation();
    togglePinSession(sid);
    return;
  }

  // Export button — markdown by default, json with Shift held.
  if (e.target.closest('.export-btn')) {
    e.stopPropagation();
    exportSession(sid, e.shiftKey ? 'json' : 'md');
    return;
  }

  // Load and render session (click on the item body)
  if (e.target.closest('.si-body')) {
    if (sid === S.sessionId) return;
    sessionListEl.querySelectorAll('.session-item').forEach(s => s.classList.remove('active'));
    item.classList.add('active');
    loadAndRenderSession(sid);
  }
});

// ── Static sidebar / confirm-dialog buttons (formerly inline handlers) ──
document.getElementById('hamburger-btn').addEventListener('click', toggleSidebar);
document.querySelector('.new-session-btn').addEventListener('click', newSession);
document.querySelector('#confirm-actions .cancel').addEventListener('click', hideConfirmDialog);
document.getElementById('confirm-delete-btn').addEventListener('click', executeDeleteSession);
