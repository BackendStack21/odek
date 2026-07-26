// Entry point: init sequence, theme, model picker, thinking toggle, cancel,
// global keyboard shortcuts. Feature modules self-register their listeners.
import { S, getSessionToken } from './state.js';
import { apiHeaders } from './net.js';
import { promptEl, skeletonEl, thinkBtn } from './dom.js';
import { escapeHtml, escapeAttr, showToast, toggleShortcuts, hideCancel, closeDialog } from './utils.js';
import { addSystemMessage } from './render.js';
import { loadSessions } from './sessions.js';
import { connect } from './ws.js';
import './input.js';
import './approvals.js';

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
  const hints = S.savedEmptyStateNode.querySelectorAll('.es-hints span');
  if (hints[0]) activateOnKey(hints[0], toggleShortcuts);
  if (hints[1]) activateOnKey(hints[1], loadSessions);
}

// ── Theme Toggle ──
function toggleTheme() {
  document.body.classList.toggle('light');
  const isLight = document.body.classList.contains('light');
  localStorage.setItem('odek_theme', isLight ? 'light' : 'dark');
  document.getElementById('theme-btn').textContent = isLight ? '☀️' : '🌙';
}
document.getElementById('theme-btn').addEventListener('click', toggleTheme);
// Restore theme on load
if (localStorage.getItem('odek_theme') === 'light') {
  document.body.classList.add('light');
  document.getElementById('theme-btn').textContent = '☀️';
}

// ── Thinking mode toggle ──────────────────────────────────────────────
function syncThinkBtn() {
  if (!thinkBtn) return;
  thinkBtn.classList.toggle('active', S.thinkingEnabled);
  thinkBtn.title = S.thinkingEnabled
    ? 'Thinking ON — click to disable extended reasoning'
    : 'Thinking OFF — click to enable extended reasoning';
}

function toggleThinkingMode() {
  S.thinkingEnabled = !S.thinkingEnabled;
  localStorage.setItem('odek_thinking', S.thinkingEnabled ? '1' : '0');
  syncThinkBtn();
  showToast(S.thinkingEnabled ? '🧠 Thinking enabled' : 'Thinking off');
}
thinkBtn.addEventListener('click', toggleThinkingMode);

syncThinkBtn(); // restore persisted state on load

// ── Model Picker ──
const customModelInput = document.getElementById('custom-model-input');

async function fetchModels() {
  const picker = document.getElementById('model-picker');
  try {
    picker.disabled = true;
    const resp = await fetch('/api/models', { headers: apiHeaders() });
    if (!resp.ok) { picker.innerHTML = '<option value="">Models unavailable</option>'; return; }
    const models = await resp.json();
    S.availableModels = models;
    if (!models || models.length === 0) {
      picker.innerHTML = '<option value="">No models</option>';
      return;
    }
    let html = '';
    models.forEach(m => {
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
  if (modelId) {
    localStorage.setItem('odek_model', modelId);
  } else {
    localStorage.removeItem('odek_model');
  }
  showToast(modelId ? 'Model: ' + modelId : 'Using default model');
}

// ── Cancel Button ──
function cancelAgent() {
  if (!S.sessionId) {
    hideCancel();
    addSystemMessage('⏹ No active session to cancel');
    return;
  }
  fetch('/api/cancel?session_id=' + encodeURIComponent(S.sessionId), {
    method: 'POST',
    headers: apiHeaders({ 'X-Session-Token': getSessionToken(S.sessionId) || '' })
  }).catch(() => {});
  hideCancel();
  addSystemMessage('⏹ Canceled');
}
document.getElementById('cancel-btn').addEventListener('click', cancelAgent);

// ── Shortcuts overlay ──
document.getElementById('shortcuts-overlay').addEventListener('click', (e) => {
  if (e.target === e.currentTarget) toggleShortcuts();
});

// ── Connect + initial load ──
connect();
// Show skeleton while connecting
if (skeletonEl) skeletonEl.classList.add('visible');
loadSessions();
fetchModels();
promptEl.focus();

// Handle keyboard shortcuts globally
document.addEventListener('keydown', (e) => {
  // Escape closes overlays. Approval cards are deliberately NOT dismissed —
  // a decision must be made explicitly.
  if (e.key === 'Escape') {
    closeDialog(); // whichever overlay dialog is open (shortcuts / confirm)
    S.pendingDeleteId = null;
  }
  // Ctrl+R refreshes sessions
  if (e.key === 'r' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault();
    loadSessions();
    showToast('Sessions refreshed');
  }
  // Alt+T toggles thinking mode
  if (e.key === 't' && e.altKey && !S.busy) {
    e.preventDefault();
    toggleThinkingMode();
  }
});
