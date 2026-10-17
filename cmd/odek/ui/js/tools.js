// Typed tool-result chips. Every renderer treats output as PLAIN TEXT —
// never HTML. Structured chips fire only on machine-shaped output
// (go test, git, HTTP status lines). Prose like "Build passed" stays quiet.
import { escapeHtml, escapeAttr } from './escape.js';

// Unified-diff evidence only: a git header, a hunk header, or a file header
// pair. A bare leading '-' is not enough — `ls -l` permission columns
// ("-rw-r--r--") and Markdown bullets start with one too.
const DIFF_LINE = /^(?:diff --git |@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@|--- \S.*\n\+\+\+ \S)/m;
const HTTP_STATUS = /\bHTTP\/\d(?:\.\d)?\s+(\d{3})\b/;
const GIT_COMMIT = /(?:^|\s)([0-9a-f]{7,40})\s+(?:commit|ok)|\[(?:main|master|[a-z0-9._/-]+)(?:\s+[0-9a-f]{7})?\]/i;
const GIT_PUSH = /^\s*To\s+\S+|^\s*\d+\s+\w+\s+->\s+\w+/m;
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

  // Go package lines carry a duration or "(cached)"; bare "ok N - name" lines
  // are TAP test points, not packages.
  const goPassed = (text.match(/^ok\s+\S+\s+(?:[\d.]+s\b|\(cached\))/gm) || []).length;
  const goFailed = (text.match(/^FAIL\s+\S+/gm) || []).length;
  const tapFailed = Math.max(
    (text.match(/^not ok\b/gm) || []).length,
    Number((text.match(/^# fail (\d+)/m) || [])[1] || 0),
  );
  if ((goPassed || goFailed || tapFailed) && /^(shell)$/.test(name)) {
    const failed = goFailed + tapFailed;
    chips.push({
      kind: 'test',
      label: (goPassed ? goPassed + ' packages passed' : '') + (goPassed && failed ? ' · ' : '') + (failed ? failed + ' failed' : ''),
      tone: failed ? 'danger' : 'ok',
    });
  }
  // The summary line lists passed/failed/skipped in any order (pytest and jest
  // print failures first), so each count is read independently.
  const summary = name === 'shell' ? text.split('\n').find((line) => /\d+\s+(?:passed|failed)\b/i.test(line)) : null;
  if (summary) {
    const count = (re) => Number((summary.match(re) || [])[1] || 0);
    const passed = count(/(\d+)\s+passed/i);
    const skipped = count(/(\d+)\s+skipped/i);
    const failed = count(/(\d+)\s+failed/i);
    if (passed || failed) {
      let label = failed ? '✗ ' : '✓ ';
      label += passed ? passed + ' passed' : '';
      if (skipped) label += (passed ? ' · ' : '') + skipped + ' skipped';
      if (failed) label += (passed || skipped ? ' · ' : '') + failed + ' failed';
      chips.push({ kind: 'test', label, tone: failed ? 'danger' : 'ok' });
    }
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
  if (hits && (name === 'search_files' || name === 'session_search')) {
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
    '<span class="tb-chip tb-chip-' + escapeAttr(c.tone || 'ok') + '">' + escapeHtml(c.label) + '</span>'
  ).join('');
}

export function collectReceipt(name, argsJSON, output) {
  const receipt = { files: [], plus: 0, minus: 0, tests: '', tools: 1 };
  try {
    const obj = JSON.parse(argsJSON || '{}');
    const writes = /^(write_file|patch)$/.test(name);
    const path = writes ? obj.path || obj.file || '' : '';
    if (path) receipt.files.push(String(path));
    if (writes && Array.isArray(obj.paths)) receipt.files.push(...obj.paths.map(String));
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
  if (r.files && r.files.length) bits.push('modified ' + new Set(r.files).size);
  if (r.plus || r.minus) bits.push('+' + r.plus + ' −' + r.minus);
  if (r.tests) bits.push(r.tests);
  return bits.join(' · ');
}
