// Sessions sidebar: list loading/rendering, session switch, rename/delete,
// confirm dialog, sidebar search and mobile toggle.
import { S, getSessionToken, setSessionToken, clearSessionToken, ensureSessionToken } from './state.js';
import { apiHeaders } from './net.js';
import { messagesEl, promptEl, sendBtn, sessionListEl, sidebarSearch, sidebarOverlay } from './dom.js';
import { escapeHtml, escapeAttr, relativeTime, showToast, forceScrollBottom, hideCancel } from './utils.js';
import { resetTurnState, hideLoading, renderSessionHistory } from './render.js';
import { removeActiveApprovalCard } from './approvals.js';

// updateActiveSessionItem syncs the .active marker with the current
// sessionId without re-rendering the list.
function updateActiveSessionItem() {
  sessionListEl.querySelectorAll('.session-item').forEach(el => {
    el.classList.toggle('active', el.dataset.id === S.sessionId);
  });
}

export async function loadSessions() {
  // Skeleton rows only on cold start (empty list); refreshes keep the
  // existing items so scroll position and hover state survive.
  if (!sessionListEl.querySelector('.session-item')) {
    sessionListEl.innerHTML = '<div class="session-skel"></div>'.repeat(3);
  }
  try {
    const resp = await fetch('/api/sessions', { headers: apiHeaders() });
    const sessions = await resp.json();
    if (!sessions || !Array.isArray(sessions)) {
      sessionListEl.querySelectorAll('.session-skel').forEach(el => el.remove());
      return;
    }
    S.allSessions = sessions;

    // Skip the full re-render when nothing changed — this runs after every
    // turn, and re-rendering would steal scroll position and hover state.
    const sig = sessions.map(s => [s.id, s.task, s.turns, s.updated_at, s.model].join('|')).join('\n');
    if (sig === S.sessionsSig) {
      updateActiveSessionItem();
      return;
    }
    S.sessionsSig = sig;

    sessionListEl.innerHTML = sessions.map(s =>
      `<div class="session-item${s.id === S.sessionId ? ' active' : ''}" data-id="${escapeAttr(s.id)}">
        <button class="si-body" type="button" title="Open session">
          <span class="id">${escapeHtml(s.id.slice(0, 8))}</span>
          <span class="task${!s.task ? ' untitled' : ''}">${escapeHtml(s.task || 'untitled')}</span>
          <span class="meta">
            <span>${s.turns || 0} turn${s.turns !== 1 ? 's' : ''}</span><span>${relativeTime(s.updated_at)}</span>
            ${s.model ? `<span class="model-chip">${escapeHtml(s.model)}</span>` : ''}
          </span>
        </button>
        <span class="si-actions">
          <button class="rename-btn" type="button" title="Rename">✎</button>
          <button class="del-btn" type="button" title="Delete">✕</button>
        </span>
      </div>`
    ).join('');
  } catch {
    sessionListEl.querySelectorAll('.session-skel').forEach(el => el.remove());
  }
}

// ── New Session ──
export function newSession() {
  S.sessionId = null;

  // Reset all streaming + tool state.
  resetTurnState();
  // Any pending approval belongs to the previous session's run — drop it.
  S.approvalQueue = [];
  removeActiveApprovalCard();
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
}

export async function loadAndRenderSession(sid) {
  try {
    const token = getSessionToken(sid);
    const resp = await fetch('/api/sessions/' + encodeURIComponent(sid), {
      headers: apiHeaders(token ? { 'X-Session-Token': token } : {})
    });
    if (!resp.ok) { showToast('Failed to load session'); return; }
    const sess = await resp.json();

    // Persist the token returned by the server (bootstrapped for legacy
    // sessions, echoed for current ones).
    const returnedToken = resp.headers.get('X-Session-Token') || sess.auth_token;
    if (returnedToken) setSessionToken(sid, returnedToken);

    // Switch session ID so the next prompt continues this session.
    S.sessionId = sid;

    // Clear current messages and reset all streaming state.
    resetTurnState();
    // Pending approvals belong to the previous view — drop them (the
    // server-side request times out on its own).
    S.approvalQueue = [];
    removeActiveApprovalCard();
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
  const token = await ensureSessionToken(sid);
  fetch('/api/sessions/' + encodeURIComponent(sid), {
    method: 'POST',
    headers: apiHeaders({
      'Content-Type': 'application/json',
      ...(token ? { 'X-Session-Token': token } : {})
    }),
    body: JSON.stringify({ name: newName })
  })
    .then(resp => {
      if (!resp.ok) throw new Error('rename failed');
      loadSessions();
      showToast('Session renamed');
    })
    .catch(() => showToast('Failed to rename session'));
}

// ── Confirm Dialog ──
export function hideConfirmDialog() {
  document.getElementById('confirm-overlay').classList.remove('active');
  S.pendingDeleteId = null;
}

export async function executeDeleteSession() {
  if (!S.pendingDeleteId) return;
  const sid = S.pendingDeleteId;
  S.pendingDeleteId = null;
  document.getElementById('confirm-overlay').classList.remove('active');

  const token = await ensureSessionToken(sid);

  fetch('/api/sessions/' + encodeURIComponent(sid), {
    method: 'DELETE',
    headers: apiHeaders(token ? { 'X-Session-Token': token } : {})
  })
    .then(() => {
      clearSessionToken(sid);
      if (S.sessionId === sid) newSession();
      loadSessions();
    })
    .catch(() => showToast('Failed to delete session'));
}

// ── Sidebar Toggle (mobile) ──
export function toggleSidebar() {
  const sidebar = document.getElementById('sidebar');
  if (!sidebar) return;
  sidebar.classList.toggle('active');
  if (sidebarOverlay) sidebarOverlay.classList.toggle('active');
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
    document.getElementById('confirm-overlay').classList.add('active');
    return;
  }

  // Rename button
  if (e.target.closest('.rename-btn')) {
    e.stopPropagation();
    renameSession(sid, e.target);
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

// Sidebar search
sidebarSearch.addEventListener('input', () => {
  const q = sidebarSearch.value.toLowerCase();
  const items = sessionListEl.querySelectorAll('.session-item');
  items.forEach(item => {
    const text = item.textContent.toLowerCase();
    item.style.display = text.includes(q) ? '' : 'none';
  });
});

// ── Static sidebar / confirm-dialog buttons (formerly inline handlers) ──
document.getElementById('hamburger-btn').addEventListener('click', toggleSidebar);
sidebarOverlay.addEventListener('click', toggleSidebar);
document.querySelector('.new-session-btn').addEventListener('click', newSession);
document.querySelector('#confirm-actions .cancel').addEventListener('click', hideConfirmDialog);
document.getElementById('confirm-delete-btn').addEventListener('click', executeDeleteSession);
