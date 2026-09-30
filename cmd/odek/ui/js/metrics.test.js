// Metrics cluster + Bodek session-cost chip.
//   node --test cmd/odek/ui/js/metrics.test.js
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

class ClassList {
  constructor(el) { this.el = el; }
  get set() { return new Set((this.el.className || '').split(/\s+/).filter(Boolean)); }
  add(...cs) { const s = this.set; cs.forEach(c => s.add(c)); this.el.className = [...s].join(' '); }
  remove(...cs) { const s = this.set; cs.forEach(c => s.delete(c)); this.el.className = [...s].join(' '); }
  contains(c) { return this.set.has(c); }
  toggle(c, force) {
    const want = force === undefined ? !this.contains(c) : !!force;
    if (want) this.add(c); else this.remove(c);
    return want;
  }
}

class FakeEl {
  constructor() {
    this.className = '';
    this.classList = new ClassList(this);
    this.textContent = '';
    this.title = '';
    this.hidden = false;
    this.style = {};
  }
}

const byId = {
  metrics: new FakeEl(),
  'ctx-gauge': new FakeEl(),
  'ctx-fill': new FakeEl(),
  'ctx-pct': new FakeEl(),
  'm-tok': new FakeEl(),
  'm-cost': new FakeEl(),
  'm-speed': new FakeEl(),
  'cost-chip': new FakeEl(),
  'speed-chip': new FakeEl(),
};
globalThis.document = {
  getElementById: (id) => byId[id] || null,
  addEventListener: () => {},
};
globalThis.window = globalThis;
globalThis.localStorage = (() => {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
})();

const { S } = await import('./state.js');
const { formatUSD, sessionCostUSD, renderMetrics, metricsDone, resetMetrics, pickTokPerSec, formatTokPerSec, metricsApplySpeed, metricsResetSpeed, turnStatsHTML, turnStatsSpans, metricsBeginTurn, metricsLiveUsage, metricsLiveContext, metricsNoteOutput } = await import('./metrics.js');

beforeEach(() => {
  S.metrics.pricesConfigured = false;
  S.metrics.inPrice = 0;
  S.metrics.outPrice = 0;
  S.metrics.sessIn = 0;
  S.metrics.sessOut = 0;
  S.metrics.ctxTokens = 0;
  S.metrics.tokPerSec = 0;
  S.metrics.tokPerSecKind = '';
  S.metrics.turnBaseIn = 0;
  S.metrics.turnBaseOut = 0;
  S.metrics.streamedOutChars = 0;
  byId['cost-chip'].hidden = true;
  byId['cost-chip'].textContent = '';
  byId['speed-chip'].hidden = true;
  byId['speed-chip'].textContent = '';
  byId['m-speed'].textContent = '—';
});

test('formatUSD matches Bodek compact dollars', () => {
  assert.equal(formatUSD(0), '$0');
  assert.equal(formatUSD(0.016), '$0.016');
  assert.equal(formatUSD(0.201), '$0.201');
  assert.equal(formatUSD(1.2), '$1.20');
  assert.equal(formatUSD(null), '');
});

test('cost chip stays hidden without prices', () => {
  S.metrics.sessIn = 10000;
  S.metrics.sessOut = 2000;
  renderMetrics();
  assert.equal(byId['cost-chip'].hidden, true);
  assert.equal(sessionCostUSD(), null);
});

test('cost chip shows Bodek header spend when prices are set', () => {
  S.metrics.pricesConfigured = true;
  S.metrics.inPrice = 1;
  S.metrics.outPrice = 3;
  metricsDone({ sessionContextTokens: 10000, sessionOutputTokens: 2000, windowTokens: 1000 });
  assert.equal(sessionCostUSD(), 0.016);
  assert.equal(byId['cost-chip'].hidden, false);
  assert.equal(byId['cost-chip'].textContent, '$0.016');
  assert.equal(byId['m-cost'].textContent, '$0.016');
});

test('reset keeps the chip when prices exist ($0, not a guessed hide)', () => {
  S.metrics.pricesConfigured = true;
  S.metrics.inPrice = 1;
  S.metrics.outPrice = 3;
  S.metrics.sessIn = 10000;
  renderMetrics();
  resetMetrics();
  assert.equal(byId['cost-chip'].hidden, false);
  assert.equal(byId['cost-chip'].textContent, '$0');
});

test('pickTokPerSec prefers generation rate and omits zeros', () => {
  assert.deepEqual(pickTokPerSec(null), { rate: 0, kind: '' });
  assert.deepEqual(pickTokPerSec({}), { rate: 0, kind: '' });
  assert.deepEqual(pickTokPerSec({ tokensPerSecond: 9.6 }), { rate: 9.6, kind: 'e2e' });
  assert.deepEqual(
    pickTokPerSec({ tokensPerSecond: 9.6, generationTokensPerSecond: 25.2 }),
    { rate: 25.2, kind: 'generation' },
  );
  assert.deepEqual(pickTokPerSec({ outputTokens: 800, latency: 0.5 }), { rate: 0, kind: '' });
});

test('formatTokPerSec matches the CLI one-decimal suffix', () => {
  assert.equal(formatTokPerSec(0), '');
  assert.equal(formatTokPerSec(9.6), '9.6 tok/s');
  assert.equal(formatTokPerSec(100), '100.0 tok/s');
});

test('speed chip stays hidden until a think-step rate arrives', () => {
  renderMetrics();
  assert.equal(byId['speed-chip'].hidden, true);
  assert.equal(byId['m-speed'].textContent, '—');
});

test('usage-style applySpeed shows generation rate on the chip and popover', () => {
  metricsApplySpeed({ generationTokensPerSecond: 25.2, tokensPerSecond: 9.6 });
  assert.equal(byId['speed-chip'].hidden, false);
  assert.equal(byId['speed-chip'].textContent, '25.2 tok/s');
  assert.equal(byId['m-speed'].textContent, '25.2 tok/s');
  assert.match(byId['speed-chip'].title, /first streamed token/);
});

test('a usage frame without a rate holds the last chip', () => {
  metricsApplySpeed({ tokensPerSecond: 9.6 });
  metricsApplySpeed({ outputTokens: 10 });
  assert.equal(S.metrics.tokPerSec, 9.6);
  assert.equal(byId['speed-chip'].textContent, '9.6 tok/s');
});

test('resetSpeed and resetMetrics hide the chip', () => {
  metricsApplySpeed({ tokensPerSecond: 9.6 });
  metricsResetSpeed();
  assert.equal(byId['speed-chip'].hidden, true);
  metricsApplySpeed({ tokensPerSecond: 9.6 });
  resetMetrics();
  assert.equal(S.metrics.tokPerSec, 0);
  assert.equal(byId['speed-chip'].hidden, true);
});

test('done applies last-call speed without using cumulative outputTokens', () => {
  metricsDone({
    sessionContextTokens: 10000, sessionOutputTokens: 2000, windowTokens: 1000,
    tokensPerSecond: 9.6, outputTokens: 800, latency: 0.05,
  });
  assert.equal(S.metrics.tokPerSec, 9.6);
  assert.equal(byId['speed-chip'].textContent, '9.6 tok/s');
});

test('turnStatsHTML includes tok/s from this-call fields, never invented from totals', () => {
  const html = turnStatsHTML({
    latency: 8.1, inputTokens: 18432, outputTokens: 78,
    generationTokensPerSecond: 25.2, tokensPerSecond: 9.6,
  });
  assert.match(html, /8\.1s/);
  assert.match(html, /↗ 25\.2 tok\/s/);
  assert.equal(html.includes('9.6 tok/s'), false, 'generation rate wins over end-to-end');
  const none = turnStatsHTML({ latency: 0.5, inputTokens: 10, outputTokens: 800 });
  assert.equal(none.includes('tok/s'), false);
});

// Pins the injection resistance of the done-frame stats footer: every span
// value must be numeric-coerced (turnStatsSpans) or escaped (turnStatsHTML)
// so a hostile done frame can never smuggle markup into the transcript.
const HOSTILE = '<img src=x onerror="alert(1)">';

test('turnStatsSpans coerces hostile done-frame values to inert numbers', () => {
  const spans = turnStatsSpans({
    latency: HOSTILE, inputTokens: HOSTILE, outputTokens: HOSTILE,
    cacheReadTokens: HOSTILE, generationTokensPerSecond: HOSTILE,
  });
  assert.ok(spans.length > 0);
  for (const sp of spans) {
    assert.equal(sp.text.includes('<'), false, `markup leaked in text: ${sp.text}`);
    assert.equal(sp.title.includes('<'), false, `markup leaked in title: ${sp.title}`);
  }
  // Coerced latency is finite → shows as 0ms, never the raw string.
  assert.match(spans[0].text, /^⚡ 0ms$/);
});

test('turnStatsHTML escapes span titles and text against attribute/body injection', () => {
  const html = turnStatsHTML({ latency: 1.2, inputTokens: 5 });
  assert.doesNotMatch(html, /onerror|<img/i);
  // No raw quote ever reaches the title attribute position.
  const hostile = turnStatsSpans({ latency: HOSTILE });
  const htmlHostile = turnStatsHTML({ latency: HOSTILE });
  assert.equal(htmlHostile.includes(HOSTILE), false);
  assert.equal(hostile.length > 0, true);
});

test('turnStatsHTML renders identical content to turnStatsSpans for normal events', () => {
  const ev = { latency: 2.4, inputTokens: 1200, outputTokens: 300, generationTokensPerSecond: 30 };
  const spans = turnStatsSpans(ev);
  const html = turnStatsHTML(ev);
  for (const sp of spans) {
    assert.ok(html.includes(sp.text), `missing span text: ${sp.text}`);
  }
  assert.equal(html.split('<span').length - 1, spans.length);
});

test('usage overlays run-cumulative tokens onto session totals mid-turn', () => {
  S.metrics.pricesConfigured = true;
  S.metrics.inPrice = 1;
  S.metrics.outPrice = 3;
  S.metrics.sessIn = 10000;
  S.metrics.sessOut = 2000;
  metricsBeginTurn();
  metricsLiveContext(5000, 128000);
  metricsLiveUsage({ inputTokens: 400, outputTokens: 50, windowTokens: 5000 });
  assert.equal(S.metrics.ctxTokens, 5000);
  assert.equal(S.metrics.sessIn, 10400);
  assert.equal(S.metrics.sessOut, 2050);
  assert.equal(sessionCostUSD().toFixed(5), '0.01655');
  assert.equal(byId['cost-chip'].hidden, false);
  assert.equal(byId['ctx-gauge'].classList.contains('on'), true);
  assert.equal(byId['ctx-fill'].style.width, '3.9%');
});

test('streamed output chars bump the live cost until the next usage frame', () => {
  S.metrics.pricesConfigured = true;
  S.metrics.inPrice = 0;
  S.metrics.outPrice = 4;
  S.metrics.sessIn = 0;
  S.metrics.sessOut = 0;
  metricsBeginTurn();
  metricsNoteOutput(400); // ~100 tokens
  assert.ok(sessionCostUSD() > 0);
  metricsLiveUsage({ outputTokens: 20 });
  assert.equal(S.metrics.streamedOutChars, 0);
  assert.equal(S.metrics.sessOut, 20);
});

