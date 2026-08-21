// Entry point: init sequence, theme, model picker (with built-in profiles),
// thinking toggle, cancel, global keyboard shortcuts. Feature modules
// self-register their listeners.
import { S, getSessionToken } from './state.js';
import { getModels, getProfiles, cancelSession } from './api.js';
import { promptEl, skeletonEl, thinkBtn } from './dom.js';
import { escapeHtml, escapeAttr, showToast, toggleShortcuts, hideCancel, closeDialog } from './utils.js';
import { addSystemMessage } from './render.js';
import { loadSessions } from './sessions.js';
import { connect, wsSend } from './ws.js';
import { togglePanels } from './panels.js';
import { initMetrics, setMetricsModel } from './metrics.js';
import './input.js';
import './approvals.js';
import './health.js';

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
  if (hints[2]) activateOnKey(hints[2], () => togglePanels(true));
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
    const [models, profilesData] = await Promise.all([getModels(), getProfiles().catch(() => null)]);
    S.availableModels = models || [];
    S.availableProfiles = (profilesData && profilesData.profiles) || [];
    if (S.availableModels.length === 0 && S.availableProfiles.length === 0) {
      picker.innerHTML = '<option value="">No models</option>';
      return;
    }
    let html = '';
    S.availableModels.forEach(m => {
      const sel = S.currentModel === m.id ? ' selected' : '';
      const label = m.current ? '★ ' + (m.description || m.id) : (m.description || m.id);
      html += `<option value="${escapeAttr(m.id)}"${sel}>${escapeHtml(label)}</option>`;
    });
    // Built-in profiles go under an optgroup in the "Other…" section so the
    // configured model stays the headline entry.
    if (S.availableProfiles.length > 0) {
      html += '<optgroup label="known models">';
      S.availableProfiles.forEach(p => {
        const sel = S.currentModel === p.id ? ' selected' : '';
        const ctx = p.max_context ? ' — ' + Math.round(p.max_context / 1024) + 'K ctx' : '';
        html += `<option value="${escapeAttr(p.id)}"${sel}>${escapeHtml(p.label + ctx)}</option>`;
      });
      html += '</optgroup>';
    }
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

// ── Management panels ──
document.getElementById('panels-btn').addEventListener('click', () => togglePanels());

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
initMetrics();
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
  // Alt+M toggles the management panels
  if (e.key === 'm' && e.altKey) {
    e.preventDefault();
    togglePanels();
  }
});
