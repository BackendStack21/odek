// Prompt handling: send, history navigation, @-completion, file
// attachments, drag-and-drop, auto-resize, and the scroll-bottom button.
import { S, getSessionToken } from './state.js';
import { apiHeaders } from './net.js';
import {
  messagesEl, promptEl, sendBtn, completionEl,
  scrollBottomBtn, fileInput, attachBtn, fileChips,
} from './dom.js';
import {
  escapeHtml, escapeAttr, formatFileSize,   scrollToBottom,
  showCancel, toggleShortcuts, SCROLL_THRESHOLD, teach,
} from './utils.js';
import { addMessage, resetTurnState, showLoading, paintIntent } from './render.js';
import { maybeHandleComposerEnter } from './commands.js';

function queueId() {
  return 'q' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
}

export function renderQueueStrip() {
  const strip = document.getElementById('queue-strip');
  if (!strip) return;
  strip.textContent = '';
  if (!S.promptQueue.length) {
    strip.hidden = true;
    return;
  }
  strip.hidden = false;
  S.promptQueue.forEach((item, i) => {
    const row = document.createElement('div');
    row.className = 'queue-row';
    const text = document.createElement('span');
    text.className = 'queue-text';
    text.textContent = item.text || (item.attachments.length ? item.attachments.length + ' file(s)' : '(empty)');
    const up = document.createElement('button');
    up.type = 'button';
    up.className = 'queue-btn';
    up.textContent = '▲';
    up.disabled = i === 0;
    up.addEventListener('click', () => moveQueue(i, -1));
    const down = document.createElement('button');
    down.type = 'button';
    down.className = 'queue-btn';
    down.textContent = '▼';
    down.disabled = i === S.promptQueue.length - 1;
    down.addEventListener('click', () => moveQueue(i, 1));
    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'queue-btn';
    del.textContent = '✕';
    del.addEventListener('click', () => {
      S.promptQueue.splice(i, 1);
      renderQueueStrip();
    });
    row.append(text, up, down, del);
    strip.appendChild(row);
  });
  paintIntent();
}

function moveQueue(i, delta) {
  const j = i + delta;
  if (j < 0 || j >= S.promptQueue.length) return;
  const tmp = S.promptQueue[i];
  S.promptQueue[i] = S.promptQueue[j];
  S.promptQueue[j] = tmp;
  renderQueueStrip();
}

export function drainQueue() {
  // Do not auto-send through a live approval — the operator is still deciding.
  if (S.busy || S.activeApprovalId || !S.promptQueue.length) return;
  const next = S.promptQueue.shift();
  renderQueueStrip();
  sendPayload(next.text, next.attachments, next.display);
}
S.drainQueue = drainQueue;

// ── Send ──
export function send() {
  // F-B2: dead socket still rejects BEFORE touching attachments.
  if (!S.ws || S.ws.readyState !== WebSocket.OPEN) return;
  const text = promptEl.value.trim();
  if (!text && S.attachedFiles.length === 0) return;

  if (maybeHandleComposerEnter(text)) {
    promptEl.value = '';
    promptEl.style.height = 'auto';
    return;
  }

  let display = text;
  let attachments = [];
  if (S.attachedFiles.length > 0) {
    const chips = S.attachedFiles.map(f => '📎 ' + f.name + ' (' + formatFileSize(f.size) + ')').join('\n');
    display = chips + (text ? '\n\n' + text : '');
    attachments = S.attachedFiles.map(f => ({ name: f.name, content: f.content }));
    clearAttachedFiles();
  }

  S.history.push(text);
  if (S.history.length > 100) S.history.shift();
  localStorage.setItem('odek_history', JSON.stringify(S.history));
  S.historyIdx = S.history.length;
  promptEl.value = '';
  promptEl.style.height = 'auto';

  if (S.busy) {
    S.promptQueue.push({
      id: queueId(),
      text,
      display,
      attachments,
      model: S.currentModel || undefined,
    });
    renderQueueStrip();
    teach('queue', 'tip: Enter queues the next prompt · reorder in the strip above');
    return;
  }

  sendPayload(text, attachments, display);
}

function sendPayload(text, attachments, display) {
  addMessage('user', display);
  resetTurnState();
  S.lastPrompt = text;
  S.lastFailedPrompt = '';
  S.busy = true;
  S.runStartedAt = Date.now();
  S.runIterations = 0;
  sendBtn.disabled = true;
  showLoading();
  showCancel();

  S.ws.send(JSON.stringify({
    type: 'prompt',
    content: text,
    attachments: attachments,
    session_id: S.sessionId,
    auth_token: getSessionToken(S.sessionId) || undefined,
    model: S.currentModel || undefined,
  }));
}

export function retryLast() {
  const text = S.lastFailedPrompt || S.lastPrompt;
  if (!text) { return; }
  promptEl.value = text;
  send();
}

// ── File Attachments ──
function addAttachedFile(file) {
  // Check total size (max 10MB total)
  const totalSize = S.attachedFiles.reduce((s, f) => s + f.size, 0) + file.size;
  if (totalSize > 10 * 1024 * 1024) {
    addErrorChip(file.name, 'total attachments exceed 10 MB');
    return;
  }
  S.attachedFiles.push(file);
  renderFileChips();
}

function removeAttachedFile(index) {
  S.attachedFiles.splice(index, 1);
  renderFileChips();
}

function clearAttachedFiles() {
  S.attachedFiles = [];
  renderFileChips();
}

function renderFileChips() {
  // Build nodes with textContent rather than innerHTML so a file name can
  // never be reinterpreted as markup.
  fileChips.textContent = '';
  S.attachedFiles.forEach((f, i) => {
    const chip = document.createElement('span');
    chip.className = 'file-chip';

    const icon = document.createElement('span');
    icon.className = 'chip-icon';
    icon.textContent = '📎';

    const name = document.createElement('span');
    name.className = 'chip-name';
    name.textContent = f.name;

    const size = document.createElement('span');
    size.className = 'chip-size';
    size.textContent = formatFileSize(f.size);

    const remove = document.createElement('span');
    remove.className = 'chip-remove';
    remove.textContent = '✕';
    remove.setAttribute('role', 'button');
    remove.setAttribute('tabindex', '0');
    remove.setAttribute('aria-label', 'Remove ' + f.name);
    remove.addEventListener('click', () => removeAttachedFile(i));
    remove.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); removeAttachedFile(i); }
    });

    chip.append(icon, name, size, remove);
    fileChips.appendChild(chip);
  });

  // Total-size meter against the 10 MB cap.
  if (S.attachedFiles.length > 0) {
    const total = S.attachedFiles.reduce((s, f) => s + f.size, 0);
    const meter = document.createElement('span');
    meter.className = 'chips-total';
    meter.textContent = formatFileSize(total) + ' / 10 MB';
    if (total > 8 * 1024 * 1024) meter.classList.add('warn');
    fileChips.appendChild(meter);
  }
}

// addErrorChip shows a transient error chip for a file that could not be
// attached (too large, unreadable). Dismisses on click or after 6s.
function addErrorChip(name, reason) {
  const chip = document.createElement('span');
  chip.className = 'file-chip error';
  chip.title = reason;
  const icon = document.createElement('span');
  icon.className = 'chip-icon';
  icon.textContent = '⚠️';
  const label = document.createElement('span');
  label.className = 'chip-name';
  label.textContent = name + ' — ' + reason;
  const remove = document.createElement('span');
  remove.className = 'chip-remove';
  remove.textContent = '✕';
  remove.setAttribute('role', 'button');
  remove.setAttribute('tabindex', '0');
  remove.setAttribute('aria-label', 'Dismiss');
  chip.append(icon, label, remove);
  fileChips.appendChild(chip);
  const dismiss = () => chip.remove();
  remove.addEventListener('click', dismiss);
  remove.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); dismiss(); }
  });
  setTimeout(dismiss, 6000);
}

function readFileAsText(file) {
  return new Promise((resolve, reject) => {
    // Limit individual files to 5MB
    if (file.size > 5 * 1024 * 1024) {
      reject(new Error('File too large (max 5 MB): ' + file.name));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

function handleFiles(fileList) {
  const promises = [];
  for (let i = 0; i < fileList.length; i++) {
    const file = fileList[i];
    promises.push(
      readFileAsText(file).then(content => {
        addAttachedFile({name: file.name, size: file.size, content});
      }).catch(err => {
        addErrorChip(file.name, err.message || 'could not read file');
      })
    );
  }
  return Promise.all(promises);
}

// Attach button
attachBtn.addEventListener('click', () => fileInput.click());
fileInput.addEventListener('change', () => {
  handleFiles(fileInput.files);
  fileInput.value = '';
});

// ── Scroll-to-bottom button visibility ──
messagesEl.addEventListener('scroll', () => {
  if (!scrollBottomBtn) return;
  const atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < SCROLL_THRESHOLD;
  scrollBottomBtn.classList.toggle('visible', !atBottom);
});
scrollBottomBtn.addEventListener('click', scrollToBottom);

// Drag and drop on messages area
messagesEl.addEventListener('dragover', (e) => {
  e.preventDefault();
  messagesEl.classList.add('drag-over');
});
messagesEl.addEventListener('dragleave', () => {
  messagesEl.classList.remove('drag-over');
});
messagesEl.addEventListener('drop', (e) => {
  e.preventDefault();
  messagesEl.classList.remove('drag-over');
  if (e.dataTransfer.files.length > 0) {
    handleFiles(e.dataTransfer.files);
    promptEl.focus();
  }
});

// ── Auto-resize ──
promptEl.addEventListener('input', () => {
  promptEl.style.height = 'auto';
  promptEl.style.height = Math.min(promptEl.scrollHeight, 200) + 'px';
});

// ── Input handlers ──
promptEl.addEventListener('keydown', (e) => {
  // @-completion keyboard navigation takes precedence while visible:
  // ↑/↓ move, Enter/Tab accept, Esc dismisses.
  if (completionEl.classList.contains('visible')) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      moveCompletionSelection(e.key === 'ArrowDown' ? 1 : -1);
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      selectCompletion();
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      completionEl.classList.remove('visible');
      return;
    }
  }

  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    send();
    return;
  }

  if (e.key === 'Enter' && e.shiftKey) {
    // Shift+Enter = new line (default behavior)
    return;
  }

  // ? = toggle shortcuts
  if (e.key === '?' && !e.shiftKey && promptEl.value === '') {
    e.preventDefault();
    toggleShortcuts();
    return;
  }

  // History up/down (only when completion is hidden)
  if (completionEl.classList.contains('visible')) return;

  if (e.key === 'ArrowUp') {
    if (S.historyIdx > 0) {
      e.preventDefault();
      S.historyIdx--;
      promptEl.value = S.history[S.historyIdx] || '';
      promptEl.selectionStart = promptEl.selectionEnd = promptEl.value.length;
    }
    return;
  }
  if (e.key === 'ArrowDown') {
    if (S.historyIdx < S.history.length - 1) {
      e.preventDefault();
      S.historyIdx++;
      promptEl.value = S.history[S.historyIdx] || '';
    } else {
      S.historyIdx = S.history.length;
      promptEl.value = '';
    }
    return;
  }
});

// ── Send button ──
sendBtn.addEventListener('click', send);

// ── Textarea + @ key detection ──
let completionTimer = null;

promptEl.addEventListener('input', () => {
  if (completionTimer) clearTimeout(completionTimer);
  completionTimer = setTimeout(checkCompletion, 150);
});

promptEl.addEventListener('keydown', (e) => {
  if (e.key === '@') {
    if (completionTimer) clearTimeout(completionTimer);
    completionTimer = setTimeout(checkCompletion, 150);
  }

  // Tab for completion selection
  if (e.key === 'Tab' && completionEl.classList.contains('visible')) {
    e.preventDefault();
    selectCompletion();
    return;
  }
});

// ── @ Completion ──
completionEl.addEventListener('click', (e) => {
  const item = e.target.closest('.comp-item');
  if (!item) return;
  replaceCompletion(item.dataset.id);
  completionEl.classList.remove('visible');
});

completionEl.addEventListener('mousemove', (e) => {
  const item = e.target.closest('.comp-item');
  if (!item) return;
  completionEl.querySelectorAll('.comp-item').forEach(el => {
    el.classList.toggle('selected', el === item);
    el.setAttribute('aria-selected', el === item);
  });
});

async function checkCompletion() {
  const val = promptEl.value;
  const cursor = promptEl.selectionStart;
  const before = val.slice(0, cursor);

  const atIdx = before.lastIndexOf('@');
  if (atIdx < 0) {
    completionEl.classList.remove('visible');
    return;
  }

  const query = before.slice(atIdx + 1).split(/\s/)[0];
  if (!query) {
    completionEl.classList.remove('visible');
    return;
  }

  S.lastAtIdx = atIdx;
  S.lastCursor = cursor;
  S.compQuery = query;
  // F-B5: staleness token — the response is dropped unless the prompt text
  // AND cursor are exactly where they were when the request left.
  const reqToken = val + '\u0000' + cursor;

  try {
    const resp = await fetch('/api/resources?q=' + encodeURIComponent(query) + '&limit=8', {
      headers: apiHeaders()
    });
    if (!resp.ok) {
      completionEl.classList.remove('visible');
      return;
    }
    const results = await resp.json();
    if (!Array.isArray(results) || results.length === 0) {
      completionEl.classList.remove('visible');
      return;
    }
    if (promptEl.value + '\u0000' + promptEl.selectionStart !== reqToken) {
      return; // stale: keep whatever popup state matches the current input
    }

    completionEl.innerHTML = results.map((r, i) =>
      `<div class="comp-item${i === 0 ? ' selected' : ''}" role="option" aria-selected="${i === 0}" data-id="${escapeAttr(r.id)}">
        <span class="comp-type">${escapeAttr(r.type)}</span>
        <span class="comp-label">${escapeHtml(r.label)}</span>
        <span class="comp-detail">${escapeHtml(r.detail || '')}</span>
      </div>`
    ).join('');

    completionEl.classList.add('visible');
  } catch {
    completionEl.classList.remove('visible');
  }
}

// moveCompletionSelection moves the .selected marker by delta (+1/-1),
// keeping aria-selected in sync for assistive technology.
function moveCompletionSelection(delta) {
  const items = Array.from(completionEl.querySelectorAll('.comp-item'));
  if (items.length === 0) return;
  let idx = items.findIndex(el => el.classList.contains('selected'));
  idx = (idx + delta + items.length) % items.length;
  items.forEach((el, i) => {
    el.classList.toggle('selected', i === idx);
    el.setAttribute('aria-selected', i === idx);
  });
}

function selectCompletion() {
  const selected = completionEl.querySelector('.selected');
  if (!selected) return;
  replaceCompletion(selected.dataset.id);
  completionEl.classList.remove('visible');
}

function replaceCompletion(id) {
  promptEl.value = promptEl.value.slice(0, S.lastAtIdx) + id + ' ' + promptEl.value.slice(S.lastCursor);
  const newPos = S.lastAtIdx + id.length + 1;
  promptEl.selectionStart = promptEl.selectionEnd = newPos;
  promptEl.focus();
}
