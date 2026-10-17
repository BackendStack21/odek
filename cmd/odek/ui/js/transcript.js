// Transcript DOM budget. A long session must not grow #messages without
// bound: style recalculation, layout and every querySelectorAll over the
// transcript scale with its size. The budget counts rendered units — each
// top-level row, and each row inside a turn stream (a single turn can hold
// hundreds of tool steps) — and trims the oldest ones. Live work is never
// trimmed: the streaming answer, the open reasoning block, the running
// sub-agent group and tool steps still waiting for their result.
//
// Trimmed rows are not lost: a marker at the top of the transcript offers
// them back. Rows rendered from a loaded session carry their message index
// (data-hidx) so the history backlog re-renders exactly what was trimmed;
// rows trimmed from a live turn come back by reloading the persisted session.
import { S } from './state.js';
import { messagesEl } from './dom.js';

export const MAX_UNITS = 300;      // units kept while the view follows the bottom
export const HARD_UNITS = 600;     // ceiling while the operator reads further up
export const HISTORY_UNITS = 150;  // units a session load or "Show earlier" renders

const TOP_UNITS = ['msg', 'turn-stream', 'tool-block', 'subagent-group', 'thinking-block', 'msg-divider'];

function hasClass(el, name) {
  if (!el) return false;
  if (el.classList && el.classList.contains) return el.classList.contains(name);
  return (' ' + (el.className || '') + ' ').indexOf(' ' + name + ' ') !== -1;
}

function kids(el) {
  return el && el.children ? Array.from(el.children) : [];
}

function holds(parent, node) {
  if (!parent || !node) return false;
  if (parent === node) return true;
  if (parent.contains) return parent.contains(node);
  for (let el = node; el; el = el.parentNode) if (el === parent) return true;
  return false;
}

function isStream(el) { return hasClass(el, 'turn-stream'); }
function isTopUnit(el) { return TOP_UNITS.some((c) => hasClass(el, c)); }

// isLive reports a unit that still receives frames for the running turn.
function isLive(el) {
  if (el.dataset && el.dataset.outcome === 'running') return true;
  if (hasClass(el, 'live')) return true;
  const refs = [S.streamBubbleEl, S.thinkingContentEl, S.subagentGroup, S.loadingEl];
  return refs.some((ref) => holds(el, ref));
}

export function countUnits() {
  let n = 0;
  for (const el of kids(messagesEl)) {
    if (isStream(el)) n += kids(el).length;
    else if (isTopUnit(el)) n++;
  }
  return n;
}

export function trimState() {
  S.transcriptTrim ||= { live: 0, backlog: null };
  return S.transcriptTrim;
}

// resetTranscriptTrim forgets trimmed rows and the history backlog. Called
// whenever the transcript is cleared (new session, session load).
export function resetTranscriptTrim() {
  S.transcriptTrim = { live: 0, backlog: null };
  const marker = findMarker();
  if (marker) marker.remove();
}

// setHistoryBacklog records the loaded session's messages and the index
// where the rendered window starts; messages before it are offered back.
export function setHistoryBacklog(messages, end) {
  trimState().backlog = end > 0 ? { messages, end } : null;
  syncTrimMarker();
}

// firstHistoryIndex returns the message index of the first rendered row
// that came from session history, or -1.
function firstHistoryIndex() {
  for (const el of kids(messagesEl)) {
    const rows = isStream(el) ? kids(el) : [el];
    for (const row of rows) {
      if (row.dataset && row.dataset.hidx != null && row.dataset.hidx !== '') return Number(row.dataset.hidx);
    }
  }
  return -1;
}

// pruneMessages trims the oldest units once the budget is exceeded. While
// the operator reads up the trim waits for the hard ceiling, and the view
// is held in place so rows vanishing above it do not move what is read.
export function pruneMessages() {
  if (!messagesEl) return;
  let units = countUnits();
  if (units <= MAX_UNITS) return;
  const reading = !S.stickBottom;
  if (reading && units <= HARD_UNITS) return;

  const before = messagesEl.scrollHeight;
  const state = trimState();
  let lastHidx = null;
  let removedHistory = false;
  // A loaded message renders as several rows (reply, reasoning, tool steps);
  // they leave together so a re-render from the backlog never duplicates or
  // drops part of a message.
  const drop = (row) => {
    const hidx = row.dataset ? row.dataset.hidx : undefined;
    const sameMessage = lastHidx != null && hidx === lastHidx;
    if (units <= MAX_UNITS && !sameMessage) return false;
    if (isLive(row)) return false;
    row.remove();
    units--;
    if (hidx != null && hidx !== '') { lastHidx = hidx; removedHistory = true; }
    else { lastHidx = null; state.live++; }
    return true;
  };

  for (const el of kids(messagesEl)) {
    if (units <= MAX_UNITS && lastHidx == null) break;
    if (isStream(el)) {
      for (const row of kids(el)) {
        if (!drop(row) && units <= MAX_UNITS) break;
      }
      if (!kids(el).length && el !== S.turnStreamEl) el.remove();
    } else if (isTopUnit(el)) {
      if (!drop(el) && units <= MAX_UNITS) break;
    }
  }

  if (removedHistory && state.backlog) {
    const first = firstHistoryIndex();
    state.backlog.end = first >= 0 ? first : state.backlog.messages.length;
  }
  if (reading) messagesEl.scrollTop -= before - messagesEl.scrollHeight;
  syncTrimMarker();
}

// earlierTurns counts the operator turns before the rendered window (the
// persisted list also holds tool results and system records, which would
// inflate a raw message count). Cached per window position.
function earlierTurns(backlog) {
  if (backlog.turnsEnd !== backlog.end) {
    let n = 0;
    for (let i = 0; i < backlog.end; i++) if (backlog.messages[i] && backlog.messages[i].role === 'user') n++;
    backlog.turns = n;
    backlog.turnsEnd = backlog.end;
  }
  return backlog.turns;
}

function findMarker() {
  return kids(messagesEl).find((el) => hasClass(el, 'transcript-trim')) || null;
}

// syncTrimMarker paints the top-of-transcript marker. Rows trimmed from a
// live turn exist only in the persisted session, so they come back through
// a reload, which waits for the running turn to finish.
export function syncTrimMarker() {
  if (!messagesEl) return;
  const state = trimState();
  const backlog = state.backlog && state.backlog.end > 0 ? state.backlog : null;
  let marker = findMarker();
  if (!state.live && !backlog) {
    if (marker) marker.remove();
    return;
  }
  if (!marker) {
    marker = document.createElement('div');
    marker.className = 'transcript-trim';
    marker.setAttribute('role', 'note');
    const text = document.createElement('span');
    text.className = 'tt-text';
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'tt-more';
    marker.appendChild(text);
    marker.appendChild(btn);
  }
  const first = kids(messagesEl)[0];
  if (first !== marker) messagesEl.insertBefore(marker, first || null);
  const text = marker.querySelector('.tt-text');
  const btn = marker.querySelector('.tt-more');
  if (state.live) {
    text.textContent = 'Earlier activity is hidden to keep the page responsive.';
    btn.textContent = S.busy ? 'Reload full history after this turn' : 'Reload full history';
    btn.disabled = !!S.busy || !S.sessionId;
    btn.dataset.action = 'reload';
  } else {
    const turns = earlierTurns(backlog);
    text.textContent = turns
      ? turns + (turns === 1 ? ' earlier turn is' : ' earlier turns are') + ' not shown.'
      : 'Earlier activity is not shown.';
    btn.textContent = 'Show earlier';
    btn.disabled = false;
    btn.dataset.action = 'earlier';
  }
}

messagesEl?.addEventListener?.('click', (e) => {
  const btn = e.target && e.target.closest ? e.target.closest('.tt-more') : null;
  if (!btn || btn.disabled) return;
  if (btn.dataset.action === 'reload') S.reloadSession?.();
  else S.showEarlierHistory?.();
});
