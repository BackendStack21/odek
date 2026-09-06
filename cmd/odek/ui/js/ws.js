// WebSocket connection and the server-event dispatch switch (protocol v2).
// New in v2: token_delta/thinking_delta live streaming, pong heartbeat
// replies carrying server info, cancelled confirmations, and the server_info
// hello pushed on connect.
import { S, setSessionToken, getSessionToken } from './state.js';
import { getWsToken } from './net.js';
import { dotEl, statusEl, sendBtn, skeletonEl, messagesEl, modelLabel, promptEl } from './dom.js';
import { formatNum, formatErrorMessage, showToast, announce, showCancel } from './utils.js';
import {
  streamToken, streamThinking, streamFlush, endThinking, endStream,
  addToolCall, addToolResult, addSubagentGroup, completeSubagents,
  appendSubagentLog, addSystemMessage, updateSubagentState,
} from './render.js';
import { queueApproval, dismissApproval, clearApprovals, expireApproval } from './approvals.js';
import { loadSessions } from './sessions.js';
import { onPong, onServerInfo, startHeartbeat, stopHeartbeat, notifyUser } from './health.js';
import { metricsLiveContext, metricsDone, flashTrim, turnCostUSD, setMetricsModel } from './metrics.js';
import { drainQueue } from './input.js';
import { setIntent, openTurn, markWakeTurn, sealTurn, paintIntent } from './render.js';
import { badgeNow } from './panels.js';
import { applyPlanMutation, schedulePlanRefresh, kickPlanLive, stopPlanLiveIfIdle, resetPlanPanel, fetchPlanSnapshot } from './plan.js';
import { listJobs } from './api.js';

// Reconnect backoff: 1s doubling to a 30s cap; reset after a clean interval
// of connected silence.
let reconnectDelay = 1000;
// F-A3: flipped after the first successful connect, so a LATER onopen is a
// reconnect — the previous turn died with the socket, and the input must be
// unbricked instead of waiting for a 'done' that never comes.
let wasConnected = false;

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
    if (wasConnected) {
      // F-A3: reconnect, not first connect. A turn in flight died with the
      // old socket — reset busy and re-enable the prompt, or the client
      // stays bricked until reload. The turn's partial output stays in the
      // transcript; only the completion is missing.
      S.busy = false;
      promptEl.disabled = false;
      addSystemMessage('Connection restored — the previous turn ended before completion.');
      // Re-adopt the session so the new connection's agent gets the memory
      // buffer (bodek does this; the old WebUI did not).
      if (S.sessionId) {
        wsSend({
          type: 'session_switch',
          session_id: S.sessionId,
          auth_token: getSessionToken(S.sessionId) || undefined,
        });
      }
    }
    wasConnected = true;
    // Connection state is visual (status lamp); #sr-status is turn lifecycle only.
    startHeartbeat();
  };

  S.ws.onclose = () => {
    stopHeartbeat();
    dotEl.className = 'dot disconnected';
    statusEl.textContent = 'reconnecting...';
    sendBtn.disabled = true;
    // Reconnect is visual; do not narrate the status lamp.
    setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  };

  S.ws.onerror = () => {
    // A transport error is terminal for this socket; close() is idempotent
    // and onclose owns the reconnect loop so we do not double-schedule.
    if (S.ws && S.ws.readyState === WebSocket.OPEN) S.ws.close();
  };

  S.ws.onmessage = (e) => {
    let event;
    try { event = JSON.parse(e.data); } catch { return; }

    const sameTurn = !event.turn_id || !S.currentTurnId || event.turn_id === S.currentTurnId;

    switch (event.type) {
      case 'server_info':
        onServerInfo(event);
        break;

      case 'turn_started':
        S.currentTurnId = event.turn_id || null;
        S.currentTurnInitiated = event.initiated || 'operator';
        openTurn(event);
        if (event.initiated === 'system') markWakeTurn(event);
        // Wake/remote turns never go through sendPayload — arm busy so
        // the Bodek plan strip and 1s live poll can start on this frame.
        if (!S.busy) {
          S.busy = true;
          if (!S.runStartedAt) S.runStartedAt = Date.now();
        }
        showCancel();
        kickPlanLive();
        paintIntent();
        badgeNow();
        announce('Turn started');
        break;

      case 'bg_job':
        upsertJob(event);
        kickJobsFetch();
        break;

      case 'bg_wake':
        setIntent('wake — background job finished');
        break;

      case 'session': {
        const prevSid = S.sessionId;
        S.sessionId = event.session_id || null;
        if (prevSid && prevSid !== S.sessionId) {
          S.jobs = [];
          resetPlanPanel();
        }
        if (event.auth_token) setSessionToken(S.sessionId, event.auth_token);
        startJobsWatch();
        kickJobsFetch();
        fetchPlanSnapshot();
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
        if (sandboxBadge) sandboxBadge.hidden = !event.sandbox;
        loadSessions();
        break;
      }

      // ── Live streaming fragments (protocol v2) ──
      // token_delta appends to the streaming answer bubble through the same
      // rAF-batched pipeline the bulk token event used; thinking_delta
      // appends to the collapsible reasoning block.
      case 'token_delta':
        if (!sameTurn) break;
        setIntent('composing');
        streamToken(event.content);
        break;

      case 'thinking_delta':
        if (!sameTurn) break;
        setIntent('reasoning');
        streamThinking(event.content);
        break;

      case 'token':
        if (!sameTurn) break;
        setIntent('composing');
        streamToken(event.content);
        break;

      case 'thinking':
        if (!sameTurn) break;
        setIntent('reasoning');
        streamThinking(event.content);
        break;

      case 'tool_call':
        if (!sameTurn) break;
        streamFlush();
        endThinking();
        setIntent(toolProgress(event.name, event.data));
        if (event.name === 'plan') applyPlanMutation(event.data);
        if (event.name === 'delegate_tasks') {
          addSubagentGroup(event.data);
        } else {
          addToolCall(event.name, event.data);
        }
        break;

      case 'tool_result':
        if (!sameTurn) break;
        // F-B1: the delegate_tasks result is rendered by the subagent group
        // (per-card results) — routing it through addToolResult too leaked
        // the full payload into an unrelated tool block.
        if (event.name === 'plan') schedulePlanRefresh();
        if (event.name === 'delegate_tasks' && S.subagentGroup) {
          completeSubagents(event.data);
        } else {
          addToolResult(event.name, event.data);
        }
        break;

      case 'subagent_log':
        appendSubagentLog(event.task_idx, event);
        break;

      case 'subagent_state':
        updateSubagentState(event);
        break;

      case 'usage':
        // Per-iteration usage — feeds the live context gauge while the
        // run is in flight (final totals arrive on done). windowTokens is
        // the PARENT conversation window (last parent call's prompt);
        // sub-agent spend never appears in it. maxContextTokens is the
        // server-resolved model limit — beats the /api/models table.
        S.runIterations = (S.runIterations || 0) + 1;
        metricsLiveContext(event.windowTokens, event.maxContextTokens);
        break;

      case 'pong':
        onPong(event);
        break;

      case 'keepalive':
        // Server-initiated idle traffic (proxies). Not a ping reply.
        break;

      case 'cancelled':
        streamFlush();
        endThinking();
        endStream();
        setIntent('');
        // The run is unwinding — drop every pending approval card so a
        // stray click cannot approve an operation whose execution context
        // is already dead (the server interrupts the approval wait on
        // cancel, but the card would stay rendered waiting for an ack that
        // never comes). Same teardown approval_ack uses, minus the ack.
        clearApprovals();
        stopPlanLiveIfIdle();
        addSystemMessage(event.idle ? '⏹ Nothing to cancel' : '⏹ Cancelled');
        badgeNow();
        announce('Turn cancelled');
        break;

      case 'subagent_cancelled':
        // Ack for a per-card stop. accepted=false is a benign race (the
        // task finished before the stop landed); the terminal card state
        // arrives via subagent_state finished/cancelled either way.
        showToast(event.accepted ? '⏹ Sub-agent stop requested' : 'ℹ️ Sub-agent already finished');
        break;

      case 'done':
        if (!sameTurn) break;
        streamFlush();
        endThinking();
        sealTurn(event);
        endStream();
        setIntent('');
        stopPlanLiveIfIdle();
        notifyUser('turn done', 'Turn finished');
        badgeNow();
        announce('Turn complete');
        drainQueue();
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
            if (event.inputTokens != null) spans.push('<span title="Input tokens (run total, incl. sub-agents)">⌂ ' + formatNum(event.inputTokens) + '</span>');
            if (event.outputTokens != null) spans.push('<span title="Output tokens (completion)">↳ ' + formatNum(event.outputTokens) + '</span>');
            const cache = (event.cacheReadTokens || 0) + (event.cacheCreationTokens || 0) + (event.cachedTokens || 0);
            if (cache > 0) spans.push('<span title="Cached tokens">⛁ ' + formatNum(cache) + '</span>');
            const turnCost = turnCostUSD(event.inputTokens || 0, event.outputTokens || 0);
            if (turnCost != null && turnCost > 0) {
              spans.push('<span title="Estimated cost of this turn at current prices">$ ' + turnCost.toFixed(4) + '</span>');
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
        if (!sameTurn) break;
        streamFlush(); endThinking(); endStream();
        setIntent('');
        S.lastFailedPrompt = S.lastPrompt;
        // The run is unwinding on error — same approval teardown as
        // 'cancelled': a pending card would wait for an ack that never
        // comes and block the queue.
        clearApprovals();
        addSystemMessage('⚠ ' + formatErrorMessage(event.message) + ' — Alt+R to retry');
        stopPlanLiveIfIdle();
        notifyUser('turn failed', 'A turn failed');
        badgeNow();
        announce('Turn failed');
        break;

      case 'approval_request':
        queueApproval(event);
        notifyUser('approval needed', 'Approval required');
        break;

      case 'approval_ack':
        // The request was answered (by this or another connected client);
        // drop it from the queue if it is still shown.
        dismissApproval(event.id);
        break;

      case 'approval_expired':
        // The server killed this approval after its timeout (F-A1). Close
        // the matching card; ids already answered or swept are no-ops, so
        // late or duplicate frames can never resurrect a closed card.
        expireApproval(event.id);
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
// Remaining kinds after the self-learning removal: loaded/autoloaded
// (silent — noisy per turn) and deleted (CLI `odek skill delete`).
function handleSkillEvent(event) {
  switch (event.event) {
    case 'deleted':
      showToast('✗ Skill deleted: ' + (event.skill_name || ''));
      break;
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

// Bodek toolProgress — one calm label for the status line (progress.go).
function toolProgress(name, data) {
  let arg = '';
  try {
    const obj = JSON.parse(data || '{}');
    arg = String(obj.command || obj.path || obj.file || obj.pattern || obj.query || obj.url || '');
  } catch { arg = ''; }
  const n = String(name || '').toLowerCase();
  if (n.includes('shell') || n.includes('bash') || n.includes('exec')) return shellProgress(arg);
  if (n.includes('web_search')) return '🔎 searching the web';
  if (n.includes('search') || n.includes('grep') || n.includes('find')) return '🔎 searching the code';
  if (n.includes('browser') || n.includes('http') || n.includes('fetch') || n.includes('web')) return '🌐 browsing the web';
  if (n.includes('read')) return '📖 reading ' + baseName(arg);
  if (n.includes('write') || n.includes('patch') || n.includes('edit')) return '📝 writing ' + baseName(arg);
  if (n.includes('list') || n.includes('dir')) return '📂 listing ' + baseName(arg);
  if (n.includes('delegate') || n.includes('subagent') || n.includes('task')) return '🤝 delegating to a sub-agent';
  if (n.includes('memory') || n.includes('recall')) return '🧠 recalling from memory';
  if (n.includes('vision') || n.includes('image') || n.includes('transcribe')) return '🎬 examining media';
  return '🔧 running ' + name;
}

function shellProgress(cmd) {
  const c = String(cmd || '').toLowerCase().trim();
  if (!c) return '❯ running a command';
  if (hasAny(c, 'go test', 'npm test', 'pytest', 'cargo test', 'jest') || c.startsWith('test ')) return '🧪 running tests';
  if (c.startsWith('git ')) return gitProgress(c);
  if (hasAny(c, 'lint', 'vet', 'gofmt', 'prettier', 'ruff')) return '🧹 linting';
  if (hasAny(c, 'build', 'compile') || c.startsWith('make') || c.startsWith('cargo b')) return '🔨 building';
  if (hasAny(c, 'install', 'go mod', 'npm i', 'yarn', 'pip ', 'apt', 'brew')) return '📦 installing dependencies';
  if (hasAny(c, 'docker', 'kubectl', 'helm')) return '🐳 working with containers';
  if (c.startsWith('curl') || c.startsWith('wget')) return '🌐 fetching';
  if (c.startsWith('ls') || c.startsWith('find') || c.startsWith('tree')) return '📂 looking around';
  if (c.includes('grep') || prefixAny(c, 'cat', 'head', 'tail', 'less', 'wc')) return '🔎 inspecting output';
  if (prefixAny(c, 'rm', 'mv', 'cp', 'mkdir', 'touch', 'chmod')) return '🗂 managing files';
  return '❯ ' + truncateLabel(c.replace(/\s+/g, ' '), 28);
}

function gitProgress(c) {
  if (c.includes('commit')) return '📌 committing';
  if (c.includes('push')) return '🚀 pushing';
  if (c.includes('clone')) return '📥 cloning';
  if (c.includes('pull') || c.includes('fetch')) return '🔄 syncing with remote';
  if (c.includes('checkout') || c.includes('switch') || c.includes('branch')) return '🌿 switching branches';
  if (c.includes('merge') || c.includes('rebase')) return '🔀 merging';
  return '🔀 checking git';
}

function baseName(p) {
  const s = String(p || '').trim();
  if (!s) return 'a file';
  const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'));
  return truncateLabel(i >= 0 ? s.slice(i + 1) : s, 28);
}

function hasAny(s, ...subs) {
  return subs.some((sub) => s.includes(sub));
}

function prefixAny(s, ...cmds) {
  return cmds.some((cmd) => s === cmd || s.startsWith(cmd + ' '));
}

function truncateLabel(s, n) {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

function upsertJob(event) {
  if (!event || !event.job_id) return;
  const jobs = S.jobs || [];
  const idx = jobs.findIndex((j) => j.id === event.job_id);
  const row = {
    id: event.job_id,
    command: event.command_head || (jobs[idx] && jobs[idx].command) || '',
    status: event.status || 'running',
    exit_code: event.exit_code,
    runtime_s: event.duration_ms != null ? event.duration_ms / 1000 : undefined,
  };
  if (idx >= 0) jobs[idx] = { ...jobs[idx], ...row };
  else jobs.unshift(row);
  S.jobs = jobs.slice(0, 40);
  paintIntent();
  badgeNow();
  if (event.status && event.status !== 'running') {
    notifyUser('job finished', 'Background job ' + (event.status || 'exited'));
  }
}

// Bodek jobs watcher: 10s while a session is attached (header chips +
// completion notes), 3s when Now is already polling. bg_job kicks now.
const JOBS_WATCH_MS = 10000;
let jobsWatchTimer = null;

function nowTabPolling() {
  const panels = document.getElementById('panels');
  if (!panels || !panels.classList || !panels.classList.contains('active')) return false;
  const tab = panels.querySelector && panels.querySelector('.ptab.active');
  return !!(tab && tab.dataset.tab === 'now');
}

function startJobsWatch() {
  if (jobsWatchTimer) return;
  jobsWatchTimer = setInterval(() => {
    if (!S.sessionId || document.hidden || nowTabPolling()) return;
    kickJobsFetch();
  }, JOBS_WATCH_MS);
}

function kickJobsFetch() {
  const sid = S.sessionId;
  if (!sid) return;
  listJobs(getSessionToken(sid) || undefined).then((data) => {
    if (S.sessionId !== sid) return;
    S.jobs = (data && data.jobs) || [];
    paintIntent();
    badgeNow();
  }).catch(() => {});
}

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
