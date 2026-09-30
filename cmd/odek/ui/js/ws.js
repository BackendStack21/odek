// WebSocket connection and the server-event dispatch switch (protocol v2).
// New in v2: token_delta/thinking_delta live streaming, pong heartbeat
// replies carrying server info, cancelled confirmations, and the server_info
// hello pushed on connect.
import { S, setSessionToken, getSessionToken } from './state.js';
import { getWsToken } from './net.js';
import { dotEl, statusEl, sendBtn, skeletonEl, modelLabel, promptEl } from './dom.js';
import { formatErrorMessage, showToast, announce, showCancel } from './utils.js';
import {
  streamToken, streamThinking, streamFlush, endThinking, endStream,
  addToolCall, addToolResult, addSubagentGroup, completeSubagents,
  appendSubagentLog, addSystemMessage, updateSubagentState,
  lastAssistantBubble,
} from './render.js';
import { queueApproval, dismissApproval, clearApprovals, expireApproval } from './approvals.js';
import { queueClarify, dismissClarify, clearClarify, expireClarify } from './clarify.js';
import { loadSessions } from './sessions.js';
import { onPong, onServerInfo, startHeartbeat, stopHeartbeat, notifyUser } from './health.js';
import { metricsLiveContext, metricsDone, metricsApplySpeed, metricsResetSpeed, metricsBeginTurn, metricsLiveUsage, turnStatsSpans, setMetricsModel } from './metrics.js';
import { drainQueue } from './input.js';
import { setIntent, openTurn, markWakeTurn, sealTurn, paintIntent } from './render.js';
import { badgeNow } from './panels.js';
import { schedulePlanRefresh, kickPlanLive, stopPlanLiveIfIdle, resetPlanPanel, fetchPlanSnapshot } from './plan.js';
import { listJobs } from './api.js';

// Reconnect backoff: 1s doubling to a 30s cap; reset after a clean interval
// of connected silence.
let reconnectDelay = 1000;
// F-A3: flipped after the first successful connect, so a LATER onopen is a
// reconnect — the previous turn died with the socket, and the input must be
// unbricked instead of waiting for a 'done' that never comes.
let wasConnected = false;
let droppedBusy = false;
let reconnectTimer = null;

function connBannerEl() {
  return document.getElementById('conn-banner');
}

function paintConnBanner() {
  const el = connBannerEl();
  if (!el) return;
  el.hidden = false;
  const existing=el.querySelector('.connection-retry');if(existing){existing.disabled=false;return;}
  el.textContent = 'Connection unavailable. ';
  const retry=document.createElement('button');retry.type='button';retry.className='connection-retry';retry.textContent='Reconnect now';
  retry.addEventListener('click',()=>{retry.disabled=true;connect();});el.appendChild(retry);
}

function hideConnBanner() {
  const el = connBannerEl();
  if (!el) return;
  if(el.contains?.(document.activeElement))promptEl.focus();
  el.hidden = true;
  el.textContent = '';
}

function noteDisconnect() {
  stopHeartbeat();
  if (dotEl) dotEl.className = 'dot disconnected';
  if (statusEl) statusEl.textContent = 'reconnecting';
  sendBtn.disabled = true;

  droppedBusy = droppedBusy || !!S.busy;
  if (droppedBusy) S.pauseQueue?.();
  streamFlush();
  endThinking();
  endStream();
  // Same teardown as cancelled/error: the approval/clarify wait died with
  // the socket. Leave the prompt queue; drainQueue no-ops until restore.
  clearApprovals({ drain: false });
  clearClarify();
  stopPlanLiveIfIdle();

  paintConnBanner();

  if (droppedBusy) S.onRecoveryNeeded?.('Connection interrupted the turn');
}

export function connect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (connBannerEl() && !connBannerEl().hidden) paintConnBanner();
  const retry=connBannerEl()?.querySelector('.connection-retry');if(retry)retry.disabled=true;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const token = getWsToken();
  const protocols = token ? ['odek.' + token] : [];
  S.ws = new WebSocket(proto + '//' + location.host + '/ws', protocols);

  S.ws.onopen = () => {
    hideConnBanner();
    dotEl.className = 'dot connected';
    statusEl.textContent = 'connected';
    sendBtn.disabled = !!S.uploading;
    reconnectDelay = 1000;
    // Hide loading skeleton when connected
    if (skeletonEl) skeletonEl.classList.remove('visible');
    S.refreshPermissions?.();
    if (wasConnected) {
      // F-A3: reconnect, not first connect. A turn in flight died with the
      // old socket — reset busy and re-enable the prompt, or the client
      // stays bricked until reload. The turn's partial output stays in the
      // transcript; only the completion is missing.
      S.busy = false;
      promptEl.disabled = false;
      if(droppedBusy){
        addSystemMessage('Work was interrupted. Review saved progress before continuing.');
        announce('Work was interrupted. Review saved progress.');
        S.onRecoveryNeeded?.('Connection interrupted the turn');
      }
      droppedBusy = false;
      // Re-adopt the session so the new connection's agent gets the memory
      // buffer (bodek does this; the old WebUI did not).
      if (S.sessionId) {
        wsSend({
          type: 'session_switch',
          session_id: S.sessionId,
          auth_token: getSessionToken(S.sessionId) || undefined,
        });
      }
      drainQueue();
    }
    wasConnected = true;
    startHeartbeat();
  };

  S.ws.onclose = () => {
    noteDisconnect();
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, reconnectDelay);
    if (reconnectTimer && typeof reconnectTimer.unref === 'function') reconnectTimer.unref();
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

    if (event.type!=='turn_settled' && event.turn_id && S.closedTurnIds?.has(event.turn_id)) return;
    if (S.turnEnded && ['tool_call','tool_result','thinking','thinking_delta','token','token_delta','subagent_state','subagent_log','approval_request','clarify_request','done'].includes(event.type)) return;
    if (S.stopRequested && ['tool_call','subagent_log','thinking','thinking_delta','approval_request','clarify_request'].includes(event.type)) return;
    if (S.stopRequested && event.type === 'subagent_state' && event.phase !== 'finished') return;
    const sameTurn = !event.turn_id || !S.currentTurnId || event.turn_id === S.currentTurnId;

    switch (event.type) {
      case 'server_info':
        onServerInfo(event);
        break;

      case 'turn_started':
        S.currentTurnId = event.turn_id || null;
        S.currentTurnInitiated = event.initiated || 'operator';
        metricsResetSpeed();
        metricsBeginTurn();
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
        break;

      case 'artifact':
        if (sameTurn) S.onArtifact?.(event.artifact);
        break;

      case 'runtime_event':
        if (sameTurn && typeof S.onRuntimeEvent === 'function') S.onRuntimeEvent(event.event);
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
        if (!prevSid && S.sessionId) S.promptQueue.forEach(item => { if (item.session_id == null) item.session_id = S.sessionId; });
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
      // token_delta is visible assistant text (not reasoning). Each burst
      // is a timeline row; a following tool_call seals that row so the next
      // tokens open a new one. thinking_delta is the italic reasoning log.
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
        if (event.name === 'plan') schedulePlanRefresh();
        if (event.name === 'delegate_tasks') {
          addSubagentGroup(event.data);
        } else {
          addToolCall(event.name, event.data, event.call_id);
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
          addToolResult(event.name, event.data, event.call_id, event.outcome);
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
        metricsLiveUsage(event);
        metricsApplySpeed(event);
        break;

      case 'pong':
        onPong(event);
        break;

      case 'keepalive':
        // Server-initiated idle traffic (proxies). Not a ping reply.
        break;

      case 'cancelled':
        if (!sameTurn || (event.session_id && S.sessionId && event.session_id !== S.sessionId)) break;
        S.pauseQueue?.();
        if(event.idle){clearApprovals({drain:false});clearClarify();endStream('cancelled');addSystemMessage('No active execution to stop.');break;}
        S.stopRequested=true;setIntent('stopping');
        clearApprovals({drain:false});clearClarify();
        if(event.requested){addSystemMessage('Stop accepted · waiting for execution to settle.');announce('Stop requested');}
        else {endStream('interrupted');addSystemMessage('Stop outcome requires review.');S.onRecoveryNeeded?.('Stop outcome requires review');}
        S.refreshSupervision?.();
        break;

      case 'turn_settled':
        if(event.session_id && S.sessionId && event.session_id!==S.sessionId)break;
        if(event.turn_id && S.currentTurnId && event.turn_id!==S.currentTurnId)break;
        if(S.stopRequested && event.status==='cancelled'){
          streamFlush();endThinking();endStream('cancelled');addSystemMessage('Execution stopped. Review saved progress before continuing.');announce('Execution stopped');
          S.onRecoveryNeeded?.('Execution stopped');kickJobsFetch();
        } else if(event.status!=='completed') {S.onRecoveryNeeded?.('Work interrupted');}
        S.paintEvidence?.();S.refreshSupervision?.();
        break;

      case 'permissions':
        S.onPermissions?.(event);
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
        S.paintEvidence?.();S.refreshSupervision?.();
        drainQueue();
        // Append per-message stats to the last assistant bubble. Built via
        // textContent/setAttribute (never innerHTML) so server-controlled
        // values cannot be reinterpreted as markup even if a future field
        // loses its numeric coercion upstream.
        const statsSpans = turnStatsSpans(event);
        if (statsSpans.length) {
          const lastAssistant = lastAssistantBubble();
          if (lastAssistant) {
            const stats = document.createElement('div');
            stats.className = 'msg-stats';
            statsSpans.forEach((sp, i) => {
              if (i > 0) stats.appendChild(document.createTextNode('  ·  '));
              const span = document.createElement('span');
              span.title = sp.title;
              span.textContent = sp.text;
              stats.appendChild(span);
            });
            lastAssistant.appendChild(stats);
          }
        }
        // Consolidated metrics (context gauge + session tokens + cost).
        metricsDone(event);
        if (S.sessionId) loadSessions();
        break;

      case 'error':
        if (!sameTurn) break;
        S.pauseQueue?.();
        streamFlush(); endThinking();
        if(S.stopRequested){setIntent('stopping');}else{endStream('interrupted');setIntent('');}
        S.lastFailedPrompt = S.lastPrompt;
        S.onRecoveryNeeded?.('Work failed');
        // The run is unwinding on error — same approval teardown as
        // 'cancelled': a pending card would wait for an ack that never
        // comes and block the queue.
        clearApprovals();
        clearClarify();
        addSystemMessage('⚠ ' + formatErrorMessage(event.message) + ' — Review saved progress before continuing or running again.');
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
        dismissApproval(event.id,event.action);
        break;

      case 'approval_expired':
        // The server killed this approval after its timeout (F-A1). Close
        // the matching card; ids already answered or swept are no-ops, so
        // late or duplicate frames can never resurrect a closed card.
        expireApproval(event.id);
        break;

      case 'clarify_request':
        queueClarify(event);
        notifyUser('question', 'The agent is asking a question');
        break;

      case 'clarify_ack':
        dismissClarify(event.id,event.action);
        break;

      case 'clarify_expired':
        expireClarify(event.id);
        break;

      case 'skill_event':
        S.refreshKnowledge?.();
        break;

      case 'memory_event':
        if(event.event==='episode_pending_review'){S.knowledgeNeedsReview=true;S.refreshSupervision?.();}
        S.refreshKnowledge?.();
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
  S.jobs = jobs.slice(0, 40);S.refreshSupervision?.();
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
  listJobs(sid, getSessionToken(sid) || undefined).then((data) => {
    if (S.sessionId !== sid) return;
    S.jobs = (data && data.jobs) || [];S.refreshSupervision?.();
    paintIntent();
    badgeNow();
  }).catch(() => {});
}

function handleAgentSignal(event) {
  switch (event.event) {
    // Automatic recovery and context maintenance need no principal action.
    case 'tool_recovery': case 'context_trimmed':
      break;
    case 'tool_running':
      setIntent((event.tool ? event.tool + ' · ' : '') + (event.detail || 'running'));
      break;
  }
}
