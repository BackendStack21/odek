// WebSocket connection and the server-event dispatch switch (protocol v2).
// New in v2: token_delta/thinking_delta live streaming, pong heartbeat
// replies carrying server info, cancelled confirmations, and the server_info
// hello pushed on connect.
import { S, setSessionToken } from './state.js';
import { getWsToken } from './net.js';
import { dotEl, statusEl, sendBtn, skeletonEl, messagesEl, modelLabel } from './dom.js';
import { formatNum, formatErrorMessage, showToast, scrollBottom, escapeHtml, announce } from './utils.js';
import {
  streamToken, streamThinking, streamFlush, endThinking, endStream,
  addToolCall, addToolResult, addSubagentGroup, completeSubagents,
  appendSubagentLog, addSystemMessage, updateSubagentState,
} from './render.js';
import { queueApproval, dismissApproval, clearApprovals } from './approvals.js';
import { loadSessions } from './sessions.js';
import { onPong, onServerInfo, startHeartbeat } from './health.js';
import { metricsLiveContext, metricsDone, flashTrim, turnCostUSD, setMetricsModel } from './metrics.js';

// Reconnect backoff: 1s doubling to a 30s cap; reset after a clean interval
// of connected silence.
let reconnectDelay = 1000;

export function connect() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const token = getWsToken();
  const protocols = token ? ['odek.' + token] : [];
  S.ws = new WebSocket(proto + '//' + location.host + '/ws', protocols);

  S.ws.onopen = () => {
    dotEl.className = 'dot connected';
    statusEl.textContent = 'connected';
    sendBtn.disabled = false;
    reconnectDelay = 1000;
    // Hide loading skeleton when connected
    if (skeletonEl) skeletonEl.classList.remove('visible');
    announce('Connected');
    startHeartbeat();
  };

  S.ws.onclose = () => {
    dotEl.className = 'dot disconnected';
    statusEl.textContent = 'reconnecting...';
    sendBtn.disabled = true;
    announce('Connection lost — reconnecting');
    setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  };

  S.ws.onerror = () => { S.ws.close(); };

  S.ws.onmessage = (e) => {
    let event;
    try { event = JSON.parse(e.data); } catch { return; }

    switch (event.type) {
      case 'server_info':
        onServerInfo(event);
        break;

      case 'session':
        S.sessionId = event.session_id || null;
        if (event.auth_token) setSessionToken(S.sessionId, event.auth_token);
        // Only adopt the server's model on the very first session event
        // (no user-selected model yet). After that the user's choice wins.
        if (event.model && !S.currentModel) {
          S.currentModel = event.model;
          setMetricsModel(event.model);
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

      // ── Live streaming fragments (protocol v2) ──
      // token_delta appends to the streaming answer bubble through the same
      // rAF-batched pipeline the bulk token event used; thinking_delta
      // appends to the collapsible reasoning block.
      case 'token_delta':
        streamToken(event.content);
        break;

      case 'thinking_delta':
        streamThinking(event.content);
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

      case 'subagent_state':
        updateSubagentState(event);
        break;

      case 'usage':
        // Per-iteration usage — feeds the live context gauge while the
        // run is in flight (final totals arrive on done).
        S.runIterations = (S.runIterations || 0) + 1;
        metricsLiveContext(event.contextTokens);
        break;

      case 'pong':
        onPong(event);
        break;

      case 'cancelled':
        streamFlush();
        endThinking();
        endStream();
        // The run is unwinding — drop every pending approval card so a
        // stray click cannot approve an operation whose execution context
        // is already dead (the server interrupts the approval wait on
        // cancel, but the card would stay rendered waiting for an ack that
        // never comes). Same teardown approval_ack uses, minus the ack.
        clearApprovals();
        addSystemMessage(event.idle ? '⏹ Nothing to cancel' : '⏹ Cancelled');
        break;

      case 'subagent_cancelled':
        // Ack for a per-card stop. accepted=false is a benign race (the
        // task finished before the stop landed); the terminal card state
        // arrives via subagent_state finished/cancelled either way.
        showToast(event.accepted ? '⏹ Sub-agent stop requested' : 'ℹ️ Sub-agent already finished');
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
            const turnCost = turnCostUSD(event.contextTokens || 0, event.outputTokens || 0);
            if (turnCost != null && turnCost > 0) {
              spans.push('<span title="Estimated cost of this turn at current prices">◈ $' + turnCost.toFixed(4) + '</span>');
            }
            stats.innerHTML = spans.join('  ·  ');
            lastAssistant.appendChild(stats);
          }
        }
        // Consolidated metrics (context gauge + session tokens + cost).
        metricsDone(event);
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

// wsSend safely sends a JSON message when the socket is open.
export function wsSend(obj) {
  if (S.ws && S.ws.readyState === WebSocket.OPEN) {
    S.ws.send(JSON.stringify(obj));
    return true;
  }
  return false;
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
      flashTrim();
      showToast('✂️ Context trimmed (' + (event.detail || '') + '): ' +
        (event.count || 0) + ' group(s) dropped');
      break;
    case 'tool_recovery':
      showToast('🔁 Tool recovery: ' + (event.tool || ''));
      break;
  }
}
