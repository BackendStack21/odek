// Pure-ish helpers: escaping, formatting, clipboard, toast, scrolling, and
// small DOM toggles. Imports only from state.js, dom.js, and untrusted.js.
import { S } from './state.js';
import { messagesEl, scrollBottomBtn, cancelBtn } from './dom.js';

// stripAttachmentBodies lives in untrusted.js (pure, node-testable);
// re-exported here so render.js keeps a single utils import.
export { stripAttachmentBodies } from './untrusted.js';

// ── Escape helpers (implemented in escape.js so markdown.js can use them
// without importing the browser-dependent modules) ──
export { escapeHtml, escapeAttr } from './escape.js';

// ── Number formatting ──
export function formatNum(n) {
  // Coerce to a finite number so a crafted (non-numeric) value can never be
  // reinterpreted as HTML when the result lands in an innerHTML assignment.
  n = Number(n);
  if (!isFinite(n)) n = 0;
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'k';
  return String(n);
}

export function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024*1024) return (bytes/1024).toFixed(1) + ' KB';
  return (bytes/(1024*1024)).toFixed(1) + ' MB';
}

// ── Relative time helper ──
export function relativeTime(dateStr) {
  if (!dateStr) return '';
  const d = new Date(dateStr);
  if (isNaN(d)) return '';
  const secs = Math.floor((Date.now() - d) / 1000);
  if (secs < 60) return 'just now';
  if (secs < 3600) return Math.floor(secs / 60) + 'm ago';
  if (secs < 86400) return Math.floor(secs / 3600) + 'h ago';
  if (secs < 86400 * 7) return Math.floor(secs / 86400) + 'd ago';
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

export function truncateStr(s, n) {
  if (!s) return '';
  return s.length > n ? s.substring(0, n) + '…' : s;
}

// ── Copy helpers ──
// copyTextToClipboard writes text to the clipboard, falling back to the
// legacy execCommand path when the async Clipboard API is unavailable or
// denied (non-secure contexts). Returns a Promise.
export function copyTextToClipboard(text) {
  const fallback = () => {
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
  };
  if (!navigator.clipboard || !navigator.clipboard.writeText) {
    fallback();
    return Promise.resolve();
  }
  return navigator.clipboard.writeText(text).catch(fallback);
}

// ── Toast ──
export function showToast(msg) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(S.toastTimer);
  S.toastTimer = setTimeout(() => el.classList.remove('show'), 2500);
}

// ── Smart Scroll ──
export const SCROLL_THRESHOLD = 100;
export function isNearBottom() {
  return messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < SCROLL_THRESHOLD;
}
export function scrollBottom() {
  if (!isNearBottom()) return; // user is reading up — don't steal scroll
  if (S.scrollRAF) return;
  S.scrollRAF = requestAnimationFrame(() => {
    messagesEl.scrollTop = messagesEl.scrollHeight;
    S.scrollRAF = null;
  });
}
export function forceScrollBottom() {
  if (S.scrollRAF) {
    cancelAnimationFrame(S.scrollRAF);
    S.scrollRAF = null;
  }
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

// Scroll-to-bottom button handler: jump and hide the button.
export function scrollToBottom() {
  messagesEl.scrollTop = messagesEl.scrollHeight;
  if (scrollBottomBtn) scrollBottomBtn.classList.remove('visible');
}

// ── Message cap ──
const MAX_MESSAGES = 80;
export function pruneMessages() {
  const items = messagesEl.querySelectorAll(':scope > .msg, :scope > .tool-block, :scope > .subagent-group, :scope > .thinking-block');
  if (items.length > MAX_MESSAGES) {
    for (let i = 0, n = items.length - MAX_MESSAGES; i < n; i++) {
      items[i].remove();
    }
  }
}

// ── Error message normalization ──
export function formatErrorMessage(msg) {
  if (!msg) return 'Unknown error';
  // Extract the core message from LiteLLM/provider verbose errors
  const match = msg.match(/"message"\s*:\s*"([^"]{0,200})"/) ||
                msg.match(/BadRequestError[^:]*:\s*(.{0,200})/);
  if (match) return match[1].trim();
  return msg.length > 300 ? msg.slice(0, 300) + '…' : msg;
}

// ── Small UI toggles (cancel button, shortcuts overlay) ──
export function showCancel() {
  if (cancelBtn) cancelBtn.classList.add('visible');
}
export function hideCancel() {
  if (cancelBtn) cancelBtn.classList.remove('visible');
}

export function toggleShortcuts() {
  const overlay = document.getElementById('shortcuts-overlay');
  if (isDialogOpen(overlay)) closeDialog();
  else openDialog(overlay);
}

// ── Screen-reader announcements ──
// announce writes to the visually-hidden #sr-status live region. The clear-
// then-set cycle forces re-announcement of identical consecutive messages.
export function announce(msg) {
  const el = document.getElementById('sr-status');
  if (!el) return;
  el.textContent = '';
  setTimeout(() => { el.textContent = msg; }, 30);
}

// ── Modal dialog focus management ──
// openDialog moves focus into the dialog, closeDialog returns it to the
// previously focused element, and a global Tab handler cycles focus within
// the open dialog (focus trap). Only one modal dialog is open at a time.
const FOCUSABLE_SEL = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';
let activeDialog = null; // { overlay, returnFocus }

export function openDialog(overlay) {
  if (!overlay) return;
  closeDialog();
  const dialog = overlay.querySelector('[role="dialog"]');
  activeDialog = { overlay, returnFocus: document.activeElement };
  overlay.classList.add('active');
  if (dialog) {
    const preferred = dialog.querySelector('[data-autofocus]') || dialog.querySelector(FOCUSABLE_SEL);
    (preferred || dialog).focus();
  }
}

export function closeDialog() {
  if (!activeDialog) return;
  const { overlay, returnFocus } = activeDialog;
  activeDialog = null;
  overlay.classList.remove('active');
  if (returnFocus && returnFocus.focus) returnFocus.focus();
}

export function isDialogOpen(overlay) {
  return !!(activeDialog && activeDialog.overlay === overlay);
}

// Focus trap: keep Tab / Shift+Tab cycling inside the open dialog.
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Tab' || !activeDialog) return;
  const dialog = activeDialog.overlay.querySelector('[role="dialog"]');
  if (!dialog) return;
  const items = Array.from(dialog.querySelectorAll(FOCUSABLE_SEL)).filter(el => !el.disabled);
  if (!items.length) { e.preventDefault(); dialog.focus(); return; }
  const first = items[0], last = items[items.length - 1];
  const active = document.activeElement;
  if (e.shiftKey && (active === first || active === dialog)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && (active === last || active === dialog)) {
    e.preventDefault();
    first.focus();
  }
});
