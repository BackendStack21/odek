// Command palette (⌘K) and slash-command dispatch. Printable keys always
// type in the composer — palette/slash live on chords and a leading `/`.
import { S, getSessionToken } from './state.js';
import { promptEl } from './dom.js';
import { escapeHtml, showToast, copyTextToClipboard, openDialog, closeDialog, isDialogOpen } from './utils.js';
import { newSession } from './sessions.js';
import { togglePanels } from './panels.js';
import { downloadExport } from './api.js';

const overlay = () => document.getElementById('palette-overlay');
const inputEl = () => document.getElementById('palette-input');
const resultsEl = () => document.getElementById('palette-results');

const COMMANDS = [
  { id: 'new', title: 'New session', hint: '/new', run: () => newSession() },
  { id: 'clear', title: 'Clear transcript', hint: '/clear', run: () => dispatchSlash('/clear') },
  { id: 'copy', title: 'Copy last reply', hint: 'palette', run: () => dispatchSlash('/copy') },
  { id: 'export-md', title: 'Export markdown', hint: 'palette', run: () => dispatchSlash('/export md') },
  { id: 'export-json', title: 'Export JSON', hint: 'palette', run: () => dispatchSlash('/export json') },
  { id: 'retry', title: 'Retry last prompt', hint: '/retry', run: () => dispatchSlash('/retry') },
  { id: 'cancel', title: 'Cancel running turn', hint: '/cancel', run: () => dispatchSlash('/cancel') },
  { id: 'stop', title: 'Stop running turn', hint: '/stop', run: () => dispatchSlash('/stop') },
  { id: 'theme', title: 'Cycle theme', hint: 'palette', run: () => dispatchSlash('/theme') },
  { id: 'notify', title: 'Toggle notifications', hint: 'palette', run: () => dispatchSlash('/notify') },
  { id: 'help', title: 'Keyboard shortcuts', hint: '?', run: () => dispatchSlash('/help') },
  { id: 'stats', title: 'Session stats', hint: 'palette', run: () => dispatchSlash('/stats') },
  { id: 'sessions', title: 'Sessions', hint: '⌘.', run: () => openTab('sessions') },
  { id: 'shutdown', title: 'Shut down serve', hint: 'typed confirm', run: () => dispatchSlash('/shutdown') },
  { id: 'tab-sessions', title: 'Inspector · sessions', hint: '⌘.', run: () => openTab('sessions') },
  { id: 'tab-now', title: 'Inspector · now', hint: '⌘.', run: () => openTab('now') },
  { id: 'tab-memory', title: 'Inspector · memory', hint: '⌘.', run: () => openTab('memory') },
  { id: 'tab-ops', title: 'Inspector · ops', hint: '⌘.', run: () => openTab('ops') },
];

const COMPOSER_SLASH = new Set(['new', 'clear', 'retry', 'cancel', 'stop']);
const TAB_WS = {
  sessions: 'sessions', session: 'sessions',
  plan: 'now', jobs: 'now', agents: 'now', now: 'now',
  memory: 'memory', skills: 'memory', tools: 'memory',
  runs: 'ops', events: 'ops', config: 'ops', ops: 'ops',
};
const TAB_ID = {
  sessions: 'ptab-sessions',
  now: 'ptab-now',
  memory: 'ptab-memory',
  ops: 'ptab-ops',
};

export function openTab(name) {
  togglePanels(true);
  const ws = TAB_WS[name] || name;
  const btn = document.getElementById(TAB_ID[ws] || 'ptab-now');
  if (btn) btn.click();
}
S.openSessionsPanel = () => openTab('sessions');

let activeIdx = 0;
let rows = [];
let onDispatch = null;

export function setCommandHandlers(handlers) {
  onDispatch = handlers;
}

export function isPaletteOpen() {
  return isDialogOpen(overlay());
}

export function togglePalette(force) {
  const o = overlay();
  if (!o) return;
  const want = force != null ? force : !isPaletteOpen();
  if (want) {
    openDialog(o);
    const inp = inputEl();
    if (inp) {
      inp.value = '';
      inp.setAttribute('aria-expanded', 'true');
      inp.focus();
    }
    renderPalette('');
  } else {
    const inp = inputEl();
    if (inp) {
      inp.setAttribute('aria-expanded', 'false');
      inp.removeAttribute('aria-activedescendant');
    }
    closeDialog();
  }
}

function fuzzy(q, text) {
  const s = String(text || '').toLowerCase();
  const n = String(q || '').toLowerCase().trim();
  if (!n) return 1;
  if (s.includes(n)) return 2 + (s.startsWith(n) ? 2 : 0);
  let i = 0;
  for (const ch of n) {
    const j = s.indexOf(ch, i);
    if (j < 0) return 0;
    i = j + 1;
  }
  return 1;
}

function collectItems(q) {
  const items = [];
  COMMANDS.forEach((c) => {
    const score = Math.max(fuzzy(q, c.title), fuzzy(q, c.hint), fuzzy(q, c.id));
    if (score) items.push({ kind: 'cmd', score, ...c });
  });
  (S.sessionPages || []).slice(0, 20).forEach((s) => {
    const label = s.task || s.id;
    const score = Math.max(fuzzy(q, label), fuzzy(q, s.id), fuzzy(q, s.model || ''));
    if (score) items.push({
      kind: 'session',
      score,
      id: 'sess:' + s.id,
      title: label,
      hint: (s.pinned ? '📌 ' : '') + (s.model || '') + ' · ' + (s.turns || 0) + ' turns',
      run: () => onDispatch && onDispatch.openSession(s.id),
    });
  });
  (S.availableModels || []).forEach((m) => {
    const score = Math.max(fuzzy(q, m.id), fuzzy(q, m.description || ''));
    if (score) items.push({
      kind: 'model',
      score,
      id: 'model:' + m.id,
      title: m.description || m.id,
      hint: m.id + (m.current ? ' · current' : ''),
      run: () => onDispatch && onDispatch.switchModel(m.id),
    });
  });
  items.sort((a, b) => b.score - a.score || a.title.localeCompare(b.title));
  return items.slice(0, 24);
}

function renderPalette(q) {
  rows = collectItems(q);
  activeIdx = 0;
  const box = resultsEl();
  if (!box) return;
  box.textContent = '';
  if (!rows.length) {
    const empty = document.createElement('div');
    empty.className = 'pal-empty';
    empty.textContent = 'no matches';
    box.appendChild(empty);
    const inp = inputEl();
    if (inp) inp.removeAttribute('aria-activedescendant');
    return;
  }
  rows.forEach((row, i) => {
    const el = document.createElement('div');
    el.id = 'pal-opt-' + i;
    el.className = 'pal-row' + (i === 0 ? ' active' : '');
    el.setAttribute('role', 'option');
    el.setAttribute('aria-selected', i === 0 ? 'true' : 'false');
    el.dataset.idx = String(i);
    el.innerHTML = '<span class="pal-kind">' + escapeHtml(row.kind) + '</span>' +
      '<span class="pal-title">' + escapeHtml(row.title) + '</span>' +
      '<span class="pal-hint">' + escapeHtml(row.hint || '') + '</span>';
    el.addEventListener('click', () => activate(i));
    box.appendChild(el);
  });
  syncPaletteActive();
}

function syncPaletteActive() {
  const inp = inputEl();
  if (inp) {
    if (rows.length) inp.setAttribute('aria-activedescendant', 'pal-opt-' + activeIdx);
    else inp.removeAttribute('aria-activedescendant');
  }
  const box = resultsEl();
  if (!box) return;
  box.querySelectorAll('.pal-row').forEach((el, i) => {
    el.classList.toggle('active', i === activeIdx);
    el.setAttribute('aria-selected', i === activeIdx ? 'true' : 'false');
  });
}

function activate(i) {
  const row = rows[i];
  if (!row) return;
  togglePalette(false);
  try { row.run(); } catch (err) { showToast(err.message || 'command failed'); }
}

export function dispatchSlash(raw) {
  const line = String(raw || '').trim();
  if (!line.startsWith('/')) return false;
  const [cmd, ...rest] = line.slice(1).split(/\s+/);
  const arg = rest.join(' ');
  if (!onDispatch) return true;
  switch (cmd) {
    case 'help': onDispatch.help(); break;
    case 'new': newSession(); break;
    case 'clear': onDispatch.clear(); break;
    case 'copy': onDispatch.copyLast(); break;
    case 'export': onDispatch.exportSession(arg === 'json' ? 'json' : 'md'); break;
    case 'retry': onDispatch.retry(); break;
    case 'queue': showToast('Queued prompts sit above the composer'); break;
    case 'theme': onDispatch.cycleTheme(arg); break;
    case 'stats': onDispatch.stats(); break;
    case 'cancel': onDispatch.cancel(); break;
    case 'notify': onDispatch.toggleNotify(); break;
    case 'shutdown': onDispatch.shutdown(); break;
    case 'model': if (arg) onDispatch.switchModel(arg); else togglePalette(true); break;
    case 'now':
    case 'memory':
    case 'ops':
    case 'plan':
    case 'jobs':
    case 'agents':
    case 'skills':
    case 'tools':
    case 'runs':
    case 'events':
    case 'config':
      openTab(cmd);
      break;
    case 'sessions':
    case 'session':
      openTab('sessions');
      break;
    case 'stop':
      if (arg && S.onSubagentStop) S.onSubagentStop(arg);
      else onDispatch.cancel();
      break;
    default:
      showToast('unknown command: /' + cmd);
  }
  return true;
}

export function maybeHandleComposerEnter(text) {
  if (!text.startsWith('/') || text.includes('\n')) return false;
  const cmd = text.slice(1).split(/\s+/)[0];
  if (!COMPOSER_SLASH.has(cmd)) return false;
  return dispatchSlash(text);
}

export async function exportActiveSession(format) {
  if (!S.sessionId) { showToast('No session to export'); return; }
  try {
    await downloadExport(S.sessionId, format, getSessionToken(S.sessionId) || undefined);
    showToast('Exported ' + (format === 'json' ? 'JSON' : 'markdown'));
  } catch (err) {
    showToast('Export failed: ' + (err.message || ''));
  }
}

export function copyLastReply() {
  const bubbles = document.querySelectorAll('#messages .msg.assistant .content');
  const last = bubbles[bubbles.length - 1];
  if (!last) { showToast('Nothing to copy'); return; }
  copyTextToClipboard(last.textContent || '').then(() => showToast('Copied reply'));
}

const palInput = inputEl();
if (palInput) {
  palInput.addEventListener('input', () => renderPalette(palInput.value));
  palInput.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
    else if (e.key === 'Enter') { e.preventDefault(); activate(activeIdx); }
    else if (e.key === 'Escape') { e.preventDefault(); togglePalette(false); promptEl.focus(); }
  });
}

function move(delta) {
  if (!rows.length) return;
  activeIdx = (activeIdx + delta + rows.length) % rows.length;
  syncPaletteActive();
  const cur = resultsEl().querySelector('.pal-row.active');
  if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: 'nearest' });
}

const palOverlay = overlay();
if (palOverlay) {
  palOverlay.addEventListener('click', (e) => {
    if (e.target === palOverlay) togglePalette(false);
  });
}
