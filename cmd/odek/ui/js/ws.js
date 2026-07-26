// WebSocket connection and the server-event dispatch switch.
import { S, setSessionToken } from './state.js';
import { getWsToken } from './net.js';
import { dotEl, statusEl, sendBtn, skeletonEl, messagesEl, modelLabel } from './dom.js';
import { formatNum, formatErrorMessage, showToast, scrollBottom, escapeHtml } from './utils.js';
import {
  streamToken, streamThinking, streamFlush, endThinking, endStream,
  addToolCall, addToolResult, addSubagentGroup, completeSubagents,
  appendSubagentLog, addSystemMessage,
} from './render.js';
import { queueApproval, dismissApproval } from './approvals.js';
import { loadSessions } from './sessions.js';

export function connect() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const token = getWsToken();
  const protocols = token ? ['odek.' + token] : [];
  S.ws = new WebSocket(proto + '//' + location.host + '/ws', protocols);

  S.ws.onopen = () => {
    dotEl.className = 'dot connected';
    statusEl.textContent = 'connected';
    sendBtn.disabled = false;
    // Hide loading skeleton when connected
    if (skeletonEl) skeletonEl.classList.remove('visible');
  };

  S.ws.onclose = () => {
    dotEl.className = 'dot disconnected';
    statusEl.textContent = 'reconnecting...';
    sendBtn.disabled = true;
    setTimeout(connect, 2000);
  };

  S.ws.onerror = () => { S.ws.close(); };

  S.ws.onmessage = (e) => {
    let event;
    try { event = JSON.parse(e.data); } catch { return; }

    switch (event.type) {
      case 'session':
        S.sessionId = event.session_id || null;
        if (event.auth_token) setSessionToken(S.sessionId, event.auth_token);
        // Only adopt the server's model on the very first session event
        // (no user-selected model yet). After that the user's choice wins.
        if (event.model && !S.currentModel) {
          S.currentModel = event.model;
          const picker = document.getElementById('model-picker');
          if (picker && picker.value !== event.model) picker.value = event.model;
        }
        modelLabel.textContent = S.currentModel || event.model || '';
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
        if (event.name === 'delegate_tasks' && S.subagentGroup) {
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
            const lat = Number(event.latency);
            const latSafe = isFinite(lat) ? lat : 0;
            const spans = [];
            spans.push('<span title="Response time">⚡ ' + (latSafe < 1 ? (latSafe * 1000).toFixed(0) + 'ms' : latSafe.toFixed(1) + 's') + '</span>');
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
        if (S.sessionId) loadSessions();
        break;

      case 'error':
        streamFlush(); endThinking(); endStream();
        addSystemMessage('⚠ ' + formatErrorMessage(event.message));
        break;

      case 'approval_request':
        queueApproval(event);
        break;

      case 'approval_ack':
        // The request was answered (by this or another connected client);
        // drop it from the queue if it is still shown.
        dismissApproval(event.id);
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
