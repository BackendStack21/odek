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
  'cost-chip': new FakeEl(),
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
const { formatUSD, sessionCostUSD, renderMetrics, metricsDone, resetMetrics } = await import('./metrics.js');

beforeEach(() => {
  S.metrics.pricesConfigured = false;
  S.metrics.inPrice = 0;
  S.metrics.outPrice = 0;
  S.metrics.sessIn = 0;
  S.metrics.sessOut = 0;
  S.metrics.ctxTokens = 0;
  byId['cost-chip'].hidden = true;
  byId['cost-chip'].textContent = '';
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
