// Message rendering: streaming, thinking, tool blocks, sub-agent swarm,
// session-history rendering, collapse/copy affordances, and the loading
// indicator. Imports only from state/dom/utils/markdown/untrusted.
import { S } from './state.js';
import { messagesEl, promptEl, sendBtn, emptyState } from './dom.js';
import {
  escapeHtml, escapeAttr, truncateStr, copyTextToClipboard,
  pruneMessages, scrollBottom, forceScrollBottom, stripAttachmentBodies,
  showCancel, hideCancel, teach,
} from './utils.js';
import { markdownToHtml } from './markdown.js';
import { parseUntrusted } from './untrusted.js';
import { classifyToolResult, chipsHtml, prettyToolBody, collectReceipt, formatReceipt } from './tools.js';

// ── Turn state ──
// resetTurnState clears all per-turn streaming/tool/sub-agent state. Called
// before a new turn (send), on new session, and when loading a session.
export function resetTurnState() {
  // Finished-turn reasoning stays collapsed (Bodek calm default).
  // Only blocks this renderer marked .live are touched.
  messagesEl.querySelectorAll('.thinking-block.live').forEach(block => {
    const content = block.querySelector('.thinking-content');
    const toggle = block.querySelector('.thinking-toggle');
    const arrow = toggle ? toggle.querySelector('.arrow') : null;
    if (content) content.classList.remove('open');
    if (arrow) arrow.classList.remove('open');
    if (toggle) toggle.setAttribute('aria-expanded', 'false');
    block.classList.remove('live');
  });

  S.streamBuffer = '';
  S.streamText = '';
  S.streamCursorEl = null;
  if (S.streamRAF) {
    cancelAnimationFrame(S.streamRAF);
    S.streamRAF = null;
  }
  S.streamBubbleEl = null;
  S.streamContentEl = null;
  S.currentToolBlock = null;
  S.subagentGroup = null;
  S.thinkingContentEl = null;
  S.toolBlockQueues.clear();
  S.toolStartQueues.clear();
  S.inToolGroup = false;
}

// ── Hide empty state ──
export function hideEmptyState() {
  if (emptyState && emptyState.parentNode) {
    emptyState.remove();
  }
}

export function setIntent(text) {
  if (arguments.length) S.currentIntent = String(text || '').trim();
  paintIntent();
}

// Bodek status line: braille spinner + one stable label + elapsed +
// queued count. The label only changes on phase (reasoning → tool →
// composing), never as a ticker — a 1s verb cycle made the rail frantic.
const SPIN_FRAMES = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const SPIN_MS = 83; // Bodek: 12 fps
let spinTimer = null;
let spinIdx = 0;

function reduceMotion() {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
}

function spinGlyph() {
  return reduceMotion() ? '⠿' : SPIN_FRAMES[spinIdx % SPIN_FRAMES.length];
}

function spinNodes() {
  const out = [];
  const railSpin = document.getElementById('intent-rail');
  if (railSpin && railSpin.querySelector) {
    const n = railSpin.querySelector('.ir-spin');
    if (n) out.push(n);
  }
  const top = document.getElementById('busy-spin');
  if (top) out.push(top);
  if (S.loadingEl && S.loadingEl.querySelector) {
    const n = S.loadingEl.querySelector('.ir-spin');
    if (n) out.push(n);
  }
  return out;
}

function paintSpin() {
  const ch = spinGlyph();
  spinNodes().forEach((n) => { n.textContent = ch; });
}

function startSpin() {
  paintSpin();
  if (spinTimer || reduceMotion()) return;
  spinTimer = setInterval(() => {
    if (!S.busy) { stopSpin(); return; }
    spinIdx++;
    paintSpin();
  }, SPIN_MS);
  if (spinTimer && typeof spinTimer.unref === 'function') spinTimer.unref();
}

function stopSpin() {
  if (spinTimer) { clearInterval(spinTimer); spinTimer = null; }
  spinIdx = 0;
}

export function paintIntent() {
  syncLiveChrome();
  const label = S.currentIntent || (S.busy ? 'reasoning' : '');
  const rail = document.getElementById('intent-rail');
  const top = document.getElementById('busy-spin');
  if (!label) {
    stopSpin();
    if (rail) rail.hidden = true;
    if (top) top.hidden = true;
    return;
  }
  let line = label;
  if (S.runStartedAt) {
    const secs = Math.floor((Date.now() - S.runStartedAt) / 1000);
    if (secs > 0) {
      line += ' · ' + (secs < 60 ? secs + 's' : Math.floor(secs / 60) + 'm' + (secs % 60) + 's');
    }
  }
  const q = (S.promptQueue || []).length;
  if (q) line += ' · ' + q + ' queued';
  const strip = planStripLabel();
  if (strip) line += '   ▸ ' + strip;
  if (rail) {
    rail.hidden = false;
    const text = rail.querySelector && rail.querySelector('.ir-text');
    if (text) text.textContent = line;
    else rail.textContent = line;
  }
  if (top) top.hidden = false;
  if (S.loadingEl) {
    const textEl = S.loadingEl.querySelector('.li-text');
    if (textEl) textEl.textContent = line;
  }
  startSpin();
}

// Bodek header instruments + busy-run plan strip. Chips stay visible
// when idle; the ▸ strip only rides the status line during a turn.
export function planStripLabel() {
  if (!S.busy || S.planAvail !== 'available') return '';
  const plan = S.plan;
  if (!plan || !plan.found) return '';
  const steps = plan.steps || [];
  if (!steps.length) return '';
  let done = 0, blocked = 0, active = '';
  steps.forEach((st) => {
    if (st.status === 'done') done++;
    else if (st.status === 'blocked') blocked++;
    else if (st.status === 'in_progress' && !active) active = String(st.title || '').replace(/\s+/g, ' ').trim();
  });
  let s = 'plan ' + done + '/' + steps.length;
  if (active) s += ' · ' + (active.length > 32 ? active.slice(0, 32) + '…' : active);
  if (blocked) s += ' · ⛔' + blocked;
  return s;
}

export function headerPlanLabel() {
  if (S.planAvail !== 'available') return '';
  const plan = S.plan;
  if (!plan || !plan.found) return '';
  const steps = plan.steps || [];
  if (!steps.length) return '';
  const done = steps.filter((st) => st.status === 'done').length;
  return 'plan ' + done + '/' + steps.length;
}

export function headerJobsLabel() {
  const jobs = S.jobs || [];
  let n = 0, failed = false;
  jobs.forEach((j) => {
    if (j.status === 'running') n++;
    if (j.status === 'failed' || j.status === 'timeout' || j.status === 'killed') failed = true;
  });
  if (n > 0) return '● ' + n + (n === 1 ? ' job' : ' jobs');
  if (failed) return '✗ job';
  return '';
}

export function syncLiveChrome() {
  const planChip = document.getElementById('plan-chip');
  if (planChip) {
    const lab = headerPlanLabel();
    planChip.hidden = !lab;
    planChip.textContent = lab;
  }
  const jobsChip = document.getElementById('jobs-chip');
  if (jobsChip) {
    const lab = headerJobsLabel();
    jobsChip.hidden = !lab;
    jobsChip.textContent = lab;
    if (jobsChip.classList && typeof jobsChip.classList.toggle === 'function') {
      jobsChip.classList.toggle('hot', lab.startsWith('✗'));
    }
  }
}

export function openTurn(event) {
  S.currentTurnId = (event && event.turn_id) || S.currentTurnId;
  S.currentTurnInitiated = (event && event.initiated) || 'operator';
  S.turnReceipt = { files: [], plus: 0, minus: 0, tests: '', tools: 0 };
}

export function markWakeTurn(event) {
  hideEmptyState();
  S.pendingWakeChip = (event && event.initiated === 'system') ? 'wake' : 'remote';
  S.currentTurnInitiated = (event && event.initiated) || 'system';
}

function assistantSender() {
  return S.pendingWakeChip ? '⬡ odek · wake' : '⬡ odek';
}

function attachWakeChip(wrapper) {
  if (!S.pendingWakeChip || !wrapper) return;
  wrapper.classList.add('wake-turn');
  const sender = wrapper.querySelector('.sender');
  if (sender) sender.textContent = '⬡ odek · wake';
  S.pendingWakeChip = null;
}

export function sealTurn(event) {
  const tid = event && event.turn_id;
  const last = (tid && messagesEl.querySelector('.msg.assistant[data-turn-id="' + tid + '"] .bubble'))
    || messagesEl.querySelector('.msg.assistant:last-child .bubble');
  if (!last || !S.turnReceipt) return;
  const line = formatReceipt(S.turnReceipt);
  if (!line) return;
  if (last.querySelector('.turn-receipt')) return;
  const rec = document.createElement('span');
  rec.className = 'turn-receipt';
  rec.textContent = line;
  const sender = last.querySelector('.sender');
  if (sender) sender.appendChild(rec);
  else last.appendChild(rec);
  void event;
}

// ── Inline Loading Indicator — Bodek status-line copy ──
export function showLoading() {
  if (S.loadingEl) {
    setIntent(S.currentIntent || 'reasoning');
    return;
  }
  const el = document.createElement('div');
  el.className = 'loading-indicator';
  el.innerHTML = '<span class="ir-spin" aria-hidden="true"></span><div class="li-text">reasoning</div>';
  messagesEl.appendChild(el);
  S.loadingEl = el;
  setIntent('reasoning');
  S.loadingTimer = setInterval(paintIntent, 1000);
  pruneMessages();
  forceScrollBottom();
}

export function hideLoading() {
  if (S.loadingEl) {
    S.loadingEl.remove();
    S.loadingEl = null;
  }
  if (S.loadingTimer) {
    clearInterval(S.loadingTimer);
    S.loadingTimer = null;
  }
  stopSpin();
}

// ── Live turn spine ──
// The model often emits answer tokens in the same LLM message as tool_calls
// (serve E2E: token → tool_call). token_delta would otherwise create the
// answer bubble first and a naive append would paint answer → tools.
// History already uses thinking → tools → answer; insertTurnWork keeps
// the live path on that same spine even when events race.
function hasClass(el, name) {
  if (!el) return false;
  if (el.classList && el.classList.contains) return el.classList.contains(name);
  return (' ' + (el.className || '') + ' ').indexOf(' ' + name + ' ') !== -1;
}

function liveAnswerEl() {
  const el = S.streamBubbleEl;
  return (el && el.parentNode === messagesEl) ? el : null;
}

function firstTurnOfKind(kindClass) {
  const kids = messagesEl.children || [];
  for (let i = 0; i < kids.length; i++) {
    const el = kids[i];
    if (!hasClass(el, kindClass)) continue;
    if (S.currentTurnId) {
      if (el.dataset && el.dataset.turnId === S.currentTurnId) return el;
      continue;
    }
    if (hasClass(el, 'live') || el === S.streamBubbleEl) return el;
  }
  return null;
}

function turnHasThinking(except) {
  const kids = messagesEl.children || [];
  for (let i = 0; i < kids.length; i++) {
    const el = kids[i];
    if (el === except) continue;
    if (!hasClass(el, 'thinking-block')) continue;
    if (S.currentTurnId) {
      if (el.dataset && el.dataset.turnId === S.currentTurnId) return true;
      continue;
    }
    if (hasClass(el, 'live')) return true;
  }
  return false;
}

function parkAnswerLast() {
  const answer = liveAnswerEl();
  if (answer) messagesEl.appendChild(answer);
}

export function insertTurnWork(el, kind) {
  if (S.currentTurnId && el.dataset) el.dataset.turnId = S.currentTurnId;
  hideLoading();
  if (kind === 'thinking' && !turnHasThinking(el)) {
    const firstTool = firstTurnOfKind('tool-block') || firstTurnOfKind('subagent-group');
    if (firstTool) {
      messagesEl.insertBefore(el, firstTool);
      parkAnswerLast();
      return;
    }
  }
  const answer = liveAnswerEl();
  if (answer) {
    messagesEl.insertBefore(el, answer);
    return;
  }
  messagesEl.appendChild(el);
}

// ── Thinking ──
export function streamThinking(content) {
  if (!S.thinkingContentEl) {
    // Remove cursor from any active stream
    removeStreamCursor();

    // Live reasoning stays collapsed (Bodek calm default). .live marks
    // this turn's block so the next prompt can leave history alone.
    const block = document.createElement('div');
    block.className = 'thinking-block live';
    block.innerHTML =
      '<div class="thinking-toggle" role="button" tabindex="0" aria-expanded="false">' +
        '<span class="arrow">▶</span> thinking' +
      '</div>' +
      '<div class="thinking-content">' + escapeHtml(content) + '</div>';
    insertTurnWork(block, 'thinking');

    S.thinkingContentEl = block.querySelector('.thinking-content');
    hideEmptyState();
    pruneMessages();
    scrollBottom();
  } else {
    S.thinkingContentEl.textContent += content;
    // Auto-follow the newest line while the block is open — but only when
    // it is open, so a user who collapses mid-turn isn't fought.
    if (S.thinkingContentEl.classList.contains('open')) {
      S.thinkingContentEl.scrollTop = S.thinkingContentEl.scrollHeight;
    }
    scrollBottom();
  }
}

function toggleThinking(el) {
  const arrow = el.querySelector('.arrow');
  const content = el.parentElement.querySelector('.thinking-content');
  if (content) {
    content.classList.toggle('open');
    arrow.classList.toggle('open');
    el.setAttribute('aria-expanded', content.classList.contains('open'));
    // Auto-open on first click
    if (content.classList.contains('open')) {
      scrollBottom();
    }
  }
}

export function endThinking() {
  S.thinkingContentEl = null;
}

// ── Streaming ──
export function streamToken(content) {
  S.streamBuffer += content;
  if (!S.streamRAF) {
    S.streamRAF = requestAnimationFrame(streamFlushRAF);
  }
}

function streamFlushRAF() {
  S.streamRAF = null;
  if (!S.streamBuffer) return;
  appendStreamText(S.streamBuffer);
  S.streamBuffer = '';
}

export function streamFlush() {
  if (S.streamRAF) {
    cancelAnimationFrame(S.streamRAF);
    S.streamRAF = null;
  }
  if (S.streamBuffer) {
    appendStreamText(S.streamBuffer);
    S.streamBuffer = '';
  }
}

function ensureStreamBubble() {
  if (!S.streamBubbleEl) {
    startStream();
  }
}

function appendStreamText(text) {
  ensureStreamBubble();
  // Accumulate and re-render the WHOLE answer so far. Fragments must never
  // be markdown-parsed individually: a chunk boundary mid-paragraph would
  // otherwise close one <p> and open another, splintering the answer into
  // one-line paragraphs. Re-rendering per rAF frame keeps live formatting
  // (fences, lists) correct while the answer streams.
  S.streamText += text;
  S.streamContentEl.innerHTML = markdownToHtml(S.streamText);
  if (S.streamCursorEl) {
    const host = streamCursorHost();
    if (S.streamCursorEl.parentNode !== host) host.appendChild(S.streamCursorEl);
  }
  scrollBottom();
}

// streamCursorHost walks to the deepest last element so the caret sits
// inline after the trailing text. Appending to the content root put the
// caret BELOW the last block — an open code fence rendered it on its own
// line, which then jumped when the fence closed (F-C4). Environments whose
// DOM shims lack lastElementChild degrade to the content root (old behavior).
function streamCursorHost() {
  let host = S.streamContentEl;
  let last = host.lastElementChild;
  while (last && last.tagName !== 'BR') {
    host = last;
    last = host.lastElementChild;
  }
  return host;
}

function removeStreamCursor() {
  if (S.streamCursorEl && S.streamCursorEl.parentNode) {
    S.streamCursorEl.remove();
  }
}

function startStream() {
  hideEmptyState();
  endThinking();
  hideLoading(); // remove the inline loading indicator — streaming has started

  const wrapper = document.createElement('div');
  wrapper.className = 'msg assistant';
  if (S.currentTurnId) wrapper.dataset.turnId = S.currentTurnId;
  wrapper.style.opacity = '1';
  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">' + assistantSender() + '</div>' +
      '<div class="content" id="stream-content"></div>' +
    '</div>';
  attachWakeChip(wrapper);
  messagesEl.appendChild(wrapper);

  S.streamText = '';
  S.streamCursorEl = document.createElement('span');
  S.streamCursorEl.className = 'stream-cursor';
  S.streamBubbleEl = wrapper;
  S.streamContentEl = wrapper.querySelector('#stream-content');
  S.streamContentEl.appendChild(S.streamCursorEl);
  compactOlderAnswers(wrapper);
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) addCopyButton(bubble);
  pruneMessages();
  scrollBottom();
}

export function endStream() {
  removeStreamCursor();
  // The live answer is the latest — never fold it. Older long replies
  // were already compacted when this bubble opened.
  S.streamBubbleEl = null;
  S.streamContentEl = null;
  S.streamText = '';
  S.streamCursorEl = null;
  S.currentToolBlock = null;
  S.subagentGroup = null;
  S.toolBlockQueues.clear();
  S.toolStartQueues.clear();
  S.inToolGroup = false;
  S.busy = false;
  hideLoading();
  hideCancel();
  sendBtn.disabled = !S.ws || S.ws.readyState !== WebSocket.OPEN;
  promptEl.disabled = false;
  promptEl.focus();
}

// ── Message rendering ──
export function addMessage(role, content) {
  hideEmptyState();
  const wrapper = document.createElement('div');
  wrapper.className = 'msg ' + role;

  const sender = role === 'user' ? '❯ you' : assistantSender();

  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">' + sender + '</div>' +
      '<div class="content">' + markdownToHtml(content) + '</div>' +
    '</div>';
  if (role === 'assistant') attachWakeChip(wrapper);
  messagesEl.appendChild(wrapper);
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) addCopyButton(bubble);
  if (role === 'assistant') compactOlderAnswers(wrapper);
  else if (bubble) checkCollapse(bubble, { latest: false });
  pruneMessages();
  scrollBottom();
}

export function addSystemMessage(content) {
  hideEmptyState();
  const el = document.createElement('div');
  el.className = 'msg system';
  el.innerHTML = '<div class="bubble"><div class="content">' + escapeHtml(content) + '</div></div>';
  messagesEl.appendChild(el);
  pruneMessages();
  scrollBottom();
}

export function addDivider(text) {
  const el = document.createElement('div');
  el.className = 'msg-divider';
  el.textContent = text || '•';
  messagesEl.appendChild(el);
  scrollBottom();
}

// Render a completed assistant message (not streaming) with copy button.
export function renderAssistantMessage(content) {
  hideEmptyState();
  const wrapper = document.createElement('div');
  wrapper.className = 'msg assistant';
  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">' + assistantSender() + '</div>' +
      '<div class="content">' + markdownToHtml(content) + '</div>' +
    '</div>';
  attachWakeChip(wrapper);
  messagesEl.appendChild(wrapper);
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) addCopyButton(bubble);
  compactOlderAnswers(wrapper);
  pruneMessages();
}

// ── Tool Helpers ──

// Matches Go's render.ToolEmoji for consistency. Exported so tests can pin
// the mirror (internal/render/render.go is the source of truth).
export function toolGlyph(name) {
  const n = String(name || '').toLowerCase();
  if (n.includes('shell') || n.includes('bash') || n.includes('exec')) return '❯';
  if (n.includes('write') || n.includes('patch') || n.includes('edit')) return '✎';
  if (n.includes('read')) return '◰';
  if (n.includes('list') || n.includes('dir') || n.includes('ls')) return '▤';
  if (n.includes('search') || n.includes('grep') || n.includes('find')) return '⌕';
  if (n.includes('browser') || n.includes('http') || n.includes('fetch') || n.includes('web')) return '◉';
  if (n.includes('delegate') || n.includes('subagent') || n.includes('task')) return '⑂';
  if (n.includes('memory') || n.includes('recall')) return '❖';
  if (n.includes('vision') || n.includes('image') || n.includes('transcribe')) return '◎';
  return '✦';
}

export function toolEmoji(name) {
  if (name === 'read_file' || name === 'write_file' || name === 'search_files' ||
      name === 'patch' || name === 'execute_code' || name === 'multi_grep') return '📝';
  if (name === 'shell' || name === 'terminal' || name === 'process') return '💻';
  if (name === 'web_search' || name === 'web_extract' || name.startsWith('browser_')) return '🌐';
  if (name === 'memory' || name === 'session_search') return '🧠';
  if (name === 'vision_analyze') return '👁️';
  if (name === 'send_message') return '💬';
  if (name === 'delegate_task' || name === 'delegate_tasks') return '👥';
  if (name === 'cronjob') return '⏰';
  // Planning — mirrors the Go renderer's `case name == "plan": return "📋"`.
  // The vestigial "todo" arm was retired there with no replacement: an
  // unknown tool falls through to the default 🔧, and so does "todo" here.
  if (name === 'plan') return '📋';
  if (name === 'skill_view' || name === 'skill_manage' ||
      name === 'skills_list' || name === 'clarify') return '➕';
  if (name === 'transcribe') return '🎙️';
  if (name === 'list_directory' || name === 'create_directory') return '📁';
  return '🔧';
}

// Extract a short human-readable preview from tool args JSON.
function buildToolPreview(name, data) {
  if (!data) return '';
  try {
    const obj = JSON.parse(data);
    switch (name) {
      case 'read_file':    return String(obj.path || '').slice(0, 60);
      case 'write_file':   return String(obj.path || '').slice(0, 60);
      case 'search_files': return (obj.pattern || obj.query || '').slice(0, 50);
      case 'multi_grep':   return (obj.pattern || '').slice(0, 50);
      case 'shell':        return (obj.command || '').slice(0, 60);
      case 'browser_navigate': case 'web_extract': return (obj.url || '').slice(0, 60);
      case 'web_search':   return (obj.query || '').slice(0, 60);
      default: {
        const first = Object.values(obj)[0];
        return first != null ? String(first).slice(0, 50) : '';
      }
    }
  } catch { return ''; }
}

// Format tool args for the expanded body — pretty-print JSON or show raw.
function formatToolArgs(data) {
  if (!data) return '';
  try {
    const obj = JSON.parse(data);
    return Object.entries(obj).map(([k, v]) => {
      const val = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
      return k + ': ' + (val.length > 300 ? val.slice(0, 300) + '…' : val);
    }).join('\n');
  } catch {
    return data.length > 500 ? data.slice(0, 500) + '…' : data;
  }
}

// ── Tool Calls ──
export function addToolCall(name, data) {
  removeStreamCursor();
  S.inToolGroup = true;

  const preview = buildToolPreview(name, data);

  const el = document.createElement('div');
  el.className = 'tool-block';
  el.innerHTML =
    '<div class="tb-header" role="button" tabindex="0" aria-expanded="false">' +
      '<span class="arrow">▶</span>' +
      ' <span class="tb-status">▸</span>' +
      ' <span class="tb-emoji">' + toolGlyph(name) + '</span>' +
      ' <span class="tb-name">' + escapeHtml(name) + '</span>' +
      (preview ? ' <span class="tb-preview">' + escapeHtml(preview) + '</span>' : '') +
      ' <span class="tb-spinner running"></span>' +
      ' <span class="tb-latency"></span>' +
    '</div>' +
    '<div class="tb-body">' + escapeHtml(formatToolArgs(data)) + '</div>';

  insertTurnWork(el, 'tool');
  S.currentToolBlock = el;
  teach('steps', 'tip: click a tool head to expand its output · thinking stays folded');

  // Push into per-name FIFO queues so parallel results route correctly.
  if (!S.toolBlockQueues.has(name)) S.toolBlockQueues.set(name, []);
  S.toolBlockQueues.get(name).push(el);
  if (!S.toolStartQueues.has(name)) S.toolStartQueues.set(name, []);
  S.toolStartQueues.get(name).push(performance.now());

  pruneMessages();
  scrollBottom();
}

// appendToolResultContent renders a tool result into the collapsed .tb-body
// (same toggle as args), truncating long output behind a "show all" expander.
// Shared by the live path (addToolResult) and session-history rendering.
function appendToolResultContent(block, output) {
  const resultEl = document.createElement('div');
  resultEl.className = 'tb-result';
  const body = block.querySelector('.tb-body');
  (body || block).appendChild(resultEl);
  fillToolResult(resultEl, output || '', true);
}

// fillToolResult renders tool output into resultEl. The server sends raw,
// unsanitized content; tool output may embed the model-facing
// <untrusted_content_*> envelope, which is unwrapped for display — the body
// is inserted as text (never HTML). Envelope source is model-facing trust
// metadata and is not shown. When truncate is true, long bodies are cut
// behind a "show all" expander carrying the full output.
function fillToolResult(resultEl, output, truncate) {
  const MAX_RESULT = 600;
  const segments = parseUntrusted(output);
  for (const seg of segments) {
    const body = seg.body;
    if (truncate && body.length > MAX_RESULT) {
      resultEl.appendChild(document.createTextNode(body.slice(0, MAX_RESULT)));
      const more = document.createElement('span');
      more.className = 'tb-result-more';
      more.setAttribute('role', 'button');
      more.tabIndex = 0;
      more.dataset.full = output;
      more.textContent = ' …show all (' + body.length + ' chars)';
      resultEl.appendChild(more);
    } else {
      resultEl.appendChild(document.createTextNode(body));
    }
  }
}

export function addToolResult(name, output) {
  // Route to the matching pending block via FIFO queue.
  const queue = S.toolBlockQueues.get(name);
  const block = (queue && queue.length > 0) ? queue.shift() : S.currentToolBlock;
  if (!block) return;

  // Remove spinner; show latency.
  const spinner = block.querySelector('.tb-spinner');
  if (spinner) spinner.classList.remove('running');
  const startQueue = S.toolStartQueues.get(name);
  if (startQueue && startQueue.length > 0) {
    const start = startQueue.shift();
    const ms = performance.now() - start;
    const latEl = block.querySelector('.tb-latency');
    if (latEl) latEl.textContent = ms < 1000 ? Math.round(ms) + 'ms' : (ms / 1000).toFixed(1) + 's';
  }

  const status = block.querySelector('.tb-status');
  if (status) {
    const failed = /(?:^|\n)(?:error|failed|denied|fatal)[:\s]/i.test(output || '');
    status.textContent = failed ? '✗' : '✓';
    status.classList.toggle('err', failed);
    status.classList.toggle('ok', !failed);
  }

  appendToolResultContent(block, output || '');

  const chips = classifyToolResult(name, prettyToolBody(output || ''));
  if (chips.length) {
    const host = block.querySelector('.tb-header');
    if (host && !host.querySelector('.tb-chip')) {
      const wrap = document.createElement('span');
      wrap.className = 'tb-chips';
      wrap.innerHTML = chipsHtml(chips);
      host.appendChild(wrap);
    }
  }
  if (S.turnReceipt) {
    const piece = collectReceipt(name, '', output || '');
    S.turnReceipt.tools = (S.turnReceipt.tools || 0) + 1;
    S.turnReceipt.plus += piece.plus;
    S.turnReceipt.minus += piece.minus;
    if (piece.tests) S.turnReceipt.tests = piece.tests;
    S.turnReceipt.files.push(...piece.files);
  }
  scrollBottom();
}

function expandToolResult(el) {
  const full = el.dataset.full || '';
  const resultEl = el.parentElement;
  if (resultEl) {
    resultEl.textContent = '';
    fillToolResult(resultEl, full, false);
  }
}

function toggleToolBody(header) {
  const arrow = header.querySelector('.arrow');
  const body = header.parentElement.querySelector('.tb-body');
  if (body) {
    body.classList.toggle('open');
    arrow.classList.toggle('open');
    header.setAttribute('aria-expanded', body.classList.contains('open'));
  }
}

// ── Sub-agent swarm (Bodek transcript) ──
// Same spine as a tool step: ▶ ▸ ⑂ delegate_tasks · 1/2 agents
// then an always-on chip strip (⟳ SA1 <goal|tool>). Details sit behind
// the chip / head, with a ⎿ tree — not a bordered card grid.

function saChipLabel(idx, text) {
  const body = String(text || '').trim() || ('Task ' + (idx + 1));
  return 'SA' + (idx + 1) + ' ' + body;
}

function saFinishGlyph(status) {
  if (status === 'cancelled') return '⊘';
  if (status === 'timeout') return '⏱';
  if (status === 'partial' || status === 'budget_exhausted') return '◐';
  if (status && status !== 'success') return '✗';
  return '✓';
}

function subagentHeadHTML(n) {
  return '<div class="sg-header" role="button" tabindex="0" aria-expanded="false">' +
    '<span class="arrow">▶</span>' +
    ' <span class="tb-status">▸</span>' +
    ' <span class="tb-emoji">⑂</span>' +
    ' <span class="tb-name">delegate_tasks</span>' +
    ' <span class="sg-rollup">0/' + n + ' agents</span>' +
  '</div>';
}

function subagentCardHTML(i, goal, withStop) {
  const title = goal || ('Task ' + (i + 1));
  return '<div class="sa-top" role="button" tabindex="0" aria-expanded="false">' +
      '<div class="sa-icon">⟳</div>' +
      '<div class="sa-goal" title="' + escapeAttr(title) + '">' + escapeHtml(saChipLabel(i, title)) + '</div>' +
      '<div class="sa-status">running</div>' +
      (withStop ? '<button class="sa-stop" title="Stop this sub-agent">■</button>' : '') +
    '</div>' +
    '<div class="sa-details">' +
      '<div class="sa-meta"></div>' +
      '<div class="sa-summary"></div>' +
      '<div class="sa-files"></div>' +
    '</div>';
}

export function addSubagentGroup(command) {
  removeStreamCursor();
  if (S.subagentGroup) return; // only one group at a time

  let tasks = [];
  try {
    const parsed = JSON.parse(command);
    tasks = parsed.tasks || [];
  } catch { tasks = []; }

  const group = document.createElement('div');
  group.className = 'subagent-group';
  group.innerHTML = subagentHeadHTML(tasks.length) + '<div class="subagent-grid" id="sa-grid"></div>';
  insertTurnWork(group, 'tool');
  S.subagentGroup = group;
  teach('swarm', 'tip: click a chip for the agent log · inspector Now lists every agent');

  const grid = group.querySelector('#sa-grid');
  tasks.forEach((task, i) => {
    const card = document.createElement('div');
    card.className = 'subagent-card running';
    card.dataset.index = i;
    card.dataset.goal = task.goal || '';
    card.innerHTML = subagentCardHTML(i, task.goal, true);
    // Disarmed until the subagent_state started record delivers the
    // task_id — the task may still be queued behind the concurrency
    // semaphore, in which case there is nothing to cancel yet.
    const stopBtn = card.querySelector('.sa-stop');
    if (stopBtn) stopBtn.disabled = true;
    grid.appendChild(card);
  });

  pruneMessages();
  scrollBottom();
}

// parseSubagentResults parses delegate_tasks output text of the form
// "📋 Sub-agent results:\n\n─── Task 1: goal ───\n{json}\n\n─── Task 2: ..."
// into a map of task index → parsed result object.
function parseSubagentResults(output) {
  const lines = (output || '').split('\n');
  let currentTaskIdx = -1;
  const taskResults = {};

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const taskMatch = line.match(/─── Task (\d+):/);
    if (taskMatch) {
      currentTaskIdx = parseInt(taskMatch[1]) - 1;
      // Collect JSON from subsequent lines
      const jsonLines = [];
      for (let j = i + 1; j < lines.length; j++) {
        const nextLine = lines[j];
        if (nextLine.startsWith('─── Task ')) break;
        if (nextLine.startsWith('📋')) continue;
        jsonLines.push(nextLine);
      }
      const jsonStr = jsonLines.join('\n').trim();
      try {
        taskResults[currentTaskIdx] = JSON.parse(jsonStr);
      } catch {
        taskResults[currentTaskIdx] = { summary: jsonStr };
      }
    }
  }
  return taskResults;
}

// finalizeSubagentCard marks a card done/error and fills its details from
// the parsed result. Shared by the live path (completeSubagents) and
// session-history rendering.
function finalizeSubagentCard(card, result, keepStatus = false) {
  const stopBtn = card.querySelector('.sa-stop');
  if (stopBtn) stopBtn.remove();
  if (!keepStatus) {
    card.querySelector('.sa-icon').textContent = saFinishGlyph(result && result.status);
    card.classList.remove('running');
    card.querySelector('.sa-status').textContent = 'done';
  }
  card.dataset.finalized = '1';

  if (!result) {
    card.classList.add('completed');
    return;
  }

  const status = result.status || 'success';
  if (!keepStatus) {
    if (status === 'error') {
      card.classList.add('error');
      card.querySelector('.sa-icon').textContent = saFinishGlyph(status);
      card.querySelector('.sa-status').textContent = 'error';
    } else if (status === 'cancelled') {
      card.classList.add('stopped');
      card.querySelector('.sa-icon').textContent = saFinishGlyph(status);
      card.querySelector('.sa-status').textContent = 'stopped';
    } else {
      card.classList.add('completed');
      card.querySelector('.sa-icon').textContent = saFinishGlyph(status);
    }
  } else if (status === 'error') {
    card.classList.add('error');
  } else if (status === 'cancelled' || card.classList.contains('stopped')) {
    card.classList.add('stopped');
  } else {
    card.classList.add('completed');
  }

  const details = card.querySelector('.sa-details');
  const summary = result.summary || '';
  const files = result.files_changed || [];
  const tokens = result.tokens_used || 0;
  const iters = result.iterations || 0;

  if (summary || files.length > 0 || tokens || iters) {
    const meta = details.querySelector('.sa-meta');
    if (tokens) {
      card.dataset.tokens = String(tokens);
      meta.textContent = tokens + ' tok' + (iters ? ' · ' + iters + ' it' : '');
    } else if (iters) {
      meta.textContent = iters + ' it';
    }

    const summaryEl = details.querySelector('.sa-summary');
    summaryEl.textContent = typeof summary === 'string' ? summary : '';

    if (files.length > 0) {
      const filesEl = details.querySelector('.sa-files');
      filesEl.innerHTML = files.map(f => '<span class="file-chip">' + escapeHtml(f) + '</span>').join('');
    }

    // Bodek: details stay collapsed unless the agent failed.
    if (status === 'error') details.classList.add('open');
  }
}

export function completeSubagents(output) {
  if (!S.subagentGroup) return;

  const taskResults = parseSubagentResults(output);
  const cards = S.subagentGroup.querySelectorAll('.subagent-card');
  cards.forEach((card, i) => {
    // Cards already finalized by a subagent_state finished transition keep
    // their pill/status — but still get the full result details (summary,
    // files, tokens) which only the batch result carries.
    finalizeSubagentCard(card, taskResults[i], card.dataset.finalized === '1');
  });
  refreshSubagentHead(S.subagentGroup);

  pruneMessages();
  scrollBottom();
}

// updateSubagentState applies a server subagent_state transition to the
// matching card: started → running, active → live tool/step, finished →
// final pill (✓ done / ✗ failed by status). Correlated by task_idx — the
// card's DOM position matches the delegated task order.
export function updateSubagentState(ev) {
  if (!S.subagentGroup) return;
  const cards = S.subagentGroup.querySelectorAll('.subagent-card');
  const card = cards[ev.task_idx];
  if (!card || card.dataset.finalized === '1') return;

  const statusEl = card.querySelector('.sa-status');
  const details = card.querySelector('.sa-details');
  const meta = details ? details.querySelector('.sa-meta') : null;

  // Remember the correlation id so the per-card stop button can target
  // this sub-agent; the button stays disarmed until it arrives.
  if (ev.task_id) card.dataset.taskId = ev.task_id;
  const armStop = () => {
    const stopBtn = card.querySelector('.sa-stop');
    if (stopBtn) stopBtn.disabled = !card.dataset.taskId || card.dataset.stopping === '1';
  };

  switch (ev.phase) {
    case 'started':
      statusEl.textContent = 'running';
      armStop();
      break;
    case 'active': {
      statusEl.textContent = '⟳ ' + (ev.tool || 'working') + (ev.step ? ' · ' + ev.step : '');
      const goalEl = card.querySelector('.sa-goal');
      if (goalEl && ev.tool) goalEl.textContent = saChipLabel(ev.task_idx, ev.tool);
      if (meta && ev.step) meta.textContent = 'step ' + ev.step + (ev.tool ? ' · ' + ev.tool : '');
      armStop();
      break;
    }
    case 'finished': {
      card.dataset.finalized = '1';
      card.classList.remove('running');
      const stopBtn = card.querySelector('.sa-stop');
      if (stopBtn) stopBtn.remove();
      const cancelled = ev.status === 'cancelled';
      const failed = !cancelled && ev.status && ev.status !== 'success' && ev.status !== 'partial';
      card.querySelector('.sa-icon').textContent = saFinishGlyph(ev.status);
      card.classList.add(cancelled ? 'stopped' : (failed ? 'error' : 'completed'));
      statusEl.textContent = cancelled ? 'stopped'
        : failed ? (ev.status || 'failed')
        : (ev.status === 'partial' ? 'partial' : 'done');
      if (ev.tokens_used) card.dataset.tokens = String(ev.tokens_used);
      if (meta) {
        const parts = [];
        if (ev.step) parts.push('step ' + ev.step);
        if (ev.tool) parts.push(ev.tool);
        if (ev.iterations) parts.push(ev.iterations + ' it');
        if (ev.tokens_used) parts.push(ev.tokens_used + ' tok');
        if (ev.duration_seconds) parts.push(ev.duration_seconds.toFixed(1) + 's');
        meta.textContent = parts.join(' · ');
      }
      if (failed) {
        const d = card.querySelector('.sa-details');
        if (d) d.classList.add('open');
        syncSubagentArrow(S.subagentGroup);
      }
      updateSubagentHeader();
      break;
    }
  }
}

// requestSubagentStop asks the server to cancel ONE running sub-agent
// (per-card stop button). The transport lives behind S.onSubagentStop
// (wired in main.js, which owns socket + session-token access) so render
// stays transport-free. Idempotent: no-op on finalized cards, cards
// without a known task_id, or repeat clicks.
export function requestSubagentStop(taskIdx) {
  if (!S.subagentGroup || !S.onSubagentStop) return;
  const card = S.subagentGroup.querySelectorAll('.subagent-card')[taskIdx];
  if (!card || card.dataset.finalized === '1' || card.dataset.stopping === '1') return;
  const taskID = card.dataset.taskId;
  if (!taskID) return;
  card.dataset.stopping = '1';
  const stopBtn = card.querySelector('.sa-stop');
  if (stopBtn) stopBtn.disabled = true;
  const statusEl = card.querySelector('.sa-status');
  if (statusEl) statusEl.textContent = 'stopping…';
  S.onSubagentStop(taskID);
}

// refreshSubagentHead writes Bodek's agentRollup onto the swarm head:
// "1/2 agents · 1 ✗ · 3.2k tok". Live/pending keep a static ▸ — the
// status rail is the only spinner.
function refreshSubagentHead(group) {
  if (!group) return;
  const cards = group.querySelectorAll('.subagent-card');
  if (!cards.length) return;
  let done = 0, failed = 0, tokens = 0, live = 0;
  cards.forEach(c => {
    const finished = c.dataset.finalized === '1'
      || c.classList.contains('completed')
      || c.classList.contains('error')
      || c.classList.contains('stopped');
    if (finished) done++;
    else live++;
    if (c.classList.contains('error') || c.classList.contains('stopped')) failed++;
    tokens += parseInt(c.dataset.tokens || '0', 10) || 0;
  });
  const parts = [done + '/' + cards.length + ' agents'];
  if (failed) parts.push(failed + ' ✗');
  if (tokens) parts.push(tokens >= 1000 ? (tokens / 1000).toFixed(1).replace(/\.0$/, '') + 'k tok' : tokens + ' tok');
  const rollup = group.querySelector('.sg-rollup');
  if (rollup) rollup.textContent = parts.join(' · ');
  const mark = group.querySelector('.sg-header .tb-status');
  if (mark) {
    mark.textContent = live ? '▸' : (failed ? '✗' : '✓');
    mark.classList.toggle('ok', !live && !failed);
    mark.classList.toggle('err', !live && !!failed);
  }
}

function updateSubagentHeader() {
  refreshSubagentHead(S.subagentGroup);
}

function toggleSaDetails(el) {
  el.classList.toggle('open');
  // Mirror state for keyboard/AT users (the chip row is role="button").
  const card = el.closest('.subagent-card');
  const top = card && card.querySelector('.sa-top');
  if (top && top.setAttribute) top.setAttribute('aria-expanded', el.classList.contains('open') ? 'true' : 'false');
  syncSubagentArrow(card && card.closest('.subagent-group'));
}

function toggleSubagentGroup(header) {
  const group = header.closest('.subagent-group');
  if (!group) return;
  const details = group.querySelectorAll('.sa-details');
  let anyClosed = false;
  details.forEach(d => { if (!d.classList.contains('open')) anyClosed = true; });
  details.forEach(d => {
    if (anyClosed) d.classList.add('open');
    else d.classList.remove('open');
    const card = d.closest('.subagent-card');
    const top = card && card.querySelector('.sa-top');
    if (top && top.setAttribute) top.setAttribute('aria-expanded', d.classList.contains('open') ? 'true' : 'false');
  });
  syncSubagentArrow(group);
}

function syncSubagentArrow(group) {
  if (!group) return;
  const header = group.querySelector('.sg-header');
  if (!header) return;
  let anyOpen = false;
  group.querySelectorAll('.sa-details').forEach(d => {
    if (d.classList.contains('open')) anyOpen = true;
  });
  const arrow = header.querySelector('.arrow');
  if (arrow) arrow.classList.toggle('open', anyOpen);
  if (header.setAttribute) header.setAttribute('aria-expanded', anyOpen ? 'true' : 'false');
}

export function appendSubagentLog(taskIdx, event) {
  if (!S.subagentGroup) return;
  const cards = S.subagentGroup.querySelectorAll('.subagent-card');
  const card = cards[taskIdx];
  if (!card) return;

  // Ensure details are open
  const details = card.querySelector('.sa-details');
  if (!details) return;
  const summaryEl = details.querySelector('.sa-summary');

  // Bodek log grammar: glyph + name on calls, ⎿ excerpt on results.
  // Details stay collapsed until the operator opens a chip — a live swarm
  // must not strobe the transcript open on every tool beat.
  let text = '';
  if (event.event === 'tool_call') {
    text = toolGlyph(event.name) + ' ' + (event.name || 'tool') +
      (event.data ? ' · ' + truncateStr(event.data, 60) : '');
  } else if (event.event === 'tool_result') {
    text = '⎿ ' + truncateStr(event.data || '', 100);
  }
  if (!text) return;

  let logContainer = card.querySelector('.sa-log');
  if (!logContainer) {
    logContainer = document.createElement('div');
    logContainer.className = 'sa-log';
    details.insertBefore(logContainer, summaryEl);
  }
  const lineEl = document.createElement('div');
  lineEl.className = 'log-line';
  lineEl.textContent = text;
  logContainer.appendChild(lineEl);
}

// ── Session history rendering ──
// Renders the full persisted transcript on session load: user/assistant
// text, thinking (reasoning_content), tool calls with their results, and
// delegate_tasks sub-agent groups. Previously only user/assistant text was
// re-rendered, so a reloaded session silently dropped most of what happened.
export function renderSessionHistory(messages) {
  // Index tool results by call id for matching against assistant tool_calls.
  const resultsById = new Map();
  messages.forEach(m => {
    if (m.role === 'tool' && m.tool_call_id) resultsById.set(m.tool_call_id, m.content || '');
  });

  messages.forEach(msg => {
    if (msg.role === 'user') {
      addMessage('user', stripAttachmentBodies(msg.content || ''));
      return;
    }
    if (msg.role !== 'assistant') return; // skip system/tool internals

    if (msg.reasoning_content) renderHistoricalThinking(msg.reasoning_content);

    const toolCalls = Array.isArray(msg.tool_calls) ? msg.tool_calls : [];
    if (toolCalls.length > 0) {
      toolCalls.forEach(tc => {
        const name = (tc.function && tc.function.name) || 'tool';
        const args = (tc.function && tc.function.arguments) || '';
        const result = resultsById.get(tc.id) || '';
        if (name === 'delegate_tasks') {
          renderHistoricalSubagents(args, result);
        } else {
          renderHistoricalToolBlock(name, args, result);
        }
      });
    }
    if (msg.content) {
      renderAssistantMessage(msg.content);
    }
  });
}

// renderHistoricalThinking renders a completed reasoning block (collapsed).
function renderHistoricalThinking(content) {
  const block = document.createElement('div');
  block.className = 'thinking-block';
  block.innerHTML =
    '<div class="thinking-toggle" role="button" tabindex="0" aria-expanded="false">' +
        '<span class="arrow">▶</span> thinking' +
    '</div>' +
    '<div class="thinking-content">' + escapeHtml(content) + '</div>';
  messagesEl.appendChild(block);
}

// renderHistoricalToolBlock renders a completed tool call with its result
// (no spinner, no latency — those are live-turn concerns).
function renderHistoricalToolBlock(name, args, result) {
  const preview = buildToolPreview(name, args);
  const el = document.createElement('div');
  el.className = 'tool-block';
  el.innerHTML =
    '<div class="tb-header" role="button" tabindex="0" aria-expanded="false">' +
      '<span class="arrow">▶</span>' +
      ' <span class="tb-status ok">✓</span>' +
      ' <span class="tb-emoji">' + toolGlyph(name) + '</span>' +
      ' <span class="tb-name">' + escapeHtml(name) + '</span>' +
      (preview ? ' <span class="tb-preview">' + escapeHtml(preview) + '</span>' : '') +
    '</div>' +
    '<div class="tb-body">' + escapeHtml(formatToolArgs(args)) + '</div>';
  messagesEl.appendChild(el);
  if (result) appendToolResultContent(el, result);
}

// renderHistoricalSubagents renders a completed delegate_tasks group with
// per-task final states parsed from the tool result.
function renderHistoricalSubagents(args, output) {
  let tasks = [];
  try { tasks = JSON.parse(args).tasks || []; } catch { tasks = []; }
  const taskResults = parseSubagentResults(output);

  const group = document.createElement('div');
  group.className = 'subagent-group';
  group.innerHTML = subagentHeadHTML(tasks.length) + '<div class="subagent-grid"></div>';
  messagesEl.appendChild(group);
  const grid = group.querySelector('.subagent-grid');

  tasks.forEach((task, i) => {
    const card = document.createElement('div');
    card.className = 'subagent-card running';
    card.dataset.index = i;
    card.dataset.goal = task.goal || '';
    card.innerHTML = subagentCardHTML(i, task.goal, false);
    grid.appendChild(card);
    finalizeSubagentCard(card, taskResults[i]);
  });
  refreshSubagentHead(group);
}

// ── Collapse long messages ──
// Must match the max-height of .bubble.collapsible in style.css.
export const COLLAPSE_MAX_HEIGHT_PX = 460;

function resolveBubble(el) {
  if (!el) return null;
  return el.classList.contains('bubble') ? el : el.querySelector('.bubble');
}

function nodeHolds(parent, node) {
  if (!parent || !node) return false;
  if (parent === node) return true;
  if (parent.contains) return parent.contains(node);
  let el = node;
  while (el) {
    if (el === parent) return true;
    el = el.parentNode;
  }
  return false;
}

function assistantMessages() {
  const out = [];
  const kids = messagesEl.children || [];
  for (let i = 0; i < kids.length; i++) {
    const el = kids[i];
    if (hasClass(el, 'msg') && hasClass(el, 'assistant')) out.push(el);
  }
  return out;
}

function releaseCollapse(bubble) {
  if (!bubble) return;
  bubble.classList.remove('collapsible', 'expanded');
  const existing = bubble.querySelector('.collapse-toggle');
  if (existing) existing.remove();
}

function setToggleLabel(toggle, open) {
  toggle.textContent = open ? 'Show less ↑' : 'Show more ↓';
  toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
}

function toggleCollapse(el) {
  const bubble = resolveBubble(el.closest('.bubble') || el.closest('.msg'));
  if (!bubble) return;
  const open = bubble.classList.toggle('expanded');
  setToggleLabel(el, open);
  if (!open) {
    const msg = bubble.closest('.msg') || bubble;
    if (msg.scrollIntoView) msg.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }
}

// checkCollapse folds a bubble that overflows the threshold. The latest
// assistant reply is never folded — pass { latest: true } or let
// compactOlderAnswers skip the keep node.
function checkCollapse(host, opts) {
  const bubble = resolveBubble(host);
  if (!bubble) return;
  if (opts && opts.latest) {
    releaseCollapse(bubble);
    return;
  }
  const content = bubble.querySelector('.content');
  if (!content) return;
  const existing = bubble.querySelector('.collapse-toggle');
  if (content.scrollHeight <= COLLAPSE_MAX_HEIGHT_PX) {
    releaseCollapse(bubble);
    return;
  }
  bubble.classList.add('collapsible');
  const open = bubble.classList.contains('expanded');
  if (existing) {
    setToggleLabel(existing, open);
    return;
  }
  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.className = 'collapse-toggle';
  setToggleLabel(toggle, false);
  bubble.appendChild(toggle);
}

// compactOlderAnswers folds every assistant bubble except `keep` (the
// current latest). Call when a new assistant reply opens so history
// shrinks and the live answer stays full-height.
export function compactOlderAnswers(keep) {
  assistantMessages().forEach((msg) => {
    if (keep && (msg === keep || nodeHolds(msg, keep))) return;
    const bubble = msg.querySelector('.bubble');
    if (bubble) checkCollapse(bubble, { latest: false });
  });
}

// ── Copy message / code ──
function copyMessage(btn, content) {
  if (!content) {
    const bubble = btn.closest('.bubble');
    if (bubble) {
      const contentEl = bubble.querySelector('.content');
      content = contentEl ? contentEl.textContent : '';
    }
  }
  if (!content) return;
  copyTextToClipboard(content).then(() => {
    btn.classList.add('copied');
    btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg> Copied';
    setTimeout(() => {
      btn.classList.remove('copied');
      btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
    }, 2000);
  });
}

function copyCode(el) {
  const block = el.closest('.code-block');
  if (!block) return;
  const code = block.querySelector('pre code');
  if (!code) return;
  copyTextToClipboard(code.textContent).then(() => {
    el.textContent = '✓ copied';
    el.classList.add('copied');
    setTimeout(() => {
      el.textContent = '📋 copy';
      el.classList.remove('copied');
    }, 2000);
  });
}

function addCopyButton(bubble) {
  if (bubble.querySelector('.copy-btn')) return;
  const btn = document.createElement('button');
  btn.className = 'copy-btn';
  btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
  btn.title = 'Copy message';
  bubble.appendChild(btn);
}

// ── Click delegation for generated content ──
// All interactive elements inside #messages are handled here — no inline
// onclick attributes anywhere in generated HTML.
messagesEl.addEventListener('click', (e) => {
  const t = e.target;

  const tbResultMore = t.closest('.tb-result-more');
  if (tbResultMore) { expandToolResult(tbResultMore); return; }

  const cbCopy = t.closest('.cb-copy');
  if (cbCopy) { copyCode(cbCopy); return; }

  const copyBtn = t.closest('.copy-btn');
  if (copyBtn) { copyMessage(copyBtn); return; }

  const collapseToggle = t.closest('.collapse-toggle');
  if (collapseToggle) { toggleCollapse(collapseToggle); return; }

  const tbHeader = t.closest('.tb-header');
  if (tbHeader) { toggleToolBody(tbHeader); return; }

  const thinkingToggle = t.closest('.thinking-toggle');
  if (thinkingToggle) { toggleThinking(thinkingToggle); return; }

  const sgHeader = t.closest('.sg-header');
  if (sgHeader) { toggleSubagentGroup(sgHeader); return; }

  // F-C7: toggle from the chip row only — .sa-details is a content
  // region; click-anywhere-to-collapse destroyed text selection and made
  // .sa-log scrolling self-closing. The stop button never toggles.
  const saTop = t.closest('.sa-top');
  if (saTop && !t.closest('.sa-stop')) {
    const card = saTop.closest('.subagent-card');
    const details = card && card.querySelector('.sa-details');
    if (details) toggleSaDetails(details);
    return;
  }

  // F-C1: the per-card stop button. Cards carry their delegation index
  // (dataset.index, set at render); requestSubagentStop owns all the
  // guards (finalized / no task_id / repeat clicks) and the stopping… UI.
  const saStop = t.closest('.sa-stop');
  if (saStop) {
    const card = saStop.closest('.subagent-card');
    if (card) requestSubagentStop(parseInt(card.dataset.index, 10));
    return;
  }
});

// Keyboard activation for the role="button" elements above: Enter/Space
// re-dispatches as a click so the same delegation handles it.
messagesEl.addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const btn = e.target.closest('[role="button"]');
  if (!btn || !messagesEl.contains(btn)) return;
  e.preventDefault();
  btn.click();
});
