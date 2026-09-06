// Typed tool-result chips. Every renderer treats output as PLAIN TEXT —
// never HTML. Structured chips fire only on machine-shaped output
// (go test, git, HTTP status lines). Prose like "Build passed" stays quiet.
import { escapeHtml } from './escape.js';

const DIFF_LINE = /^(?:diff --git |@@ |[+-](?![+-]{2}))/m;
const HTTP_STATUS = /\bHTTP\/\d(?:\.\d)?\s+(\d{3})\b/;
const GIT_COMMIT = /(?:^|\s)([0-9a-f]{7,40})\s+(?:commit|ok)|\[(?:main|master|[a-z0-9._/-]+)(?:\s+[0-9a-f]{7})?\]/i;
const GIT_PUSH = /^\s*To\s+\S+|^\s*\d+\s+\w+\s+->\s+\w+/m;
const GO_TEST = /(\d+)\s+passed(?:.*?(\d+)\s+skipped)?(?:.*?(\d+)\s+failed)?/i;
const SEARCH_HITS = /(\d+)\s+(?:matches?|hits?|results?)\b/i;
const LINT_WARN = /(\d+)\s+warnings?(?:\s*,\s*(\d+)\s+errors?)?/i;

export function classifyToolResult(name, output) {
  const text = String(output || '');
  const chips = [];

  if (DIFF_LINE.test(text)) {
    let plus = 0, minus = 0;
    for (const line of text.split('\n')) {
      if (line.startsWith('+') && !line.startsWith('+++')) plus++;
      else if (line.startsWith('-') && !line.startsWith('---')) minus++;
    }
    chips.push({ kind: 'diff', label: '+' + plus + ' −' + minus, tone: minus > plus * 2 ? 'warn' : 'ok' });
  }

  const http = text.match(HTTP_STATUS);
  if (http) {
    const code = Number(http[1]);
    chips.push({
      kind: 'http',
      label: String(code),
      tone: code >= 500 ? 'danger' : code >= 400 ? 'warn' : 'ok',
    });
  }

  const tests = text.match(GO_TEST);
  if (tests && (name === 'shell' || name === 'parallel_shell')) {
    const passed = tests[1] || '0';
    const skipped = tests[2];
    const failed = tests[3];
    let label = '✓ ' + passed + ' passed';
    if (skipped) label += ' · ' + skipped + ' skipped';
    if (failed && Number(failed) > 0) label += ' · ' + failed + ' failed';
    chips.push({ kind: 'test', label, tone: failed && Number(failed) > 0 ? 'danger' : 'ok' });
  }

  if (GIT_PUSH.test(text) || /\bpushed\b/i.test(text) && /origin|remote/i.test(text)) {
    chips.push({ kind: 'git', label: '↑ push', tone: 'ok' });
  } else if (GIT_COMMIT.test(text) && /\bcommit\b/i.test(text)) {
    const sha = (text.match(/\b([0-9a-f]{7,40})\b/) || [])[1];
    chips.push({ kind: 'git', label: sha ? '⎇ ' + sha.slice(0, 7) : '⎇ commit', tone: 'ok' });
  }

  const lint = text.match(LINT_WARN);
  if (lint && /lint|eslint|staticcheck|govet/i.test(name + text.slice(0, 200))) {
    chips.push({ kind: 'lint', label: lint[0], tone: lint[2] && Number(lint[2]) > 0 ? 'danger' : 'warn' });
  }

  const hits = text.match(SEARCH_HITS);
  if (hits && (name === 'search_files' || name === 'multi_grep' || name === 'session_search')) {
    chips.push({ kind: 'search', label: hits[1] + ' hits', tone: 'ok' });
  }

  if (looksLikeJSON(text)) chips.push({ kind: 'json', label: 'json', tone: 'ok' });

  return chips;
}

function looksLikeJSON(text) {
  const t = text.trim();
  return (t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'));
}

export function prettyToolBody(output) {
  const t = String(output || '');
  if (looksLikeJSON(t)) {
    try { return JSON.stringify(JSON.parse(t), null, 2); } catch { /* keep raw */ }
  }
  return t;
}

export function chipsHtml(chips) {
  if (!chips || !chips.length) return '';
  return chips.map((c) =>
    '<span class="tb-chip tb-chip-' + escapeHtml(c.tone || 'ok') + '">' + escapeHtml(c.label) + '</span>'
  ).join('');
}

export function collectReceipt(name, argsJSON, output) {
  const receipt = { files: [], plus: 0, minus: 0, tests: '', tools: 1 };
  try {
    const obj = JSON.parse(argsJSON || '{}');
    const path = obj.path || obj.file || '';
    if (path) receipt.files.push(String(path));
    if (Array.isArray(obj.paths)) receipt.files.push(...obj.paths.map(String));
  } catch { /* ignore */ }
  const chips = classifyToolResult(name, output);
  for (const c of chips) {
    if (c.kind === 'diff') {
      const m = c.label.match(/\+(\d+)\s+−(\d+)/);
      if (m) { receipt.plus += Number(m[1]); receipt.minus += Number(m[2]); }
    }
    if (c.kind === 'test') receipt.tests = c.label;
  }
  return receipt;
}

export function formatReceipt(r) {
  if (!r) return '';
  const bits = [];
  if (r.files && r.files.length) bits.push('touched ' + r.files.length);
  if (r.plus || r.minus) bits.push('+' + r.plus + ' −' + r.minus);
  if (r.tests) bits.push(r.tests);
  return bits.join(' · ');
}
