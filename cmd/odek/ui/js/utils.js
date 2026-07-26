// Pure-ish helpers: escaping, formatting, clipboard, toast, scrolling, and
// small DOM toggles. Imports only from state.js and dom.js.
import { S } from './state.js';
import { messagesEl, scrollBottomBtn, cancelBtn } from './dom.js';

// ── Escape helpers ──
export function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

export function escapeAttr(s) {
  if (!s) return '';
  // & must be replaced first — doing it last double-escapes the entities
  // introduced by the quote replacements (&quot; → &amp;quot;).
  return s.replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/'/g,'&#39;')
          .replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

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

// Replace inlined attachment blocks (--- name (size) ---\n...\n--- end name ---)
// with chip-style placeholders so reloaded user messages don't dump file bodies.
export function stripAttachmentBodies(content) {
  if (!content) return '';
  const re = /^--- (.+?) \(([^)]+)\) ---\n[\s\S]*?\n--- end \1 ---\n?/gm;
  return content.replace(re, (m, name, size) => '📎 ' + name + ' (' + size + ')\n');
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
  document.getElementById('shortcuts-overlay').classList.toggle('active');
}
