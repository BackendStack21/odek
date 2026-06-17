(() => {
'use strict';

// ── State ──
// Migrate legacy kode_* keys to odek_*
['history', 'model', 'theme', 'thinking'].forEach(k => {
  if (!localStorage.getItem('odek_' + k) && localStorage.getItem('kode_' + k)) {
    localStorage.setItem('odek_' + k, localStorage.getItem('kode_' + k));
    localStorage.removeItem('kode_' + k);
  }
});

let ws = null;
let sessionId = null;
let sessionTokens = {}; // session id -> auth token
let busy = false;
let history = JSON.parse(localStorage.getItem('odek_history') || '[]');
let historyIdx = -1;
let attachedFiles = []; // {name, size, content}
let currentModel = localStorage.getItem('odek_model') || '';
let availableModels = [];
// Per-query thinking toggle. Persisted so it survives page refresh.
let thinkingEnabled = localStorage.getItem('odek_thinking') === '1';

function getSessionToken(sid) {
  if (!sid) return '';
  return sessionTokens[sid] || localStorage.getItem('odek_session_token_' + sid) || '';
}

function setSessionToken(sid, token) {
  if (!sid || !token) return;
  sessionTokens[sid] = token;
  localStorage.setItem('odek_session_token_' + sid, token);
}

function clearSessionToken(sid) {
  if (!sid) return;
  delete sessionTokens[sid];
  localStorage.removeItem('odek_session_token_' + sid);
}

// ── DOM ──
const messagesEl = document.getElementById('messages');
const promptEl = document.getElementById('prompt');
const sendBtn = document.getElementById('send-btn');
const completionEl = document.getElementById('completion');
const statusEl = document.getElementById('ws-status');
const dotEl = document.getElementById('ws-dot');
const modelLabel = document.getElementById('model-label');
const statsBar = document.getElementById('stats-bar');
const sessionListEl = document.getElementById('session-list');
const sidebarSearch = document.getElementById('sidebar-search');
const emptyState = document.getElementById('empty-state');
const cancelBtn = document.getElementById('cancel-btn');
const scrollBottomBtn = document.getElementById('scroll-bottom-btn');
const skeletonEl = document.getElementById('loading-skeleton');
const sidebarOverlay = document.getElementById('sidebar-overlay');

// ── Streaming ──
let streamBubbleEl = null;
let streamContentEl = null;
let streamBuffer = '';
let streamRAF = null;
let thinkingContentEl = null;  // current thinking block if any

// ── Tool call state ──
let currentToolBlock = null;
// FIFO queues per tool name so parallel results route to the correct block.
// Map<string, HTMLElement[]>
const toolBlockQueues = new Map();
// Whether the current turn has started a "tool calls" divider group.
let inToolGroup = false;
// Timestamps for tool latency (name → start ms, queue-based like above).
const toolStartQueues = new Map();

// ── Sub-agent state ──
let subagentGroup = null;

// ── Smart Scroll ──
let scrollRAF = null;
const SCROLL_THRESHOLD = 100;
function isNearBottom() {
  return messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < SCROLL_THRESHOLD;
}
function scrollBottom() {
  if (!isNearBottom()) return; // user is reading up — don't steal scroll
  if (scrollRAF) return;
  scrollRAF = requestAnimationFrame(() => {
    messagesEl.scrollTop = messagesEl.scrollHeight;
    scrollRAF = null;
  });
}
function forceScrollBottom() {
  if (scrollRAF) {
    cancelAnimationFrame(scrollRAF);
    scrollRAF = null;
  }
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

// ── Toast ──
let toastTimer = null;
function showToast(msg) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 2500);
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
let loadingEl = null;
let loadingTimer = null;

function showLoading() {
  const el = document.createElement('div');
  el.className = 'loading-indicator';
  el.innerHTML = '<div class="li-dots"><span></span></div><div class="li-text">thinking</div>';
  // Insert after the last message (the user message we just added)
  messagesEl.appendChild(el);
  loadingEl = el;
  // Cycle messages
  let idx = 0;
  loadingTimer = setInterval(() => {
    if (!loadingEl) return;
    const textEl = loadingEl.querySelector('.li-text');
    if (!textEl) return;
    textEl.textContent = loadingMessages[idx % loadingMessages.length];
    idx++;
  }, 2000);
  pruneMessages();
  // Force scroll to show the indicator (user just sent — they're at bottom)
  forceScrollBottom();
}

function hideLoading() {
  if (loadingEl) {
    loadingEl.remove();
    loadingEl = null;
  }
  if (loadingTimer) {
    clearInterval(loadingTimer);
    loadingTimer = null;
  }
}

// ── Error message normalization ──
function formatErrorMessage(msg) {
  if (!msg) return 'Unknown error';
  // Extract the core message from LiteLLM/provider verbose errors
  const match = msg.match(/"message"\s*:\s*"([^"]{0,200})"/) ||
                msg.match(/BadRequestError[^:]*:\s*(.{0,200})/);
  if (match) return match[1].trim();
  return msg.length > 300 ? msg.slice(0, 300) + '…' : msg;
}

// ── Number formatting ──
function formatNum(n) {
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'k';
  return String(n);
}

// ── Mesage cap ──
const MAX_MESSAGES = 80;
function pruneMessages() {
  const items = messagesEl.querySelectorAll(':scope > .msg, :scope > .tool-block, :scope > .subagent-group, :scope > .thinking-block, :scope > .typing-indicator');
  if (items.length > MAX_MESSAGES) {
    for (let i = 0, n = items.length - MAX_MESSAGES; i < n; i++) {
      items[i].remove();
    }
  }
}

// ── Auto-resize ──
promptEl.addEventListener('input', () => {
  promptEl.style.height = 'auto';
  promptEl.style.height = Math.min(promptEl.scrollHeight, 200) + 'px';
});

// ── Particles ──
function initParticles() {
  const el = document.getElementById('particles');
  if (!el) return;
  for (let i = 0; i < 15; i++) {
    const p = document.createElement('div');
    p.className = 'particle';
    p.style.left = Math.random() * 100 + '%';
    p.style.animationDelay = Math.random() * 6 + 's';
    p.style.animationDuration = (4 + Math.random() * 4) + 's';
    p.style.width = p.style.height = (1 + Math.random() * 3) + 'px';
    el.appendChild(p);
  }
}
initParticles();

// ── WebSocket ──
function getWsToken() {
  const meta = document.querySelector('meta[name="odek-ws-token"]');
  return meta ? meta.getAttribute('content') : '';
}

function connect() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const token = getWsToken();
  const protocols = token ? ['odek.' + token] : [];
  ws = new WebSocket(proto + '//' + location.host + '/ws', protocols);

  ws.onopen = () => {
    dotEl.className = 'dot connected';
    statusEl.textContent = 'connected';
    sendBtn.disabled = false;
    statsBar.textContent = '';
    // Hide loading skeleton when connected
    if (skeletonEl) skeletonEl.classList.remove('visible');
  };

  ws.onclose = () => {
    dotEl.className = 'dot disconnected';
    statusEl.textContent = 'reconnecting...';
    sendBtn.disabled = true;
    setTimeout(connect, 2000);
  };

  ws.onerror = () => { ws.close(); };

  ws.onmessage = (e) => {
    let event;
    try { event = JSON.parse(e.data); } catch { return; }

    switch (event.type) {
      case 'session':
        sessionId = event.session_id || null;
        if (event.auth_token) setSessionToken(sessionId, event.auth_token);
        // Only adopt the server's model on the very first session event
        // (no user-selected model yet). After that the user's choice wins.
        if (event.model && !currentModel) {
          currentModel = event.model;
          const picker = document.getElementById('model-picker');
          if (picker && picker.value !== event.model) picker.value = event.model;
        }
        modelLabel.textContent = currentModel || event.model || '';
        const sandboxBadge = document.getElementById('sandbox-badge');
        if (sandboxBadge) {
          sandboxBadge.style.display = event.sandbox ? 'inline-flex' : 'none';
        }
        loadSessions();
        break;

      case 'token':
        streamToken(event.content);
        break;

      case 'thinking':
        streamThinking(event.content);
        break;

      case 'tool_call':
        streamFlush();
        endThinking();
        if (event.name === 'delegate_tasks') {
          addSubagentGroup(event.data);
        } else {
          addToolCall(event.name, event.data);
        }
        break;

      case 'tool_result':
        if (event.name === 'delegate_tasks' && subagentGroup) {
          completeSubagents(event.data);
        }
        addToolResult(event.name, event.data);
        break;

      case 'subagent_log':
        appendSubagentLog(event.task_idx, event);
        break;

      case 'done':
        streamFlush();
        endThinking();
        endStream();
        // Append per-message stats to the last assistant bubble
        if (event.latency != null) {
          const lastAssistant = messagesEl.querySelector('.msg.assistant:last-child .bubble');
          if (lastAssistant) {
            const stats = document.createElement('div');
            stats.className = 'msg-stats';
            const spans = [];
            spans.push('<span title="Response time">⚡ ' + (event.latency < 1 ? (event.latency * 1000).toFixed(0) + 'ms' : event.latency.toFixed(1) + 's') + '</span>');
            if (event.contextTokens != null) spans.push('<span title="Input tokens (prompt)">' + formatNum(event.contextTokens) + ' in</span>');
            if (event.outputTokens != null) spans.push('<span title="Output tokens (completion)">' + formatNum(event.outputTokens) + ' out</span>');
            // Cache metrics — show only when non-zero
            if (event.cacheCreationTokens > 0) spans.push('<span title="Cache write: tokens stored on first cache-controlled request">' + formatNum(event.cacheCreationTokens) + ' stored</span>');
            if (event.cacheReadTokens > 0) spans.push('<span title="Cache hit: tokens served from cache on subsequent requests">' + formatNum(event.cacheReadTokens) + ' read</span>');
            if (event.cachedTokens > 0) spans.push('<span title="Cached tokens (automatic prefix match)">' + formatNum(event.cachedTokens) + ' cached</span>');
            stats.innerHTML = spans.join('  ·  ');
            lastAssistant.appendChild(stats);
          }
        }
        // Update session-level token stats in top bar
        const sessionStatsEl = document.getElementById('session-stats');
        if (event.sessionContextTokens != null && event.sessionOutputTokens != null) {
          const sessSpans = ['<span title="Session total input tokens">∑ ' + formatNum(event.sessionContextTokens) + ' in</span>', '<span title="Session total output tokens">' + formatNum(event.sessionOutputTokens) + ' out</span>'];
          if (event.cacheReadTokens > 0 || event.cacheCreationTokens > 0 || event.cachedTokens > 0) {
            // Count total session cache stats (accumulated across the session)
            // We store cache totals on the session-stats element's dataset
            const el = document.getElementById('session-stats');
            const cc = (parseInt(el.dataset.cacheCreate || '0') + (event.cacheCreationTokens || 0));
            const cr = (parseInt(el.dataset.cacheRead || '0') + (event.cacheReadTokens || 0));
            const cd = (parseInt(el.dataset.cached || '0') + (event.cachedTokens || 0));
            el.dataset.cacheCreate = cc;
            el.dataset.cacheRead = cr;
            el.dataset.cached = cd;
            if (cr > 0) sessSpans.push('<span title="Session total cache hits">' + formatNum(cr) + ' read</span>');
            if (cc > 0) sessSpans.push('<span title="Session total cache writes">' + formatNum(cc) + ' stored</span>');
            if (cd > 0) sessSpans.push('<span title="Session total cached tokens (automatic)">' + formatNum(cd) + ' cached</span>');
          }
          sessionStatsEl.innerHTML = sessSpans.join('  ·  ');
          sessionStatsEl.classList.add('visible');
        }
        if (sessionId) loadSessions();
        break;

      case 'error':
        streamFlush(); endThinking(); endStream();
        addSystemMessage('⚠ ' + formatErrorMessage(event.message));
        break;

      case 'approval_request':
        showApprovalDialog(event);
        break;

      case 'skill_event':
        handleSkillEvent(event);
        break;

      case 'memory_event':
        handleMemoryEvent(event);
        break;

      case 'agent_signal':
        handleAgentSignal(event);
        break;
    }
  };
}

// ── Thinking ──
function streamThinking(content) {
  if (!thinkingContentEl) {
    // Remove cursor from any active stream
    removeStreamCursor();

    const block = document.createElement('div');
    block.className = 'thinking-block';
    block.innerHTML =
      '<div class="thinking-toggle" onclick="toggleThinking(this)">' +
        '<span class="arrow">▶</span> reasoning' +
      '</div>' +
      '<div class="thinking-content">' + escapeHtml(content) + '</div>';
    messagesEl.appendChild(block);

    thinkingContentEl = block.querySelector('.thinking-content');
    hideEmptyState();
    pruneMessages();
    scrollBottom();
  } else {
    thinkingContentEl.textContent += content;
    scrollBottom();
  }
}

window.toggleThinking = function(el) {
  const arrow = el.querySelector('.arrow');
  const content = el.parentElement.querySelector('.thinking-content');
  if (content) {
    content.classList.toggle('open');
    arrow.classList.toggle('open');
    // Auto-open on first click
    if (content.classList.contains('open')) {
      scrollBottom();
    }
  }
};

function endThinking() {
  thinkingContentEl = null;
}

// ── Streaming ──
function streamToken(content) {
  streamBuffer += content;
  if (!streamRAF) {
    streamRAF = requestAnimationFrame(streamFlushRAF);
  }
}

function streamFlushRAF() {
  streamRAF = null;
  if (!streamBuffer) return;
  appendStreamText(streamBuffer);
  streamBuffer = '';
}

function streamFlush() {
  if (streamRAF) {
    cancelAnimationFrame(streamRAF);
    streamRAF = null;
  }
  if (streamBuffer) {
    appendStreamText(streamBuffer);
    streamBuffer = '';
  }
}

function ensureStreamBubble() {
  if (!streamBubbleEl) {
    startStream();
  }
}

function appendStreamText(text) {
  ensureStreamBubble();
  // Convert MD fragments to HTML and append
  const html = markdownToHtml(text);
  // Use a temp container to handle multi-node fragment
  const temp = document.createElement('div');
  temp.innerHTML = html;
  while (temp.firstChild) {
    streamContentEl.appendChild(temp.firstChild);
  }
  scrollBottom();
}

function removeStreamCursor() {
  if (streamBubbleEl) {
    const cursor = streamBubbleEl.querySelector('.stream-cursor');
    if (cursor) cursor.remove();
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
      '<div class="content" id="stream-content"><span class="stream-cursor"></span></div>' +
    '</div>';
  messagesEl.appendChild(wrapper);

  streamBubbleEl = wrapper;
  streamContentEl = wrapper.querySelector('#stream-content');
  // Add copy button and collapse check to the stream bubble
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) {
    addCopyButton(bubble);
    checkCollapse(bubble);
  }
  pruneMessages();
  scrollBottom();
}

function endStream() {
  removeStreamCursor();
  streamBubbleEl = null;
  streamContentEl = null;
  currentToolBlock = null;
  subagentGroup = null;
  toolBlockQueues.clear();
  toolStartQueues.clear();
  inToolGroup = false;
  busy = false;
  hideLoading();
  hideCancel();
  sendBtn.disabled = !ws || ws.readyState !== WebSocket.OPEN;
  promptEl.disabled = false;
  promptEl.focus();
}

// ── Message rendering ──
function addMessage(role, content) {
  hideEmptyState();
  const wrapper = document.createElement('div');
  wrapper.className = 'msg ' + role;

  let sender = role;
  if (role === 'user') sender = 'you';

  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">' + sender + '</div>' +
      '<div class="content">' + markdownToHtml(escapeHtml(content)) + '</div>' +
    '</div>';
  messagesEl.appendChild(wrapper);
  pruneMessages();
  scrollBottom();
}

function addSystemMessage(content) {
  hideEmptyState();
  const el = document.createElement('div');
  el.className = 'msg system';
  el.innerHTML = '<div class="bubble"><div class="content">' + escapeHtml(content) + '</div></div>';
  messagesEl.appendChild(el);
  pruneMessages();
  scrollBottom();
}

function addTypingIndicator() {
  const el = document.createElement('div');
  el.className = 'typing-indicator';
  el.innerHTML = '<div class="typing-dot"></div><div class="typing-dot"></div><div class="typing-dot"></div>';
  messagesEl.appendChild(el);
  scrollBottom();
  return el;
}

function addDivider(text) {
  const el = document.createElement('div');
  el.className = 'msg-divider';
  el.textContent = text || '•';
  messagesEl.appendChild(el);
  scrollBottom();
}

// ── Tool Helpers ──

// Matches Go's render.ToolEmoji for consistency.
function toolEmoji(name) {
  if (name === 'read_file' || name === 'write_file' || name === 'search_files' ||
      name === 'patch' || name === 'execute_code' || name === 'multi_grep') return '📝';
  if (name === 'shell' || name === 'terminal' || name === 'process') return '💻';
  if (name === 'web_search' || name === 'web_extract' || name.startsWith('browser_')) return '🌐';
  if (name === 'memory' || name === 'session_search') return '🧠';
  if (name === 'vision_analyze') return '👁️';
  if (name === 'send_message') return '💬';
  if (name === 'delegate_task' || name === 'delegate_tasks') return '👥';
  if (name === 'cronjob') return '⏰';
  if (name === 'todo' || name === 'skill_view' || name === 'skill_manage' ||
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
function addToolCall(name, data) {
  removeStreamCursor();
  // Only add the "tool calls" divider once per tool group per turn.
  if (!inToolGroup) {
    addDivider('tool calls');
    inToolGroup = true;
  }

  const emoji = toolEmoji(name);
  const preview = buildToolPreview(name, data);

  const el = document.createElement('div');
  el.className = 'tool-block';
  el.innerHTML =
    '<div class="tb-header" onclick="toggleToolBody(this)">' +
      '<span class="arrow">▶</span>' +
      ' <span class="tb-emoji">' + emoji + '</span>' +
      ' <span class="tb-name">' + escapeHtml(name) + '</span>' +
      (preview ? ' <span class="tb-preview">' + escapeHtml(preview) + '</span>' : '') +
      ' <span class="tb-spinner running"></span>' +
      ' <span class="tb-latency"></span>' +
    '</div>' +
    '<div class="tb-body">' + escapeHtml(formatToolArgs(data)) + '</div>';

  messagesEl.appendChild(el);
  currentToolBlock = el;

  // Push into per-name FIFO queues so parallel results route correctly.
  if (!toolBlockQueues.has(name)) toolBlockQueues.set(name, []);
  toolBlockQueues.get(name).push(el);
  if (!toolStartQueues.has(name)) toolStartQueues.set(name, []);
  toolStartQueues.get(name).push(performance.now());

  pruneMessages();
  scrollBottom();
}

function addToolResult(name, output) {
  // Route to the matching pending block via FIFO queue.
  const queue = toolBlockQueues.get(name);
  const block = (queue && queue.length > 0) ? queue.shift() : currentToolBlock;
  if (!block) return;

  // Remove spinner; show latency.
  const spinner = block.querySelector('.tb-spinner');
  if (spinner) spinner.classList.remove('running');
  const startQueue = toolStartQueues.get(name);
  if (startQueue && startQueue.length > 0) {
    const start = startQueue.shift();
    const ms = performance.now() - start;
    const latEl = block.querySelector('.tb-latency');
    if (latEl) latEl.textContent = ms < 1000 ? Math.round(ms) + 'ms' : (ms / 1000).toFixed(1) + 's';
  }

  // Show result, truncated if long.
  const MAX_RESULT = 600;
  const truncated = output && output.length > MAX_RESULT;
  let resultEl = block.querySelector('.tb-result');
  if (!resultEl) {
    resultEl = document.createElement('div');
    resultEl.className = 'tb-result';
    block.appendChild(resultEl);
  }
  if (truncated) {
    resultEl.innerHTML =
      escapeHtml(output.slice(0, MAX_RESULT)) +
      '<span class="tb-result-more" onclick="expandToolResult(this)" data-full="' +
        escapeAttr(output) + '"> …show all (' + output.length + ' chars)</span>';
  } else {
    resultEl.textContent = output || '';
  }
  scrollBottom();
}

window.expandToolResult = function(el) {
  const full = el.dataset.full || '';
  const resultEl = el.parentElement;
  if (resultEl) resultEl.textContent = full;
};

window.toggleToolBody = function(header) {
  const arrow = header.querySelector('.arrow');
  const body = header.parentElement.querySelector('.tb-body');
  if (body) {
    body.classList.toggle('open');
    arrow.classList.toggle('open');
  }
};

// ── Sub-agent Cards ──
function addSubagentGroup(command) {
  removeStreamCursor();
  if (subagentGroup) return; // only one group at a time

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
  subagentGroup = group;

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
      '<div class="sa-details" onclick="toggleSaDetails(this)">' +
        '<div class="sa-meta"></div>' +
        '<div class="sa-summary"></div>' +
        '<div class="sa-files"></div>' +
      '</div>';
    grid.appendChild(card);
  });

  pruneMessages();
  scrollBottom();
}

function completeSubagents(output) {
  if (!subagentGroup) return;

  // Parse sub-agent results from the output text
  // The delegate_tasks tool returns formatted text like:
  // "📋 Sub-agent results:\n\n─── Task 1: goal ───\n{json}\n\n─── Task 2: ..."
  const lines = output.split('\n');
  let currentTaskIdx = -1;
  const taskResults = {};

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const taskMatch = line.match(/─── Task (\d+):/);
    if (taskMatch) {
      currentTaskIdx = parseInt(taskMatch[1]) - 1;
      // Collect JSON from subsequent lines
      let jsonLines = [];
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

  const cards = subagentGroup.querySelectorAll('.subagent-card');
  cards.forEach((card, i) => {
    const result = taskResults[i];
    card.querySelector('.sa-icon').textContent = '✓';
    card.classList.remove('running');
    card.querySelector('.sa-status').textContent = 'done';

    if (result) {
      const status = result.status || 'success';
      if (status === 'error') {
        card.classList.add('error');
        card.querySelector('.sa-icon').textContent = '✗';
        card.querySelector('.sa-status').textContent = 'error';
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
    } else {
      card.classList.add('completed');
    }
  });

  pruneMessages();
  scrollBottom();
}

window.toggleSaDetails = function(el) {
  el.classList.toggle('open');
};

function appendSubagentLog(taskIdx, event) {
  if (!subagentGroup) return;
  const cards = subagentGroup.querySelectorAll('.subagent-card');
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

function truncateStr(s, n) {
  if (!s) return '';
  return s.length > n ? s.substring(0, n) + '…' : s;
}

// ── Approval ──
let approvalId = null;

window.showApprovalDialog = function(event) {
  approvalId = event.id;
  const riskEl = document.getElementById('approval-risk');
  riskEl.textContent = event.risk;
  riskEl.className = 'approval-risk ' + event.risk;
  // Set icon based on risk
  const iconEl = document.getElementById('approval-icon');
  const iconMap = { '🟡': '⚠️', '🔴': '🚫', '🟢': '✅' };
  iconEl.textContent = iconMap[event.risk] || '🛡️';
  iconEl.className = 'approval-icon ' + (event.risk || '');
  document.getElementById('approval-command').textContent = event.command;
  const descEl = document.getElementById('approval-desc');
  if (event.description) {
    descEl.textContent = event.description;
    descEl.style.display = 'block';
  } else {
    descEl.style.display = 'none';
  }
  // Hide the Trust button when the server says it is not allowed for
  // this class (destructive / blocked). Each call gets its own approval.
  const trustBtn = document.querySelector('#approval-actions .trust');
  if (trustBtn) {
    trustBtn.style.display = (event.allow_trust === false) ? 'none' : '';
  }

  // Approval-fatigue interrupt. When the server flags friction=true the
  // user has already approved the threshold number of this class
  // recently; require them to type the literal word 'approve' instead
  // of clicking, after a 1.5s gate.
  const overlay = document.getElementById('approval-overlay');
  let frictionInput = document.getElementById('approval-friction-input');
  let frictionMsg   = document.getElementById('approval-friction-msg');
  if (event.friction) {
    if (!frictionMsg) {
      frictionMsg = document.createElement('div');
      frictionMsg.id = 'approval-friction-msg';
      frictionMsg.style.cssText = 'color: var(--accent); font-size: 13px; margin-top: 8px;';
      overlay.querySelector('.approval-dialog')?.appendChild(frictionMsg)
        || document.getElementById('approval-actions').parentNode.insertBefore(frictionMsg, document.getElementById('approval-actions'));
    }
    frictionMsg.textContent =
      `⚠️ You have approved ${event.friction_approvals || 0} ${event.risk} operations in the last minute. ` +
      `Type the word 'approve' to proceed.`;
    frictionMsg.style.display = '';

    if (!frictionInput) {
      frictionInput = document.createElement('input');
      frictionInput.id = 'approval-friction-input';
      frictionInput.type = 'text';
      frictionInput.placeholder = 'type: approve';
      frictionInput.style.cssText = 'width: 100%; margin-top: 6px; padding: 6px;';
      frictionMsg.parentNode.insertBefore(frictionInput, document.getElementById('approval-actions'));
    }
    frictionInput.value = '';
    frictionInput.style.display = '';

    // Disable Approve button until correct word typed + 1.5s elapsed.
    const approveBtn = document.querySelector('#approval-actions .approve');
    if (approveBtn) {
      approveBtn.disabled = true;
      setTimeout(() => {
        frictionInput.oninput = () => {
          approveBtn.disabled = (frictionInput.value.trim().toLowerCase() !== 'approve');
        };
      }, 1500);
    }
  } else {
    if (frictionMsg)   frictionMsg.style.display = 'none';
    if (frictionInput) frictionInput.style.display = 'none';
    const approveBtn = document.querySelector('#approval-actions .approve');
    if (approveBtn) approveBtn.disabled = false;
  }

  overlay.classList.add('active');
};

window.sendApproval = function(action) {
  if (!approvalId) return;
  ws.send(JSON.stringify({
    type: 'approval_response',
    id: approvalId,
    action: action
  }));
  document.getElementById('approval-overlay').classList.remove('active');
  approvalId = null;
};

// ── Confirm Dialog ──
window.hideConfirmDialog = function() {
  document.getElementById('confirm-overlay').classList.remove('active');
  pendingDeleteId = null;
};

window.executeDeleteSession = async function() {
  if (!pendingDeleteId) return;
  const sid = pendingDeleteId;
  pendingDeleteId = null;
  document.getElementById('confirm-overlay').classList.remove('active');

  let token = getSessionToken(sid);
  if (!token) {
    try {
      const bootstrap = await fetch('/api/sessions/' + encodeURIComponent(sid));
      if (bootstrap.ok) {
        const bs = await bootstrap.json();
        token = bootstrap.headers.get('X-Session-Token') || bs.auth_token;
        if (token) setSessionToken(sid, token);
      }
    } catch { /* continue — server will return 401 if token required */ }
  }

  fetch('/api/sessions/' + encodeURIComponent(sid), {
    method: 'DELETE',
    headers: token ? { 'X-Session-Token': token } : {}
  })
    .then(() => {
      clearSessionToken(sid);
      if (sessionId === sid) newSession();
      loadSessions();
    })
    .catch(() => showToast('Failed to delete session'));
};

// ── Theme Toggle ──
window.toggleTheme = function() {
  document.body.classList.toggle('light');
  const isLight = document.body.classList.contains('light');
  localStorage.setItem('odek_theme', isLight ? 'light' : 'dark');
  document.getElementById('theme-btn').textContent = isLight ? '☀️' : '🌙';
};
// Restore theme on load
if (localStorage.getItem('odek_theme') === 'light') {
  document.body.classList.add('light');
  document.getElementById('theme-btn').textContent = '☀️';
}

// ── Model Picker ──
const customModelInput = document.getElementById('custom-model-input');

async function fetchModels() {
  const picker = document.getElementById('model-picker');
  try {
    picker.disabled = true;
    const resp = await fetch('/api/models');
    if (!resp.ok) { picker.innerHTML = '<option value="">Models unavailable</option>'; return; }
    const models = await resp.json();
    availableModels = models;
    if (!models || models.length === 0) {
      picker.innerHTML = '<option value="">No models</option>';
      return;
    }
    let html = '';
    models.forEach(m => {
      const sel = currentModel === m.id ? ' selected' : '';
      const label = m.current ? '★ ' + (m.description || m.id) : (m.description || m.id);
      html += `<option value="${escapeAttr(m.id)}"${sel}>${escapeHtml(label)}</option>`;
    });
    // "Other..." sentinel opens the free-text input.
    html += '<option value="__custom__">Other (type model ID)…</option>';
    picker.innerHTML = html;
    if (currentModel) {
      picker.value = currentModel;
      // If the current model isn't in the list, show the custom input.
      if (!picker.value && currentModel) {
        picker.value = '__custom__';
        customModelInput.value = currentModel;
        customModelInput.style.display = 'inline-block';
      }
    }
  } catch {
    picker.innerHTML = '<option value="">Failed to load</option>';
  } finally {
    picker.disabled = false;
  }
}

// Called by the select's onchange — handles both known models and the
// "Other…" sentinel that reveals the free-text custom model input.
window.onPickerChange = function(value) {
  if (value === '__custom__') {
    customModelInput.style.display = 'inline-block';
    customModelInput.focus();
    customModelInput.select();
    return;
  }
  customModelInput.style.display = 'none';
  customModelInput.value = '';
  if (value) switchModel(value);
};

// Commit a custom model ID from the text input on Enter or blur.
customModelInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); commitCustomModel(); }
  if (e.key === 'Escape') {
    customModelInput.style.display = 'none';
    customModelInput.value = '';
    const picker = document.getElementById('model-picker');
    if (currentModel) picker.value = currentModel;
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

window.switchModel = function(modelId) {
  currentModel = modelId;
  if (modelId) {
    localStorage.setItem('odek_model', modelId);
  } else {
    localStorage.removeItem('odek_model');
  }
  showToast(modelId ? 'Model: ' + modelId : 'Using default model');
};

// ── Session Rename ──
window.renameSession = async function(sid, el) {
  const item = el.closest('.session-item');
  if (!item) return;
  const taskEl = item.querySelector('.task');
  const currentName = taskEl ? taskEl.textContent : '';
  const newName = prompt('Rename session:', currentName);
  if (!newName || newName === currentName) return;

  let token = getSessionToken(sid);
  if (!token) {
    try {
      const bootstrap = await fetch('/api/sessions/' + encodeURIComponent(sid));
      if (bootstrap.ok) {
        const bs = await bootstrap.json();
        token = bootstrap.headers.get('X-Session-Token') || bs.auth_token;
        if (token) setSessionToken(sid, token);
      }
    } catch { /* continue — server will return 401 if token required */ }
  }

  fetch('/api/sessions/' + encodeURIComponent(sid), {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { 'X-Session-Token': token } : {})
    },
    body: JSON.stringify({ name: newName })
  })
    .then(resp => {
      if (!resp.ok) throw new Error('rename failed');
      loadSessions();
      showToast('Session renamed');
    })
    .catch(() => showToast('Failed to rename session'));
};

// ── Skill Events ──
function handleSkillEvent(event) {
  switch (event.event) {
    case 'saved':
      showToast('✓ Skill saved: ' + (event.skill_name || ''));
      break;
    case 'deleted':
      showToast('✗ Skill deleted: ' + (event.skill_name || ''));
      break;
    case 'suggested': {
      // Inline card with save/skip — shown in messages area.
      const el = document.createElement('div');
      el.className = 'skill-toast';
      el.innerHTML =
        '💡 <strong>Skill suggestion:</strong> ' + escapeHtml(event.skill_name || '') +
        (event.heuristic ? ' — <em>' + escapeHtml(event.heuristic) + '</em>' : '');
      messagesEl.appendChild(el);
      scrollBottom();
      break;
    }
    case 'loaded': case 'autoloaded':
      // Silent — noisy to show every skill load.
      break;
  }
}

// ── Memory Events ──
function handleMemoryEvent(event) {
  switch (event.event) {
    case 'fact_added':
      showToast('🧠 Memory fact added (' + (event.target || '') + ')');
      break;
    case 'fact_merged':
      showToast('🧠 Memory fact merged (' + (event.target || '') + ')');
      break;
    case 'fact_replaced':
      showToast('🧠 Memory fact updated (' + (event.target || '') + ')');
      break;
    case 'fact_removed':
      showToast('🧠 Memory fact removed (' + (event.target || '') + ')');
      break;
    case 'fact_consolidated':
      showToast('🧠 Memory consolidated (' + (event.target || '') + ': ' +
        (event.count || 0) + ' → ' + (event.new_count || 0) + ')');
      break;
    case 'episode_stored':
      // Silent by default — fires after every qualifying session.
      break;
    case 'episode_promoted':
      showToast('💾 ✓ Episode promoted: ' + (event.session_id || ''));
      break;
    case 'episode_evicted':
      showToast('💾 ✗ ' + (event.count || 0) + ' episode(s) evicted');
      break;
    case 'episode_pending_review':
      showToast('🔒 Episode pending review (untrusted): ' + (event.session_id || ''));
      break;
    case 'episode_deduped':
      // Silent — internal dedup detail.
      break;
  }
}

// ── Agent Signals ──
function handleAgentSignal(event) {
  switch (event.event) {
    case 'context_trimmed':
      showToast('✂️ Context trimmed (' + (event.detail || '') + '): ' +
        (event.count || 0) + ' group(s) dropped');
      break;
    case 'tool_recovery':
      showToast('🔁 Tool recovery: ' + (event.tool || ''));
      break;
  }
}

// ── New Session ──
// Saved on first load so we can restore the empty state after clearing.
let savedEmptyStateNode = null;
let savedScrollBtnNode = null;

window.newSession = function() {
  sessionId = null;

  // Reset all streaming + tool state.
  streamBuffer = '';
  if (streamRAF) { cancelAnimationFrame(streamRAF); streamRAF = null; }
  streamBubbleEl = null; streamContentEl = null;
  currentToolBlock = null; subagentGroup = null; thinkingContentEl = null;
  toolBlockQueues.clear(); toolStartQueues.clear(); inToolGroup = false;
  busy = false;
  hideLoading(); hideCancel();
  sendBtn.disabled = !ws || ws.readyState !== WebSocket.OPEN;
  promptEl.disabled = false;

  // Clear messages and restore empty state.
  messagesEl.innerHTML = '';
  if (savedScrollBtnNode) messagesEl.appendChild(savedScrollBtnNode);
  if (savedEmptyStateNode) messagesEl.appendChild(savedEmptyStateNode);

  sessionListEl.querySelectorAll('.session-item').forEach(s => s.classList.remove('active'));
  showToast('New session');
  promptEl.value = '';
  promptEl.style.height = 'auto';
  promptEl.focus();
};

// ── Shortcuts ──
window.toggleShortcuts = function() {
  document.getElementById('shortcuts-overlay').classList.toggle('active');
};
document.getElementById('shortcuts-overlay').addEventListener('click', (e) => {
  if (e.target === e.currentTarget) toggleShortcuts();
});

// ── Hide empty state ──
function hideEmptyState() {
  if (emptyState && emptyState.parentNode) {
    emptyState.remove();
  }
}

// ── Escape helpers ──
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function escapeAttr(s) {
  if (!s) return '';
  return s.replace(/"/g,'&quot;').replace(/'/g,'&#39;').replace(/&/g,'&amp;');
}

// ── Markdown to HTML (safe, no DOMPurify needed since we control input) ──
function markdownToHtml(text) {
  if (!text) return '';

  let html = escapeHtml(text);

  // Headers (must be at start of line)
  html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');

  // Horizontal rules
  html = html.replace(/^(---|\*\*\*|___)$/gm, '<hr>');

  // Code blocks (```lang ... ```) — need to handle BEFORE inline code
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (match, lang, code) => {
    const langLabel = lang || 'code';
    return '<div class="code-block">' +
      '<div class="cb-header">' +
        '<span class="cb-lang">' + escapeHtml(langLabel) + '</span>' +
        '<span class="cb-copy" onclick="copyCode(this)">📋 copy</span>' +
      '</div>' +
      '<pre><code>' + escapeHtml(code) + '</code></pre>' +
    '</div>';
  });

  // Inline code
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');

  // Bold
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');

  // Italic
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');

  // Strikethrough
  html = html.replace(/~~(.+?)~~/g, '<s>$1</s>');

  // Links — allowlist safe URL schemes to prevent javascript:/data: XSS.
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (match, label, url) => {
    const trimmed = url.trim();
    const safe = /^(https?:|mailto:|\/|#|\.\/|\.\.\/)/i.test(trimmed);
    if (!safe) return label;
    return '<a href="' + trimmed.replace(/"/g, '&quot;') + '" target="_blank" rel="noopener noreferrer">' + label + '</a>';
  });

  // Unordered lists (simple: lines starting with - or *)
  html = html.replace(/^[\s]*[-*]\s+(.+)$/gm, '<li>$1</li>');
  html = html.replace(/(<li>.*<\/li>\n?)+/g, '<ul>$&</ul>');

  // Paragraphs — wrap remaining non-tag text in <p>
  // Split by double newlines (paragraph breaks)
  const parts = html.split(/\n\n+/);
  html = parts.map(part => {
    part = part.trim();
    if (!part) return '';
    // Don't wrap if it starts with a block-level tag
    if (/^<(h[1-4]|ul|ol|li|pre|div|hr|table)/.test(part)) return part;
    // Don't wrap single <br>
    if (/^<br\s*\/?>$/.test(part)) return part;
    return '<p>' + part.replace(/\n/g, '<br>') + '</p>';
  }).join('\n');

  return html;
}

// ── Copy code ──
window.copyCode = function(el) {
  const block = el.closest('.code-block');
  if (!block) return;
  const code = block.querySelector('pre code');
  if (!code) return;
  const text = code.textContent;
  navigator.clipboard.writeText(text).then(() => {
    el.textContent = '✓ copied';
    el.classList.add('copied');
    setTimeout(() => {
      el.textContent = '📋 copy';
      el.classList.remove('copied');
    }, 2000);
  }).catch(() => {
    // Fallback
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    el.textContent = '✓ copied';
    setTimeout(() => { el.textContent = '📋 copy'; }, 2000);
  });
};

// ── Send ──
function send() {
  let text = promptEl.value.trim();

  // Build display message (filename chips only — never the file body) and a
  // separate attachments payload. The server wraps each attachment with the
  // untrusted-content boundary before injecting it into the model context.
  let display = text;
  let attachments = [];
  if (attachedFiles.length > 0) {
    const chips = attachedFiles.map(f => '📎 ' + f.name + ' (' + formatFileSize(f.size) + ')').join('\n');
    display = chips + (text ? '\n\n' + text : '');
    attachments = attachedFiles.map(f => ({ name: f.name, content: f.content }));
    clearAttachedFiles();
  }

  if ((!text && attachments.length === 0) || busy || !ws || ws.readyState !== WebSocket.OPEN) return;

  history.push(text);
  if (history.length > 100) history.shift();
  localStorage.setItem('odek_history', JSON.stringify(history));
  historyIdx = history.length;

  promptEl.value = '';
  promptEl.style.height = 'auto';

  // Add user message (display-only — file bodies are not rendered)
  addMessage('user', display);

  // Reset streaming + tool state for the new turn.
  streamBuffer = '';
  if (streamRAF) {
    cancelAnimationFrame(streamRAF);
    streamRAF = null;
  }
  streamBubbleEl = null;
  streamContentEl = null;
  currentToolBlock = null;
  subagentGroup = null;
  thinkingContentEl = null;
  toolBlockQueues.clear();
  toolStartQueues.clear();
  inToolGroup = false;

  busy = true;
  sendBtn.disabled = true;
  promptEl.disabled = true;

  showLoading();
  showCancel();

  ws.send(JSON.stringify({
    type: 'prompt',
    content: text,
    attachments: attachments,
    session_id: sessionId,
    auth_token: getSessionToken(sessionId) || undefined,
    model: currentModel || undefined,
    thinking: thinkingEnabled ? 'enabled' : ''
  }));
}

// ── File Attachments ──
const fileInput = document.getElementById('file-input');
const attachBtn = document.getElementById('attach-btn');
const fileChips = document.getElementById('file-chips');

function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024*1024) return (bytes/1024).toFixed(1) + ' KB';
  return (bytes/(1024*1024)).toFixed(1) + ' MB';
}

function addAttachedFile(file) {
  // Check total size (max 10MB total)
  const totalSize = attachedFiles.reduce((s, f) => s + f.size, 0) + file.size;
  if (totalSize > 10 * 1024 * 1024) {
    showToast('Total attachment size exceeds 10 MB');
    return;
  }
  attachedFiles.push(file);
  renderFileChips();
}

function removeAttachedFile(index) {
  attachedFiles.splice(index, 1);
  renderFileChips();
}

function clearAttachedFiles() {
  attachedFiles = [];
  renderFileChips();
}

function renderFileChips() {
  if (attachedFiles.length === 0) {
    fileChips.innerHTML = '';
    return;
  }
  fileChips.innerHTML = attachedFiles.map((f, i) =>
    '<span class="file-chip">' +
      '<span class="chip-icon">📎</span>' +
      '<span class="chip-name">' + escapeHtml(f.name) + '</span>' +
      '<span class="chip-size">' + formatFileSize(f.size) + '</span>' +
      '<span class="chip-remove" onclick="removeAttachedFile(' + i + ')">✕</span>' +
    '</span>'
  ).join('');
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
        showToast(err.message);
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

// ── Input handlers ──
promptEl.addEventListener('keydown', (e) => {
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
    if (historyIdx > 0) {
      e.preventDefault();
      historyIdx--;
      promptEl.value = history[historyIdx] || '';
      promptEl.selectionStart = promptEl.selectionEnd = promptEl.value.length;
    }
    return;
  }
  if (e.key === 'ArrowDown') {
    if (historyIdx < history.length - 1) {
      e.preventDefault();
      historyIdx++;
      promptEl.value = history[historyIdx] || '';
    } else {
      historyIdx = history.length;
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
  completionEl.querySelectorAll('.comp-item').forEach(el => el.classList.remove('selected'));
  item.classList.add('selected');
});

let lastAtIdx = -1;
let lastCursor = -1;
let compQuery = '';

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

  lastAtIdx = atIdx;
  lastCursor = cursor;
  compQuery = query;

  try {
    const resp = await fetch('/api/resources?q=' + encodeURIComponent(query) + '&limit=8');
    const results = await resp.json();
    if (!results || results.length === 0) {
      completionEl.classList.remove('visible');
      return;
    }

    completionEl.innerHTML = results.map((r, i) =>
      `<div class="comp-item${i === 0 ? ' selected' : ''}" data-id="${escapeAttr(r.id)}">
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

function selectCompletion() {
  const selected = completionEl.querySelector('.selected');
  if (!selected) return;
  replaceCompletion(selected.dataset.id);
  completionEl.classList.remove('visible');
}

function replaceCompletion(id) {
  promptEl.value = promptEl.value.slice(0, lastAtIdx) + id + ' ' + promptEl.value.slice(lastCursor);
  const newPos = lastAtIdx + id.length + 1;
  promptEl.selectionStart = promptEl.selectionEnd = newPos;
  promptEl.focus();
}

// ── Relative time helper ──
function relativeTime(dateStr) {
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

// ── Sessions ──
let allSessions = [];
let pendingDeleteId = null;

sessionListEl.addEventListener('click', (e) => {
  // Delete button
  if (e.target.classList.contains('del-btn')) {
    const item = e.target.closest('.session-item');
    if (!item) return;
    const sid = item.dataset.id;
    if (!sid) return;
    e.stopPropagation();
    pendingDeleteId = sid;
    document.getElementById('confirm-msg').textContent = 'Delete session ' + sid.slice(0, 8) + '...?';
    document.getElementById('confirm-overlay').classList.add('active');
    return;
  }

  // Rename button
  if (e.target.classList.contains('rename-btn')) return; // handled by inline onclick

  // Load and render session
  const item = e.target.closest('.session-item');
  if (!item) return;
  const sid = item.dataset.id;
  if (!sid || sid === sessionId) return;

  sessionListEl.querySelectorAll('.session-item').forEach(s => s.classList.remove('active'));
  item.classList.add('active');

  loadAndRenderSession(sid);
});

async function loadAndRenderSession(sid) {
  try {
    let token = getSessionToken(sid);
    const headers = token ? { 'X-Session-Token': token } : {};
    const resp = await fetch('/api/sessions/' + encodeURIComponent(sid), { headers });
    if (!resp.ok) { showToast('Failed to load session'); return; }
    const sess = await resp.json();

    // Persist the token returned by the server (bootstrapped for legacy
    // sessions, echoed for current ones).
    const returnedToken = resp.headers.get('X-Session-Token') || sess.auth_token;
    if (returnedToken) setSessionToken(sid, returnedToken);

    // Switch session ID so the next prompt continues this session.
    sessionId = sid;

    // Clear current messages and reset all streaming state.
    streamBuffer = '';
    if (streamRAF) { cancelAnimationFrame(streamRAF); streamRAF = null; }
    streamBubbleEl = null; streamContentEl = null;
    currentToolBlock = null; subagentGroup = null; thinkingContentEl = null;
    toolBlockQueues.clear(); toolStartQueues.clear(); inToolGroup = false;
    busy = false; hideLoading(); hideCancel();
    sendBtn.disabled = !ws || ws.readyState !== WebSocket.OPEN;
    promptEl.disabled = false;

    messagesEl.innerHTML = '';
    if (savedScrollBtnNode) messagesEl.appendChild(savedScrollBtnNode);

    const messages = sess.messages || [];
    // Only render user and assistant messages; skip system/tool internals.
    const visible = messages.filter(m => m.role === 'user' || m.role === 'assistant');

    if (visible.length === 0) {
      if (savedEmptyStateNode) messagesEl.appendChild(savedEmptyStateNode);
      showToast('Empty session');
      return;
    }

    visible.forEach(msg => {
      if (msg.role === 'user') {
        addMessage('user', stripAttachmentBodies(msg.content || ''));
      } else if (msg.role === 'assistant' && msg.content) {
        renderAssistantMessage(msg.content);
      }
    });

    forceScrollBottom();
    showToast('Session loaded');
  } catch (err) {
    showToast('Error loading session');
  }
}

// Replace inlined attachment blocks (--- name (size) ---\n...\n--- end name ---)
// with chip-style placeholders so reloaded user messages don't dump file bodies.
function stripAttachmentBodies(content) {
  if (!content) return '';
  const re = /^--- (.+?) \(([^)]+)\) ---\n[\s\S]*?\n--- end \1 ---\n?/gm;
  return content.replace(re, (m, name, size) => '📎 ' + name + ' (' + size + ')\n');
}

// Render a completed assistant message (not streaming) with copy button.
function renderAssistantMessage(content) {
  hideEmptyState();
  const wrapper = document.createElement('div');
  wrapper.className = 'msg assistant';
  wrapper.innerHTML =
    '<div class="bubble">' +
      '<div class="sender">assistant</div>' +
      '<div class="content">' + markdownToHtml(escapeHtml(content)) + '</div>' +
    '</div>';
  messagesEl.appendChild(wrapper);
  const bubble = wrapper.querySelector('.bubble');
  if (bubble) { addCopyButton(bubble); checkCollapse(bubble); }
  pruneMessages();
}

// Sidebar search
sidebarSearch.addEventListener('input', () => {
  const q = sidebarSearch.value.toLowerCase();
  const items = sessionListEl.querySelectorAll('.session-item');
  items.forEach(item => {
    const text = item.textContent.toLowerCase();
    item.style.display = text.includes(q) ? '' : 'none';
  });
});

async function loadSessions() {
  try {
    const resp = await fetch('/api/sessions');
    const sessions = await resp.json();
    if (!sessions || !Array.isArray(sessions)) return;
    allSessions = sessions;

    sessionListEl.innerHTML = sessions.map(s =>
      `<div class="session-item${s.id === sessionId ? ' active' : ''}" data-id="${escapeAttr(s.id)}">
        <div style="display:flex;align-items:center;gap:4px">
          <div class="id">${escapeHtml(s.id.slice(0, 8))}</div>
          <span class="rename-btn" title="Rename" onclick="event.stopPropagation();renameSession('${escapeAttr(s.id)}', this)">✎</span>
          <span class="del-btn" title="Delete">✕</span>
        </div>
        <div class="task${!s.task ? ' untitled' : ''}">${escapeHtml(s.task || 'untitled')}</div>
        <div class="meta">
          <span>${s.turns || 0} turn${s.turns !== 1 ? 's' : ''}</span><span>${relativeTime(s.updated_at)}</span>
          ${s.model ? `<span class="model-chip">${escapeHtml(s.model)}</span>` : ''}
        </div>
      </div>`
    ).join('');
  } catch { /* ignore */ }
}

// ── Init ──
// Save references so newSession() can restore the empty state after clearing.
savedEmptyStateNode = document.getElementById('empty-state');
savedScrollBtnNode = scrollBottomBtn;

// ── Thinking mode toggle ──────────────────────────────────────────────
const thinkBtn = document.getElementById('think-btn');

function syncThinkBtn() {
  if (!thinkBtn) return;
  thinkBtn.classList.toggle('active', thinkingEnabled);
  thinkBtn.title = thinkingEnabled
    ? 'Thinking ON — click to disable extended reasoning'
    : 'Thinking OFF — click to enable extended reasoning';
}

window.toggleThinkingMode = function() {
  thinkingEnabled = !thinkingEnabled;
  localStorage.setItem('odek_thinking', thinkingEnabled ? '1' : '0');
  syncThinkBtn();
  showToast(thinkingEnabled ? '🧠 Thinking enabled' : 'Thinking off');
};

syncThinkBtn(); // restore persisted state on load

connect();
// Show skeleton while connecting
if (skeletonEl) skeletonEl.classList.add('visible');
loadSessions();
fetchModels();
promptEl.focus();

// Handle keyboard shortcuts globally
document.addEventListener('keydown', (e) => {
  // Escape closes modals
  if (e.key === 'Escape') {
    document.getElementById('shortcuts-overlay').classList.remove('active');
    document.getElementById('approval-overlay').classList.remove('active');
    document.getElementById('confirm-overlay').classList.remove('active');
    pendingDeleteId = null;
  }
  // Ctrl+R refreshes sessions
  if (e.key === 'r' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault();
    loadSessions();
    showToast('Sessions refreshed');
  }
  // Alt+T toggles thinking mode
  if (e.key === 't' && e.altKey && !busy) {
    e.preventDefault();
    window.toggleThinkingMode();
  }
});


// ── Phase 1: Cancel Button ──
function showCancel() {
  if (cancelBtn) cancelBtn.classList.add('visible');
}
function hideCancel() {
  if (cancelBtn) cancelBtn.classList.remove('visible');
}
window.cancelAgent = function() {
  if (!sessionId) {
    hideCancel();
    addSystemMessage('⏹ No active session to cancel');
    return;
  }
  fetch('/api/cancel?session_id=' + encodeURIComponent(sessionId), {
    method: 'POST',
    headers: { 'X-Session-Token': getSessionToken(sessionId) || '' }
  }).catch(function(){});
  hideCancel();
  addSystemMessage('⏹ Canceled');
};

// ── Phase 1: Scroll to Bottom ──
window.scrollToBottom = function() {
  messagesEl.scrollTop = messagesEl.scrollHeight;
  if (scrollBottomBtn) scrollBottomBtn.classList.remove('visible');
};

// ── Phase 1: Sidebar Toggle (mobile) ──
window.toggleSidebar = function() {
  var sidebar = document.getElementById('sidebar');
  if (!sidebar) return;
  sidebar.classList.toggle('active');
  if (sidebarOverlay) sidebarOverlay.classList.toggle('active');
};

// ── Phase 1: Collapse Long Messages ──
window.toggleCollapse = function(el) {
  var bubble = el.closest('.bubble');
  if (!bubble) return;
  bubble.classList.toggle('expanded');
  el.textContent = bubble.classList.contains('expanded') ? 'Show less ▲' : 'Show more ▼';
};

// ── Phase 1: Copy Message ──
window.copyMessage = function(btn, content) {
  if (!content) {
    var bubble = btn.closest('.bubble');
    if (bubble) {
      var contentEl = bubble.querySelector('.content');
      content = contentEl ? contentEl.textContent : '';
    }
  }
  if (!content) return;
  navigator.clipboard.writeText(content).then(function() {
    btn.classList.add('copied');
    btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg> Copied';
    setTimeout(function() {
      btn.classList.remove('copied');
      btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
    }, 2000);
  }).catch(function(){});
};

// ── Phase 1: Add copy buttons to rendered messages ──
function addCopyButton(bubble) {
  if (bubble.querySelector('.copy-btn')) return;
  var btn = document.createElement('button');
  btn.className = 'copy-btn';
  btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
  btn.title = 'Copy message';
  btn.onclick = function() { copyMessage(this); };
  bubble.appendChild(btn);
}

// ── Phase 1: Collapse long messages after rendering ──
function checkCollapse(bubble) {
  var content = bubble.querySelector('.content');
  if (!content || content.scrollHeight <= 500) return;
  bubble.classList.add('collapsible');
  var toggle = document.createElement('div');
  toggle.className = 'collapse-toggle';
  toggle.textContent = 'Show more ▼';
  toggle.onclick = function() { toggleCollapse(this); };
  bubble.appendChild(toggle);
}

// Patch addMessage to add copy button and collapse check
var origAddMessage = addMessage;
addMessage = function(role, content) {
  origAddMessage(role, content);
  var msgs = messagesEl.querySelectorAll('.msg .bubble');
  if (msgs.length > 0) {
    var last = msgs[msgs.length - 1];
    addCopyButton(last);
    checkCollapse(last);
  }
};


})();
