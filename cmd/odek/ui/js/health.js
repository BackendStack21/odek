// Server health: application-level heartbeat (WS ping/pong with RTT
// measurement), the /api/health REST snapshot, and the status popover.
import { S } from './state.js';
import { getHealth, getUsage } from './api.js';
import { showToast } from './utils.js';

// heartbeatInterval cadence; a missed pong (no reply within 2×) is treated
// as a degraded link — the socket-level reconnect loop stays authoritative.
const HEARTBEAT_MS = 25000;

let heartbeatTimer = null;
let awaitingPong = false;
let lastPingSent = 0;

export function startHeartbeat() {
  stopHeartbeat();
  heartbeatTimer = setInterval(() => {
    sendPing();
  }, HEARTBEAT_MS);
}

export function stopHeartbeat() {
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  heartbeatTimer = null;
  awaitingPong = false;
  setPingLatency(null);
}

function sendPing() {
  if (!S.ws || S.ws.readyState !== WebSocket.OPEN) return;
  if (awaitingPong) return; // previous ping unanswered — let onclose handle it
  awaitingPong = true;
  lastPingSent = performance.now();
  S.ws.send(JSON.stringify({ type: 'ping' }));
  // Safety: clear the awaiting flag if no pong arrives; latency stays stale.
  setTimeout(() => { awaitingPong = false; }, HEARTBEAT_MS);
}

// onPong processes a pong event: records RTT + the server info snapshot.
export function onPong(event) {
  const rtt = lastPingSent ? Math.round(performance.now() - lastPingSent) : null;
  awaitingPong = false;
  S.health = {
    latencyMs: rtt,
    version: event.version,
    model: event.model,
    sandbox: event.sandbox,
    stream: event.stream,
    uptimeSeconds: event.uptime_seconds,
    wsConnections: event.ws_connections,
    source: 'pong',
  };
  setPingLatency(rtt);
  renderPopover();
}

// onServerInfo processes the server_info event pushed on connect.
export function onServerInfo(event) {
  S.health = {
    ...(S.health || {}),
    version: event.version,
    model: event.model,
    sandbox: event.sandbox,
    stream: event.stream,
    uptimeSeconds: event.uptime_seconds,
    wsConnections: event.ws_connections,
    source: 'server_info',
  };
  const streamBadge = document.getElementById('stream-badge');
  if (streamBadge) streamBadge.style.display = event.stream ? 'inline-flex' : 'none';
  renderPopover();
}

function setPingLatency(ms) {
  const el = document.getElementById('ping-latency');
  if (!el) return;
  if (ms == null) {
    el.textContent = '';
    el.classList.remove('warn');
    return;
  }
  el.textContent = ms + 'ms';
  el.classList.toggle('warn', ms > 1000);
  el.title = 'WebSocket round-trip time';
}

function fmtTokens(n) {
  if (n == null) return '—';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
  return String(n);
}

function fmtUptime(secs) {
  if (secs == null) return '—';
  if (secs < 60) return secs + 's';
  if (secs < 3600) return Math.floor(secs / 60) + 'm ' + (secs % 60) + 's';
  if (secs < 86400) return Math.floor(secs / 3600) + 'h ' + Math.floor((secs % 3600) / 60) + 'm';
  return Math.floor(secs / 86400) + 'd ' + Math.floor((secs % 86400) / 3600) + 'h';
}

export function renderPopover() {
  const h = S.health;
  if (!h) return;
  const set = (id, v) => {
    const el = document.getElementById(id);
    if (el) el.textContent = v;
  };
  set('hp-version', h.version || 'dev');
  set('hp-uptime', fmtUptime(h.uptimeSeconds));
  set('hp-model', h.model || '—');
  set('hp-sandbox', h.sandbox ? '🔒 enabled' : 'off');
  set('hp-stream', h.stream ? '⚡ live' : 'buffered');
  set('hp-conns', h.wsConnections != null ? String(h.wsConnections) : '—');
  set('hp-latency', h.latencyMs != null ? h.latencyMs + 'ms' : '—');
  const u = h.usage;
  set('hp-usage', u
    ? u.promptsCompleted + '/' + u.promptsStarted + ' prompts · ' + fmtTokens(u.tokensIn) + ' in · ' + fmtTokens(u.tokensOut) + ' out' +
      (u.pricesConfigured ? ' · ≈$' + u.costUsd.toFixed(4) : '')
    : '—');
}

async function refreshFromRest() {
  try {
    const [h, usage] = await Promise.all([getHealth(), getUsage().catch(() => null)]);
    S.health = {
      ...(S.health || {}),
      version: h.version,
      model: h.model,
      sandbox: h.sandbox,
      stream: h.stream,
      uptimeSeconds: h.uptime_seconds,
      wsConnections: h.ws_connections,
      source: 'rest',
    };
    if (usage) {
      S.health.usage = {
        promptsStarted: usage.prompts_started,
        promptsCompleted: usage.prompts_completed,
        tokensIn: usage.tokens_in,
        tokensOut: usage.tokens_out,
        costUsd: usage.estimated_cost_usd,
        pricesConfigured: usage.prices_configured,
      };
    }
    renderPopover();
  } catch {
    showToast('health unavailable');
  }
}

// Popover toggle: click (or Enter/Space) on the status group.
const popover = () => document.getElementById('health-popover');

export function togglePopover() {
  const p = popover();
  if (!p) return;
  if (p.hidden) {
    p.hidden = false;
    refreshFromRest();
    // Auto-dismiss on outside click / Escape.
    setTimeout(() => {
      document.addEventListener('click', outsideDismiss, { once: true });
      document.addEventListener('keydown', escDismiss, { once: true });
    }, 0);
  } else {
    p.hidden = true;
  }
}

function outsideDismiss(e) {
  const p = popover();
  if (p && !p.hidden && !p.contains(e.target) && !document.getElementById('status-group').contains(e.target)) {
    p.hidden = true;
  } else if (p && !p.hidden) {
    // still open — re-arm for the next click
    document.addEventListener('click', outsideDismiss, { once: true });
  }
}

function escDismiss(e) {
  const p = popover();
  if (e.key !== 'Escape' || !p || p.hidden) return;
  p.hidden = true;
}

const statusGroup = document.getElementById('status-group');
if (statusGroup) {
  statusGroup.addEventListener('click', (e) => {
    e.stopPropagation();
    togglePopover();
  });
  statusGroup.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      togglePopover();
    }
  });
}
