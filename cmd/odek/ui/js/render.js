// Message rendering: streaming, thinking, tool blocks, sub-agent cards,
// session-history rendering, collapse/copy affordances, and the loading
// indicator. Imports only from state/dom/utils/markdown/untrusted.
import { S } from './state.js';
import { messagesEl, promptEl, sendBtn, emptyState } from './dom.js';
import {
  escapeHtml, escapeAttr, truncateStr, copyTextToClipboard,
  pruneMessages, scrollBottom, forceScrollBottom, stripAttachmentBodies,
  showCancel, hideCancel, announce,
} from './utils.js';
import { markdownToHtml } from './markdown.js';
import { parseUntrusted } from './untrusted.js';

// ── Turn state ──
// resetTurnState clears all per-turn streaming/tool/sub-agent state. Called
// before a new turn (send), on new session, and when loading a session.
export function resetTurnState() {
  // The finished turn's reasoning blocks were auto-expanded while it ran;
  // collapse them now (next prompt or session switch). Only blocks this
  // renderer opened itself (.live) are touched.
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

// ── Inline Loading Indicator — typographic, no emoji ──
const loadingMessages = [
  'thinking',
  'reasoning',
  'considering',
  'planning',
  'tracing',
  'searching',
  'composing',
];

export function showLoading() {
  const el = document.createElement('div');
  el.className = 'loading-indicator';
  el.innerHTML = '<div class="li-dots"><span></span></div><div class="li-text">thinking</div>';
  // Insert after the last message (the user message we just added)
  messagesEl.appendChild(el);
  S.loadingEl = el;
  // Cycle messages with live elapsed time and iteration count.
  let idx = 0;
  S.loadingTimer = setInterval(() => {
    if (!S.loadingEl) return;
    const textEl = S.loadingEl.querySelector('.li-text');
    if (!textEl) return;
    let text = loadingMessages[idx % loadingMessages.length];
    idx++;
    if (S.runStartedAt) {
      const secs = Math.floor((Date.now() - S.runStartedAt) / 1000);
      text += ' · ' + (secs < 60 ? secs + 's' : Math.floor(secs / 60) + 'm' + (secs % 60) + 's');
    }
    if (S.runIterations > 0) text += ' · iter ' + S.runIterations;
    textEl.textContent = text;
  }, 1000);
  pruneMessages();
  // Force scroll to show the indicator (user just sent — they're at bottom)
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
}

// ── Thinking ──
export function streamThinking(content) {
  if (!S.thinkingContentEl) {
    // Remove cursor from any active stream
    removeStreamCursor();

    // Live reasoning opens expanded (marked .live so the next turn can
    // collapse exactly the blocks it auto-opened — manually opened
    // historical blocks are never touched).
    const block = document.createElement('div');
    block.className = 'thinking-block live';
    block.innerHTML =
      '<div class="thinking-toggle" role="button" tabindex="0" aria-expanded="true">' +
        '<span class="arrow open">▶</span> reasoning' +
      '</div>' +
      '<div class="thinking-content open">' + escapeHtml(content) + '</div>';
    messagesEl.appendChild(block);

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
  if (S.streamCursorEl && S.streamCursorEl.parentNode !== S.streamContentEl) {
    S.streamContentEl.appendChild(S.streamCursorEl);
  }
  scrollBottom();
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
  wrapper.style.opacity = '1';
  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">assistant</div>' +
      '<div class="content" id="stream-content"></div>' +
    '</div>';
  messagesEl.appendChild(wrapper);

  S.streamText = '';
  S.streamCursorEl = document.createElement('span');
  S.streamCursorEl.className = 'stream-cursor';
  S.streamBubbleEl = wrapper;
  S.streamContentEl = wrapper.querySelector('#stream-content');
  S.streamContentEl.appendChild(S.streamCursorEl);
  // Add copy button and collapse check to the stream bubble
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) {
    addCopyButton(bubble);
    checkCollapse(bubble);
  }
  pruneMessages();
  scrollBottom();
}

export function endStream() {
  removeStreamCursor();
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

  let sender = role;
  if (role === 'user') sender = 'you';

  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">' + sender + '</div>' +
      '<div class="content">' + markdownToHtml(content) + '</div>' +
    '</div>';
  messagesEl.appendChild(wrapper);
  // Copy button and collapse check on the freshly appended bubble.
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) {
    addCopyButton(bubble);
    checkCollapse(bubble);
  }
  pruneMessages();
  scrollBottom();
}

export function addSystemMessage(content) {
  hideEmptyState();
  const el = document.createElement('div');
  el.className = 'msg system';
  el.innerHTML = '<div class="bubble"><div class="content">' + escapeHtml(content) + '</div></div>';
  messagesEl.appendChild(el);
  announce(content);
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
      '<div class="sender">assistant</div>' +
      '<div class="content">' + markdownToHtml(content) + '</div>' +
    '</div>';
  messagesEl.appendChild(wrapper);
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) { addCopyButton(bubble); checkCollapse(bubble); }
  pruneMessages();
}

// ── Tool Helpers ──

// Matches Go's render.ToolEmoji for consistency. Exported so tests can pin
// the mirror (internal/render/render.go is the source of truth).
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
  // Only add the "tool calls" divider once per tool group per turn.
  if (!S.inToolGroup) {
    addDivider('tool calls');
    S.inToolGroup = true;
  }

  const emoji = toolEmoji(name);
  const preview = buildToolPreview(name, data);

  const el = document.createElement('div');
  el.className = 'tool-block';
  el.innerHTML =
    '<div class="tb-header" role="button" tabindex="0" aria-expanded="false">' +
      '<span class="arrow">▶</span>' +
      ' <span class="tb-emoji">' + emoji + '</span>' +
      ' <span class="tb-name">' + escapeHtml(name) + '</span>' +
      (preview ? ' <span class="tb-preview">' + escapeHtml(preview) + '</span>' : '') +
      ' <span class="tb-spinner running"></span>' +
      ' <span class="tb-latency"></span>' +
    '</div>' +
    '<div class="tb-body">' + escapeHtml(formatToolArgs(data)) + '</div>';

  messagesEl.appendChild(el);
  S.currentToolBlock = el;

  // Push into per-name FIFO queues so parallel results route correctly.
  if (!S.toolBlockQueues.has(name)) S.toolBlockQueues.set(name, []);
  S.toolBlockQueues.get(name).push(el);
  if (!S.toolStartQueues.has(name)) S.toolStartQueues.set(name, []);
  S.toolStartQueues.get(name).push(performance.now());

  pruneMessages();
  scrollBottom();
}

// appendToolResultContent renders a tool result into a block, truncating
// long output behind a "show all" expander. Shared by the live path
// (addToolResult) and session-history rendering.
function appendToolResultContent(block, output) {
  const resultEl = document.createElement('div');
  resultEl.className = 'tb-result';
  block.appendChild(resultEl);
  fillToolResult(resultEl, output || '', true);
}

// fillToolResult renders tool output into resultEl. The server sends raw,
// unsanitized content; tool output may embed the model-facing
// <untrusted_content_*> envelope, which is unwrapped for display — the body
// is inserted as text (never HTML) and the envelope source is shown as a
// badge instead of the literal tag text. When truncate is true, long bodies
// are cut behind a "show all" expander carrying the full output.
function fillToolResult(resultEl, output, truncate) {
  const MAX_RESULT = 600;
  const segments = parseUntrusted(output);
  for (const seg of segments) {
    if (seg.source) {
      const badge = document.createElement('span');
      badge.className = 'tb-source';
      badge.textContent = '🔒 ' + seg.source;
      resultEl.appendChild(badge);
      resultEl.appendChild(document.createTextNode('\n'));
    }
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

  appendToolResultContent(block, output || '');
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

// ── Sub-agent Cards ──
export function addSubagentGroup(command) {
  removeStreamCursor();
  if (S.subagentGroup) return; // only one group at a time

  addDivider('delegated tasks');

  let tasks = [];
  try {
    const parsed = JSON.parse(command);
    tasks = parsed.tasks || [];
  } catch { tasks = []; }

  const group = document.createElement('div');
  group.className = 'subagent-group';
  group.innerHTML = '<div class="sg-header">Sub-agents</div><div class="subagent-grid" id="sa-grid"></div>';
  messagesEl.appendChild(group);
  S.subagentGroup = group;

  const grid = group.querySelector('#sa-grid');
  tasks.forEach((task, i) => {
    const card = document.createElement('div');
    card.className = 'subagent-card running';
    card.dataset.index = i;
    card.innerHTML =
      '<div class="sa-top">' +
        '<div class="sa-icon">⟳</div>' +
        '<div class="sa-goal" title="' + escapeAttr(task.goal || 'Task ' + (i+1)) + '">' + escapeHtml(task.goal || 'Task ' + (i+1)) + '</div>' +
        '<div class="sa-status">running</div>' +
      '</div>' +
      '<div class="sa-details">' +
        '<div class="sa-meta"></div>' +
        '<div class="sa-summary"></div>' +
        '<div class="sa-files"></div>' +
      '</div>';
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
  if (!keepStatus) {
    card.querySelector('.sa-icon').textContent = '✓';
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
      card.querySelector('.sa-icon').textContent = '✗';
      card.querySelector('.sa-status').textContent = 'error';
    } else {
      card.classList.add('completed');
    }
  } else if (status === 'error') {
    card.classList.add('error');
  } else {
    card.classList.add('completed');
  }

  // Auto-open details for error or when there's a summary
  const details = card.querySelector('.sa-details');
  const summary = result.summary || '';
  const files = result.files_changed || [];
  const tokens = result.tokens_used || 0;
  const iters = result.iterations || 0;

  if (summary || files.length > 0) {
    const meta = details.querySelector('.sa-meta');
    if (tokens) meta.textContent = tokens + ' tokens' + (iters ? ' · ' + iters + ' iters' : '');

    const summaryEl = details.querySelector('.sa-summary');
    summaryEl.textContent = typeof summary === 'string' ? summary : '';

    if (files.length > 0) {
      const filesEl = details.querySelector('.sa-files');
      filesEl.innerHTML = files.map(f => '<span class="file-chip"><span class="icon">📄</span>' + escapeHtml(f) + '</span>').join('');
    }

    details.classList.add('open');
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

  switch (ev.phase) {
    case 'started':
      statusEl.textContent = 'running';
      break;
    case 'active':
      statusEl.textContent = '⟳ ' + (ev.tool || 'working') + (ev.step ? ' · ' + ev.step : '');
      if (meta && ev.step) meta.textContent = 'step ' + ev.step + (ev.tool ? ' · ' + ev.tool : '');
      break;
    case 'finished': {
      card.dataset.finalized = '1';
      card.classList.remove('running');
      const failed = ev.status && ev.status !== 'success' && ev.status !== 'partial';
      card.querySelector('.sa-icon').textContent = failed ? '✗' : '✓';
      card.classList.add(failed ? 'error' : 'completed');
      statusEl.textContent = failed ? (ev.status || 'failed') : (ev.status === 'partial' ? 'partial' : 'done');
      if (meta) {
        const parts = [];
        if (ev.tokens_used) parts.push(ev.tokens_used + ' tokens');
        if (ev.iterations) parts.push(ev.iterations + ' iters');
        if (ev.duration_seconds) parts.push(ev.duration_seconds.toFixed(1) + 's');
        meta.textContent = parts.join(' · ');
      }
      if (failed) {
        const d = card.querySelector('.sa-details');
        if (d) d.classList.add('open');
      }
      updateSubagentHeader();
      break;
    }
  }
}

// updateSubagentHeader shows wave progress on the group header:
// "Sub-agents · N/M complete · F failed".
function updateSubagentHeader() {
  if (!S.subagentGroup) return;
  const cards = S.subagentGroup.querySelectorAll('.subagent-card');
  if (!cards.length) return;
  let done = 0, failed = 0;
  cards.forEach(c => {
    if (c.classList.contains('completed')) done++;
    else if (c.classList.contains('error')) failed++;
  });
  const header = S.subagentGroup.querySelector('.sg-header');
  if (header && (done || failed)) {
    header.textContent = 'Sub-agents · ' + done + '/' + cards.length + ' complete' +
      (failed ? ' · ' + failed + ' failed' : '');
  }
}

function toggleSaDetails(el) {
  el.classList.toggle('open');
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

  // Format the log line
  let text = '';
  if (event.event === 'tool_call') {
    text = '🔧 ' + event.name + (event.data ? '(' + truncateStr(event.data, 60) + ')' : '');
  } else if (event.event === 'tool_result') {
    text = '📄 ' + truncateStr(event.data || '', 100);
  }
  if (!text) return;

  // Append to existing summary content (or create a log container)
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

  details.classList.add('open');
  scrollBottom();
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

  let toolDividerShown = false;
  messages.forEach(msg => {
    if (msg.role === 'user') {
      addMessage('user', stripAttachmentBodies(msg.content || ''));
      toolDividerShown = false;
      return;
    }
    if (msg.role !== 'assistant') return; // skip system/tool internals

    if (msg.reasoning_content) renderHistoricalThinking(msg.reasoning_content);

    const toolCalls = Array.isArray(msg.tool_calls) ? msg.tool_calls : [];
    if (msg.content) {
      renderAssistantMessage(msg.content);
    }
    if (toolCalls.length > 0) {
      if (!toolDividerShown) {
        addDivider('tool calls');
        toolDividerShown = true;
      }
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
  });
}

// renderHistoricalThinking renders a completed reasoning block (collapsed).
function renderHistoricalThinking(content) {
  const block = document.createElement('div');
  block.className = 'thinking-block';
  block.innerHTML =
    '<div class="thinking-toggle" role="button" tabindex="0" aria-expanded="false">' +
      '<span class="arrow">▶</span> reasoning' +
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
      ' <span class="tb-emoji">' + toolEmoji(name) + '</span>' +
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
  addDivider('delegated tasks');

  let tasks = [];
  try { tasks = JSON.parse(args).tasks || []; } catch { tasks = []; }
  const taskResults = parseSubagentResults(output);

  const group = document.createElement('div');
  group.className = 'subagent-group';
  group.innerHTML = '<div class="sg-header">Sub-agents</div><div class="subagent-grid"></div>';
  messagesEl.appendChild(group);
  const grid = group.querySelector('.subagent-grid');

  tasks.forEach((task, i) => {
    const card = document.createElement('div');
    card.className = 'subagent-card running';
    card.dataset.index = i;
    card.innerHTML =
      '<div class="sa-top">' +
        '<div class="sa-icon">⟳</div>' +
        '<div class="sa-goal" title="' + escapeAttr(task.goal || 'Task ' + (i+1)) + '">' + escapeHtml(task.goal || 'Task ' + (i+1)) + '</div>' +
        '<div class="sa-status">running</div>' +
      '</div>' +
      '<div class="sa-details">' +
        '<div class="sa-meta"></div>' +
        '<div class="sa-summary"></div>' +
        '<div class="sa-files"></div>' +
      '</div>';
    grid.appendChild(card);
    finalizeSubagentCard(card, taskResults[i]);
  });
}

// ── Collapse long messages ──
// Must match the max-height of .bubble.collapsible in style.css.
export const COLLAPSE_MAX_HEIGHT_PX = 460;

function toggleCollapse(el) {
  const bubble = el.closest('.bubble');
  if (!bubble) return;
  bubble.classList.toggle('expanded');
  el.textContent = bubble.classList.contains('expanded') ? 'Show less ▲' : 'Show more ▼';
}

function checkCollapse(bubble) {
  const content = bubble.querySelector('.content');
  if (!content || content.scrollHeight <= COLLAPSE_MAX_HEIGHT_PX) return;
  bubble.classList.add('collapsible');
  const toggle = document.createElement('div');
  toggle.className = 'collapse-toggle';
  toggle.setAttribute('role', 'button');
  toggle.setAttribute('tabindex', '0');
  toggle.textContent = 'Show more ▼';
  bubble.appendChild(toggle);
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

  const saDetails = t.closest('.sa-details');
  if (saDetails) { toggleSaDetails(saDetails); return; }
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
