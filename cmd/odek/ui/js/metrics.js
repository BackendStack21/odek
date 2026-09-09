// Metrics cluster: the consolidated telemetry surface in the topbar —
// live context-window gauge, session token totals, generation speed,
// and estimated session cost. All state flows through here so the
// numbers can never disagree between the topbar, the health popover,
// and per-message stats.
//
// Data sources:
//   - /api/limits        → per-million prices (flat pair + model_prices)
//   - /api/models        → context-window sizes per listed model
//   - WS usage events    → live context tokens + this-call tok/s per iteration
//   - WS done events     → final session token totals + last-call tok/s
//   - session records    → seeding when a stored session is opened
import { S } from './state.js';
import { getLimits, getModels } from './api.js';
import { formatNum } from './utils.js';

// Metrics state (kept on S so the health popover can read it too).
S.metrics = {
  maxContext: 0,        // tokens; 0 = unknown (gauge shows raw tokens only)
  inPrice: 0,           // USD per 1M input tokens (resolved for current model)
  outPrice: 0,          // USD per 1M output tokens
  flatIn: 0,            // operator's flat pair (limits.limits)
  flatOut: 0,
  pricesConfigured: false,
  modelPrices: {},      // exact model id → {in, out}
  ctxTokens: 0,         // latest reported context size (this run)
  sessIn: 0,            // session cumulative input tokens
  sessOut: 0,           // session cumulative output tokens
  tokPerSec: 0,         // last think-step rate; 0 = unknown / omit
  tokPerSecKind: '',    // 'generation' | 'e2e' | ''
  model: '',
};

// init loads prices and context sizes once, then resolves for the current
// model. Non-fatal on failure — the cluster degrades to token counts.
export async function initMetrics() {
  try {
    const [limits, models] = await Promise.all([
      getLimits(),
      getModels().catch(() => null),
    ]);
    if (limits) {
      const lim = limits.limits || {};
      S.metrics.flatIn = lim.input_cost_per_million_usd || 0;
      S.metrics.flatOut = lim.output_cost_per_million_usd || 0;
      // effective_prices is already resolved for the CONFIGURED model.
      S.metrics.inPrice = (limits.effective_prices && limits.effective_prices.input_cost_per_million_usd) || S.metrics.flatIn;
      S.metrics.outPrice = (limits.effective_prices && limits.effective_prices.output_cost_per_million_usd) || S.metrics.flatOut;
      S.metrics.modelPrices = {};
      for (const [id, p] of Object.entries(lim.model_prices || {})) {
        S.metrics.modelPrices[id] = {
          in: p.input_cost_per_million_usd ?? S.metrics.flatIn,
          out: p.output_cost_per_million_usd ?? S.metrics.flatOut,
        };
      }
      S.metrics.pricesConfigured = S.metrics.flatIn > 0 || S.metrics.flatOut > 0 ||
        Object.keys(S.metrics.modelPrices).length > 0;
    }
    if (models && Array.isArray(models)) {
      const id = S.currentModel || S.metrics.model;
      const cur = models.find(m => m.current || m.id === id);
      if (cur && cur.max_context) S.metrics.maxContext = cur.max_context;
    }
  } catch { /* degraded mode: tokens only */ }
  S.metrics.model = S.currentModel || S.metrics.model;
  resolvePrices();
  renderMetrics();
}

// setModel re-resolves context size + prices after a model switch.
export function setMetricsModel(model) {
  S.metrics.model = model || '';
  if (!model) return;
  const known = (S.availableModels || []).find(m => m.id === model);
  if (known && known.max_context) {
    S.metrics.maxContext = known.max_context;
  }
  resolvePrices();
  renderMetrics();
}

// resolvePrices applies the model_prices override (per field, flat pair
// fallback) — the client-side twin of limits.ResolvePrices.
function resolvePrices() {
  const m = S.metrics.modelPrices[S.metrics.model];
  S.metrics.inPrice = m ? m.in : S.metrics.flatIn;
  S.metrics.outPrice = m ? m.out : S.metrics.flatOut;
}

// ── Event entry points ──

// liveContext: a per-iteration usage event — the freshest parent window
// size plus the server-resolved model limit (when reported).
export function metricsLiveContext(windowTokens, maxContextTokens) {
  if (maxContextTokens > 0) S.metrics.maxContext = maxContextTokens;
  if (!windowTokens || windowTokens <= 0) { renderMetrics(); return; }
  S.metrics.ctxTokens = windowTokens;
  renderMetrics();
}

// pickTokPerSec prefers decode-ish generation rate when the stream measured
// TTFT separately; otherwise end-to-end. 0 / absent means unknown — callers
// must not invent a rate from cumulative outputTokens / wall latency.
export function pickTokPerSec(src) {
  if (!src) return { rate: 0, kind: '' };
  const gen = Number(src.generationTokensPerSecond);
  if (gen > 0) return { rate: gen, kind: 'generation' };
  const e2e = Number(src.tokensPerSecond);
  if (e2e > 0) return { rate: e2e, kind: 'e2e' };
  return { rate: 0, kind: '' };
}

export function formatTokPerSec(rate) {
  if (!(rate > 0)) return '';
  return rate.toFixed(1) + ' tok/s';
}

export function tokPerSecTitle(kind) {
  return kind === 'generation'
    ? 'Generation speed — completion tokens after the first streamed token'
    : 'Think-step throughput — completion tokens over the full call (prefill + wait + decode)';
}

// metricsApplySpeed records a this-call rate from a usage/done frame.
// Missing / zero rates are held, not zeroed, so a silent provider frame
// does not blank the chip mid-run.
export function metricsApplySpeed(src) {
  const picked = pickTokPerSec(src);
  if (picked.rate > 0) {
    S.metrics.tokPerSec = picked.rate;
    S.metrics.tokPerSecKind = picked.kind;
  }
  renderMetrics();
}

// metricsResetSpeed clears the live chip at the start of a new turn so
// the previous turn's rate is not shown as current while the first
// think step is still in flight.
export function metricsResetSpeed() {
  S.metrics.tokPerSec = 0;
  S.metrics.tokPerSecKind = '';
  renderMetrics();
}

// metricsDone: final totals for the turn (cumulative for the session).
export function metricsDone(evt) {
  // Re-resolve prices if the run's model differs from the loaded one (e.g.
  // the server's configured model adopted on the first session event).
  const model = S.currentModel || S.metrics.model;
  if (model && model !== S.metrics.model) setMetricsModel(model);
  // Server-reported model limit wins over the models-table re-resolve
  // above (it reflects runtime overrides like llm.context_window).
  if (evt && evt.maxContextTokens > 0) S.metrics.maxContext = evt.maxContextTokens;
  if (evt && evt.sessionContextTokens != null) S.metrics.sessIn = evt.sessionContextTokens;
  if (evt && evt.sessionOutputTokens != null) S.metrics.sessOut = evt.sessionOutputTokens;
  // windowTokens (parent window) seeds the gauge — NOT inputTokens, which
  // is the run-cumulative billing total incl. sub-agent spend. 0/absent =
  // "not reported": hold the last known value instead of zeroing.
  if (evt && evt.windowTokens > 0) S.metrics.ctxTokens = evt.windowTokens;
  metricsApplySpeed(evt);
}

// metricsFromSession seeds totals when a stored session is opened.
export function metricsFromSession(sess) {
  if (!sess) return resetMetrics();
  S.metrics.sessIn = sess.input_tokens || 0;
  S.metrics.sessOut = sess.output_tokens || 0;
  S.metrics.ctxTokens = 0; // unknown until the next run reports it
  S.metrics.tokPerSec = 0;
  S.metrics.tokPerSecKind = '';
  if (sess.model) setMetricsModel(sess.model);
  renderMetrics();
}

export function resetMetrics() {
  S.metrics.ctxTokens = 0;
  S.metrics.sessIn = 0;
  S.metrics.sessOut = 0;
  S.metrics.tokPerSec = 0;
  S.metrics.tokPerSecKind = '';
  renderMetrics();
}

// turnStatsHTML is the per-message footer on done. Speed uses this-call
// fields only — never cumulative outputTokens / run latency.
export function turnStatsHTML(event) {
  if (!event || event.latency == null) return '';
  const lat = Number(event.latency);
  const latSafe = Number.isFinite(lat) ? lat : 0;
  const spans = [];
  spans.push('<span title="Response time">⚡ ' + (latSafe < 1 ? (latSafe * 1000).toFixed(0) + 'ms' : latSafe.toFixed(1) + 's') + '</span>');
  if (event.inputTokens != null) spans.push('<span title="Input tokens (run total, incl. sub-agents)">⌂ ' + formatNum(event.inputTokens) + '</span>');
  if (event.outputTokens != null) spans.push('<span title="Output tokens (completion)">↳ ' + formatNum(event.outputTokens) + '</span>');
  const cache = (event.cacheReadTokens || 0) + (event.cacheCreationTokens || 0) + (event.cachedTokens || 0);
  if (cache > 0) spans.push('<span title="Cached tokens">⛁ ' + formatNum(cache) + '</span>');
  const picked = pickTokPerSec(event);
  if (picked.rate > 0) {
    spans.push('<span title="' + tokPerSecTitle(picked.kind) + '">↗ ' + formatTokPerSec(picked.rate) + '</span>');
  }
  const turnCost = turnCostUSD(event.inputTokens || 0, event.outputTokens || 0);
  if (turnCost != null && turnCost > 0) {
    spans.push('<span title="Estimated cost of this turn at current prices">$ ' + turnCost.toFixed(4) + '</span>');
  }
  return spans.join('  ·  ');
}

// sessionCostUSD estimates the current session's spend from its totals.
export function sessionCostUSD() {
  if (!S.metrics.pricesConfigured) return null;
  return S.metrics.sessIn / 1e6 * S.metrics.inPrice + S.metrics.sessOut / 1e6 * S.metrics.outPrice;
}

// turnCostUSD prices one turn's usage (for per-message stats).
export function turnCostUSD(inTok, outTok) {
  if (!S.metrics.pricesConfigured) return null;
  return inTok / 1e6 * S.metrics.inPrice + outTok / 1e6 * S.metrics.outPrice;
}

// flashTrim draws attention to the gauge when the engine trims history.
export function flashTrim() {
  const gauge = document.getElementById('ctx-gauge');
  if (!gauge) return;
  gauge.classList.remove('trim');
  void gauge.offsetWidth; // restart the animation
  gauge.classList.add('trim');
  setTimeout(() => gauge.classList.remove('trim'), 1200);
}

// ── Rendering ──

// Bodek formatUSD: cents at $1+, up to four decimals below (trailing
// zeros trimmed, two-decimal floor) so small spends stay visible.
export function formatUSD(usd) {
  if (usd == null) return '';
  if (usd <= 0) return '$0';
  if (usd >= 1) return '$' + usd.toFixed(2);
  let s = usd.toFixed(4).replace(/0+$/, '');
  const dot = s.indexOf('.');
  if (dot >= 0 && s.length - dot < 3) s += '00'.slice(0, 3 - (s.length - dot));
  return '$' + s;
}

export function renderMetrics() {
  const cluster = document.getElementById('metrics');
  if (!cluster) return;
  const m = S.metrics;
  const hasAny = m.ctxTokens > 0 || m.sessIn > 0 || m.sessOut > 0 || m.tokPerSec > 0;
  cluster.classList.toggle('visible', hasAny);

  // Context gauge: percentage against the model's window when known,
  // otherwise just the raw token count.
  const fill = document.getElementById('ctx-fill');
  const pct = document.getElementById('ctx-pct');
  const gauge = document.getElementById('ctx-gauge');
  if (gauge) {
    const showGauge = m.ctxTokens > 0;
    gauge.classList.toggle('on', showGauge);
    if (showGauge) {
      let ratio = 0;
      if (m.maxContext > 0) {
        ratio = Math.min(1, m.ctxTokens / m.maxContext);
        if (pct) pct.textContent = Math.round(ratio * 100) + '%';
      } else if (pct) {
        pct.textContent = formatNum(m.ctxTokens);
      }
      if (fill) fill.style.width = (ratio * 100).toFixed(1) + '%';
      gauge.classList.toggle('warn', m.maxContext > 0 && ratio > 0.6);
      gauge.classList.toggle('hot', m.maxContext > 0 && ratio > 0.85);
      gauge.title = 'Context: ' + formatNum(m.ctxTokens) + ' tokens' +
        (m.maxContext > 0 ? ' of ~' + formatNum(m.maxContext) + ' (' + Math.round(ratio * 100) + '%)' : '') +
        ' — the engine trims history automatically near the limit';
    }
  }

  const tok = document.getElementById('m-tok');
  if (tok) {
    tok.textContent = m.sessIn > 0 || m.sessOut > 0
      ? '⇥ ' + formatNum(m.sessIn) + '  ↦ ' + formatNum(m.sessOut)
      : '';
    tok.title = 'Session totals: ' + formatNum(m.sessIn) + ' in · ' + formatNum(m.sessOut) + ' out';
  }

  const usd = sessionCostUSD();
  const priced = usd != null && (m.inPrice > 0 || m.outPrice > 0);
  const label = priced ? formatUSD(usd) : '';
  const tip = priced
    ? 'Estimated session cost at current prices ($' + m.inPrice + '/M in · $' + m.outPrice + '/M out)'
    : '';

  const cost = document.getElementById('m-cost');
  if (cost) {
    cost.textContent = label || '—';
    cost.title = tip;
    cost.classList.toggle('on', priced);
  }

  const chip = document.getElementById('cost-chip');
  if (chip) {
    chip.hidden = !priced;
    chip.textContent = label;
    chip.title = tip || 'Session cost';
  }

  const speedLabel = formatTokPerSec(m.tokPerSec);
  const speedTip = speedLabel ? tokPerSecTitle(m.tokPerSecKind) : '';
  const speed = document.getElementById('m-speed');
  if (speed) {
    speed.textContent = speedLabel || '—';
    speed.title = speedTip;
    speed.classList.toggle('on', !!speedLabel);
  }
  const speedChip = document.getElementById('speed-chip');
  if (speedChip) {
    speedChip.hidden = !speedLabel;
    speedChip.textContent = speedLabel;
    speedChip.title = speedTip || 'Generation speed';
  }
}
