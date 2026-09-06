// Entry point: init sequence, theme, model picker (with built-in profiles),
// cancel, global keyboard shortcuts. Feature modules self-register their listeners.
import { S, getSessionToken } from './state.js';
import { promptEl, skeletonEl } from './dom.js';
import { escapeHtml, escapeAttr, showToast, toggleShortcuts, hideCancel, closeDialog, formatNum } from './utils.js';
import { addSystemMessage } from './render.js';
import { loadSessions, loadAndRenderSession } from './sessions.js';
import { connect, wsSend } from './ws.js';
import { togglePanels } from './panels.js';
import { initMetrics, setMetricsModel, sessionCostUSD } from './metrics.js';
import { setCommandHandlers, togglePalette, isPaletteOpen, copyLastReply, exportActiveSession, openTab } from './commands.js';
import { retryLast } from './input.js';
import { requestNotify, syncNotifyBtn, togglePopover } from './health.js';
import { getModels, cancelSession, shutdownServer } from './api.js';
import './input.js';
import './approvals.js';
import './health.js';
import './commands.js';

// ── Init ──
// Save references so newSession() can restore the empty state after clearing.
S.savedEmptyStateNode = document.getElementById('empty-state');
S.savedScrollBtnNode = document.getElementById('scroll-bottom-btn');

// Empty-state hint actions (the saved node is re-appended on session
// switches, so direct listeners persist). The hints are role="button"
// spans, so Enter/Space must activate them like a click.
function activateOnKey(el, fn) {
  el.addEventListener('click', fn);
  el.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fn(); }
  });
}
if (S.savedEmptyStateNode) {
  const hintFn = {
    palette: () => togglePalette(true),
    at: () => { promptEl.focus(); if (!promptEl.value.includes('@')) promptEl.value += '@'; },
    slash: () => { promptEl.focus(); if (!promptEl.value.startsWith('/')) promptEl.value = '/'; },
    inspector: () => togglePanels(true),
    sessions: () => openTab('sessions'),
  };
  S.savedEmptyStateNode.querySelectorAll('[data-hint]').forEach((el) => {
    const fn = hintFn[el.getAttribute('data-hint')];
    if (fn) activateOnKey(el, fn);
  });
}

const THEMES = ['ember-dark', 'ember-light', 'high-contrast'];
const THEME_GLYPH = { 'ember-dark': '◐', 'ember-light': '☀', 'high-contrast': '▣' };

function applyTheme(name) {
  const theme = THEMES.includes(name) ? name : 'ember-dark';
  S.theme = theme;
  document.body.classList.remove('light', 'theme-ember-dark', 'theme-ember-light', 'theme-high-contrast', 'theme-classic');
  document.body.classList.add('theme-' + theme);
  if (theme === 'ember-light') document.body.classList.add('light');
  const root = document.documentElement;
  if (root && root.style) root.style.colorScheme = theme === 'ember-light' ? 'light' : 'dark';
  localStorage.setItem('odek_theme', theme);
  const btn = document.getElementById('theme-btn');
  if (btn) {
    btn.textContent = THEME_GLYPH[theme] || '◐';
    btn.title = 'Theme: ' + theme + ' (click to cycle)';
  }
}

function cycleTheme(want) {
  if (want && THEMES.includes(want)) { applyTheme(want); return; }
  const i = THEMES.indexOf(S.theme);
  applyTheme(THEMES[(i + 1) % THEMES.length]);
  showToast('Theme: ' + S.theme);
}

applyTheme(S.theme);
document.getElementById('theme-btn').addEventListener('click', () => cycleTheme());

// ── Model Picker ──
const customModelInput = document.getElementById('custom-model-input');

async function fetchModels() {
  const picker = document.getElementById('model-picker');
  try {
    picker.disabled = true;
    const models = await getModels();
    S.availableModels = Array.isArray(models) ? models : [];
    if (S.availableModels.length === 0) {
      picker.innerHTML = '<option value="">No models</option>';
      return;
    }
    let html = '';
    S.availableModels.forEach(m => {
      const sel = S.currentModel === m.id ? ' selected' : '';
      const label = m.current ? '★ ' + (m.description || m.id) : (m.description || m.id);
      html += `<option value="${escapeAttr(m.id)}"${sel}>${escapeHtml(label)}</option>`;
    });
    // "Other..." sentinel opens the free-text input.
    html += '<option value="__custom__">Other (type model ID)…</option>';
    picker.innerHTML = html;
    if (S.currentModel) {
      picker.value = S.currentModel;
      // If the current model isn't in the list, show the custom input.
      if (!picker.value && S.currentModel) {
        picker.value = '__custom__';
        customModelInput.value = S.currentModel;
        customModelInput.style.display = 'inline-block';
      }
    }
  } catch {
    picker.innerHTML = '<option value="">Failed to load</option>';
  } finally {
    picker.disabled = false;
  }
}

// Handles both known models and the "Other…" sentinel that reveals the
// free-text custom model input.
function onPickerChange(value) {
  if (value === '__custom__') {
    customModelInput.style.display = 'inline-block';
    customModelInput.focus();
    customModelInput.select();
    return;
  }
  customModelInput.style.display = 'none';
  customModelInput.value = '';
  if (value) switchModel(value);
}
document.getElementById('model-picker').addEventListener('change', (e) => onPickerChange(e.target.value));

// Commit a custom model ID from the text input on Enter or blur.
customModelInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); commitCustomModel(); }
  if (e.key === 'Escape') {
    customModelInput.style.display = 'none';
    customModelInput.value = '';
    const picker = document.getElementById('model-picker');
    if (S.currentModel) picker.value = S.currentModel;
  }
});
customModelInput.addEventListener('blur', () => {
  if (customModelInput.value.trim()) commitCustomModel();
});

function commitCustomModel() {
  const id = customModelInput.value.trim();
  if (!id) return;
  switchModel(id);
  // Add as a selectable option if not already present.
  const picker = document.getElementById('model-picker');
  let found = false;
  for (const opt of picker.options) { if (opt.value === id) { found = true; break; } }
  if (!found) {
    const opt = document.createElement('option');
    opt.value = id;
    opt.textContent = id;
    // Insert before the "Other…" sentinel
    const sentinel = picker.querySelector('option[value="__custom__"]');
    picker.insertBefore(opt, sentinel);
  }
  picker.value = id;
  customModelInput.style.display = 'none';
}

function switchModel(modelId) {
  S.currentModel = modelId;
  setMetricsModel(modelId);
  if (modelId) {
    localStorage.setItem('odek_model', modelId);
  } else {
    localStorage.removeItem('odek_model');
  }
  const label = document.getElementById('model-label');
  if (label) label.textContent = modelId || '';
  showToast(modelId ? 'Model: ' + modelId : 'Using default model');
}

// ── Cancel Button ──
// Prefer the in-socket cancel (no header juggling, immediate); fall back to
// the REST endpoint when the socket is down but the session is known.
function cancelAgent() {
  if (!S.sessionId) {
    hideCancel();
    addSystemMessage('⏹ No active session to cancel');
    return;
  }
  const token = getSessionToken(S.sessionId);
  if (wsSend({
    type: 'cancel',
    session_id: S.sessionId,
    auth_token: token || undefined,
  })) {
    hideCancel();
    addSystemMessage('⏹ Cancel requested');
    return;
  }
  cancelSession(S.sessionId, token || undefined).catch(() => {});
  hideCancel();
  addSystemMessage('⏹ Canceled');
}
document.getElementById('cancel-btn').addEventListener('click', cancelAgent);

// Per-sub-agent stop: cards call back through here; main owns socket +
// session-token access (same auth envelope as the turn cancel). The
// server replies with a subagent_cancelled ack; the terminal card state
// arrives as the subagent_state finished/cancelled transition.
S.onSubagentStop = (taskID) => {
  if (!S.sessionId || !taskID) return;
  wsSend({
    type: 'subagent_cancel',
    session_id: S.sessionId,
    auth_token: getSessionToken(S.sessionId) || undefined,
    task_id: taskID,
  });
};

// ── Management panels ──
document.getElementById('panels-btn').addEventListener('click', () => togglePanels());
const paletteBtn = document.getElementById('palette-btn');
if (paletteBtn) paletteBtn.addEventListener('click', () => togglePalette(true));
const notifyBtn = document.getElementById('notify-btn');
if (notifyBtn) notifyBtn.addEventListener('click', () => {
  if (S.notifyEnabled) {
    S.notifyEnabled = false;
    localStorage.setItem('odek_notify', '0');
    syncNotifyBtn();
    showToast('Notifications off');
  } else {
    requestNotify();
  }
});
syncNotifyBtn();

function openNowFromChip() {
  openTab('now');
}
['plan-chip', 'jobs-chip'].forEach((id) => {
  const chip = document.getElementById(id);
  if (!chip) return;
  chip.addEventListener('click', openNowFromChip);
  chip.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openNowFromChip();
    }
  });
});

const costChip = document.getElementById('cost-chip');
if (costChip) {
  const openCost = (e) => {
    e.stopPropagation();
    togglePopover();
  };
  costChip.addEventListener('click', openCost);
  costChip.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    openCost(e);
  });
}

// ── Shortcuts overlay ──
document.getElementById('shortcuts-overlay').addEventListener('click', (e) => {
  if (e.target === e.currentTarget) toggleShortcuts();
});

function showStats() {
  const m = S.metrics || {};
  const cost = sessionCostUSD();
  addSystemMessage(
    'stats · ctx ' + formatNum(m.ctxTokens || 0) +
    (m.maxContext ? '/' + formatNum(m.maxContext) : '') +
    ' · ⇥ ' + formatNum(m.sessIn || 0) + ' ↦ ' + formatNum(m.sessOut || 0) +
    (cost != null ? ' · ◈ $' + cost.toFixed(4) : '')
  );
}

function clearTranscript() {
  if (S.busy) { showToast('Finish or cancel the turn first'); return; }
  if (!confirm('Clear the visible transcript? The session stays on disk.')) return;
  messagesElClear();
}

function messagesElClear() {
  const messages = document.getElementById('messages');
  if (!messages) return;
  messages.innerHTML = '';
  if (S.savedScrollBtnNode) messages.appendChild(S.savedScrollBtnNode);
  if (S.savedEmptyStateNode) messages.appendChild(S.savedEmptyStateNode);
}

function openShutdown() {
  const o = document.getElementById('shutdown-overlay');
  if (o) o.classList.add('active');
  const inp = document.getElementById('shutdown-input');
  const btn = document.getElementById('shutdown-confirm');
  if (inp) { inp.value = ''; inp.focus(); }
  if (btn) btn.disabled = true;
}

function closeShutdown() {
  const o = document.getElementById('shutdown-overlay');
  if (o) o.classList.remove('active');
}

const shutdownInput = document.getElementById('shutdown-input');
const shutdownConfirm = document.getElementById('shutdown-confirm');
const shutdownCancel = document.getElementById('shutdown-cancel');
if (shutdownInput && shutdownConfirm) {
  shutdownInput.addEventListener('input', () => {
    shutdownConfirm.disabled = shutdownInput.value.trim() !== 'shutdown';
  });
  shutdownConfirm.addEventListener('click', async () => {
    if (shutdownInput.value.trim() !== 'shutdown') return;
    try {
      await shutdownServer();
      showToast('Shutting down…');
      closeShutdown();
    } catch (err) {
      showToast('shutdown failed: ' + err.message);
    }
  });
}
if (shutdownCancel) shutdownCancel.addEventListener('click', closeShutdown);

setCommandHandlers({
  help: toggleShortcuts,
  clear: clearTranscript,
  copyLast: copyLastReply,
  exportSession: exportActiveSession,
  retry: retryLast,
  cycleTheme,
  stats: showStats,
  cancel: cancelAgent,
  toggleNotify: () => notifyBtn && notifyBtn.click(),
  shutdown: openShutdown,
  switchModel,
  openSession: (id) => loadAndRenderSession(id),
});

// ── Connect + initial load ──
connect();
if (skeletonEl) skeletonEl.classList.add('visible');
loadSessions();
fetchModels();
initMetrics();
promptEl.focus();

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeShutdown();
    closeDialog();
    S.pendingDeleteId = null;
    return;
  }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault();
    togglePalette(!isPaletteOpen());
    return;
  }
  if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 'c') {
    e.preventDefault();
    copyLastReply();
    return;
  }
  if ((e.metaKey || e.ctrlKey) && e.key === '.') {
    e.preventDefault();
    togglePanels();
    return;
  }
  const tag = (e.target && e.target.tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') {
    if (e.key === 'r' && e.altKey && e.target.id === 'prompt') {
      e.preventDefault();
      retryLast();
    }
    return;
  }
  if (e.key === 'r' && e.altKey) {
    e.preventDefault();
    retryLast();
  }
  if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
    e.preventDefault();
    jumpTurn(e.key === 'ArrowUp' ? -1 : 1);
  }
});

function jumpTurn(dir) {
  const turns = Array.from(document.querySelectorAll('#messages .msg.assistant, #messages .msg.user'));
  if (!turns.length) return;
  const mid = window.innerHeight / 3;
  let idx = 0;
  turns.forEach((el, i) => {
    const r = el.getBoundingClientRect ? el.getBoundingClientRect() : { top: 0 };
    if (r.top <= mid) idx = i;
  });
  idx = Math.max(0, Math.min(turns.length - 1, idx + dir));
  if (turns[idx].scrollIntoView) turns[idx].scrollIntoView({ block: 'start' });
}
