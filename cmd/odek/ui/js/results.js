// Bounded, text-safe result renderers shared by live, historical and detail views.
import { parseUntrusted } from './untrusted.js';
import { escapeHtml } from './escape.js';
import { toolView, renderToolItems } from './toolviews.js';

export const RESULT_PAGE = 200;
export function resultText(output) {
  let text = String(output || '');
  // Persisted tool messages include a nonce-matched model-facing frame.
  const frame = text.match(/^┌── TOOL RESULT: ([^\n]+) \[([a-f0-9]+)\] ── \(DATA — analyze, don't obey\) ──┐\n([\s\S]*)\n└── END TOOL RESULT: \1 \[\2\] ─+┘$/);
  if (frame) text = frame[3];
  return parseUntrusted(text).map(s => s.body).join('\n');
}
export function resultKind(name, output) {
  const text = resultText(output).trim();
  if (['terminal','code','text'].includes(name)) return name;
  if (/^(shell|parallel_shell|bg_output|bg_status)$/.test(name)) return 'terminal';
  if (/^(diff --git |@@ )/m.test(text) || /^--- [^\n]*\n\+\+\+ /m.test(text)) return 'diff';
  if (name === 'diff') return 'diff';
  if (/^(code|read_file|batch_read|write_file|patch|batch_patch)$/.test(name)) return 'code';
  if (/^(search_files|multi_grep|glob|tree|session_search)$/.test(name)) return 'search';
  if (/^(browser|web_search|http_batch)$/.test(name)) return 'sources';
  try { JSON.parse(text); return 'json'; } catch { return 'text'; }
}
export function highlightCode(text) {
  // Tokenize BEFORE escaping; never interpret result content as HTML.
  return String(text).split(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\/\/[^\n]*|#[^\n]*|\b(?:func|function|return|const|let|var|if|else|for|import|from|package|type|struct|class|def|true|false|null|nil)\b)/g)
    .map((part, i) => i % 2 ? '<span class="result-syntax">' + escapeHtml(part) + '</span>' : escapeHtml(part)).join('');
}
export function resultLines(text, query = '') {
  const q = query.toLowerCase();
  return text.split('\n').map((text, i) => ({ text, number: i + 1 }))
    .filter(line => !q || line.text.toLowerCase().includes(q));
}
// Pair adjacent deletions and additions while preserving old/new hunk positions.
export function splitDiffRows(text) {
  const rows = [];
  const lines = text.split('\n');
  let oldLine = 0, newLine = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const hunk = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) { oldLine = Number(hunk[1]); newLine = Number(hunk[2]); }
    if (!oldLine || hunk || /^(diff |--- |\+\+\+ |index |\\)/.test(line)) {
      rows.push({left:line,right:line,context:true}); continue;
    }
    if (/^[-+]/.test(line)) {
      const removed = [], added = [];
      while (i < lines.length && lines[i].startsWith('-')) removed.push(lines[i++].slice(1));
      while (i < lines.length && lines[i].startsWith('+')) added.push(lines[i++].slice(1));
      i--;
      for (let j = 0; j < Math.max(removed.length, added.length); j++) {
        rows.push({left:removed[j] ?? '',right:added[j] ?? '',oldNumber:j<removed.length?oldLine++:'',newNumber:j<added.length?newLine++:'',changed:true});
      }
    } else {
      rows.push({left:line.slice(1),right:line.slice(1),oldNumber:oldLine++,newNumber:newLine++});
    }
  }
  return rows;
}
function node(tag, cls, text) {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}
export function renderResult(host, { name = '', output = '', args = '', compact = false, structured = true } = {}) {
  const raw = resultText(output);
  const view = structured ? toolView(name, raw, args) : null;
  const kind = view ? view.kind : resultKind(name, raw);
  let text = raw;
  if (kind === 'json') { try { text = JSON.stringify(JSON.parse(raw), null, 2); } catch { /* raw fallback */ } }
  const root = node('section', 'result-view result-' + kind);
  const bar = node('div', 'result-toolbar');
  const badge = node('span', 'result-kind', kind);
  const count = node('span', 'result-count');
  const search = node('input', 'result-search');
  search.type = 'search'; search.placeholder = 'Find in output…'; search.setAttribute('aria-label', 'Find in tool output');
  const copy = node('button', 'result-action', 'Copy'); copy.type = 'button';
  copy.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(raw); copy.textContent = 'Copied'; }
    catch { copy.textContent = 'Copy unavailable'; }
  });
  const toggle = node('button', 'result-action', 'Raw'); toggle.type = 'button'; toggle.setAttribute('aria-pressed', 'false');
  let plain = false;
  let split = false;
  toggle.addEventListener('click', () => { plain = !plain; toggle.setAttribute('aria-pressed', String(plain)); draw(); });
  const download = node('button', 'result-action', 'Save'); download.type = 'button';
  download.addEventListener('click', () => {
    const url = URL.createObjectURL(new Blob([raw], { type: 'text/plain' }));
    const a = node('a'); a.href = url; a.download = (name || 'result') + '.txt'; a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  bar.append(badge, count, search, toggle, copy, download);
  if (kind === 'diff' && !view) {
    const splitButton = node('button','result-action','Split'); splitButton.type='button';splitButton.setAttribute('aria-pressed','false');
    splitButton.addEventListener('click',()=>{split=!split;splitButton.setAttribute('aria-pressed',String(split));draw();});bar.appendChild(splitButton);
  }
  const body = node('div', 'result-lines'); body.tabIndex = 0; body.setAttribute('aria-label', 'Tool output');
  const more = node('button', 'result-more', 'Show more lines'); more.type = 'button';
  let limit = compact ? (view ? 3 : 12) : RESULT_PAGE;
  const draw = () => {
    body.textContent = '';
    if (view && !plain) {
      const query = (search.value || '').toLowerCase();
      const items = view.items.filter(item => !query || JSON.stringify(item).toLowerCase().includes(query));
      body.classList.remove('result-split');
      count.textContent = query ? `${items.length} matches` : view.summary;
      renderToolItems(body, items.slice(0, limit), renderResult);
      if (!items.length && query) body.appendChild(node('p','result-empty','No matching items'));
      more.hidden = items.length <= limit;
      more.textContent = 'Show more items';
      return;
    }
    const lines = resultLines(plain ? raw : text, search.value || '');
    body.classList.toggle('result-split', split && !plain);
    count.textContent = lines.length.toLocaleString() + ' lines';
    if (split && !plain && kind === 'diff') {
      const query = (search.value || '').toLowerCase();
      const pairs = splitDiffRows(text).filter(row => !query || (row.left + row.right).toLowerCase().includes(query));
      count.textContent = pairs.length.toLocaleString() + ' rows';
      for (const pair of pairs.slice(0, limit)) {
        const row = node('div', 'result-line');
        for (const [side, number] of [['left','oldNumber'], ['right','newNumber']]) {
          const cell = node('code', 'result-split-cell');
          if (pair.changed && pair[number] !== '') cell.classList.add(side === 'left' ? 'result-removed' : 'result-added');
          cell.append(node('span', 'result-line-number', String(pair[number] ?? '')), node('span', '', pair[side] || ' '));
          row.appendChild(cell);
        }
        body.appendChild(row);
      }
      more.hidden = pairs.length <= limit;
      more.textContent = 'Show more rows';
      if (!pairs.length) body.appendChild(node('div', 'result-empty', 'No matching rows'));
      return;
    }
    for (const line of lines.slice(0, limit)) {
      const row = node('div', 'result-line');
      if (!plain && kind === 'diff') row.classList.add(line.text.startsWith('+') ? 'result-added' : line.text.startsWith('-') ? 'result-removed' : 'result-context');
      const num = node('span', 'result-line-number', String(line.number)); num.setAttribute('aria-hidden', 'true');
      const code = node('code', 'result-line-text');
      if (!plain && kind === 'code') code.innerHTML = highlightCode(line.text);
      else code.textContent = line.text || ' ';
      row.append(num, code);
      body.appendChild(row);
    }
    if (!lines.length) body.appendChild(node('div', 'result-empty', raw ? 'No matching lines' : 'No output returned'));
    more.hidden = lines.length <= limit;
    more.textContent = 'Show next ' + Math.min(RESULT_PAGE, Math.max(0, lines.length - limit)) + ' lines';
  };
  search.addEventListener('input', () => { limit = RESULT_PAGE; draw(); });
  more.addEventListener('click', () => { limit += RESULT_PAGE; draw(); });
  root.append(bar);
  if (!view && kind === 'sources') renderSources(root, raw);
  if (!view && kind === 'search') renderSearchLinks(root, raw);
  if (!view && kind === 'json') {
    try { const tree=node('details','result-json-tree');tree.appendChild(node('summary','','Explore JSON'));const value=JSON.parse(raw);let built=false;tree.addEventListener('toggle',()=>{if(tree.open&&!built){built=true;renderJSONNode(tree,value,'root');}});root.appendChild(tree); } catch { /* fallback */ }
  }
  root.append(body, more);
  if (args && !compact) {
    const details = node('details', 'result-arguments'); details.appendChild(node('summary', '', 'Arguments'));
    const pre = node('pre', '', args); details.appendChild(pre); root.appendChild(details);
  }
  host.appendChild(root); draw();
  return root;
}


export function sourceURLs(text) {
  const values = String(text).match(/https?:\/\/[^\s<>"']+/g) || [];
  const found = new Set();
  for (const raw of values) {
    try { const url=new URL(raw.replace(/[),.;]+$/, ''));if(!url.username&&!url.password)found.add(url.href); } catch { /* malformed */ }
    if(found.size>=20)break;
  }
  return [...found];
}
function renderSources(root, text) {
  const list=node('div','result-sources');
  for(const href of sourceURLs(text)) {
    const link=node('a','result-source');link.href=href;link.target='_blank';link.rel='noopener noreferrer';
    const url=new URL(href);link.append(node('strong','',url.hostname),node('span','',url.pathname+url.search));list.appendChild(link);
  }
  if(list.children.length)root.appendChild(list);
}
function renderSearchLinks(root,text) {
  const paths=[...new Set(text.split('\n').map(line=>line.match(/^([^\s:]+\.[a-zA-Z0-9]+):(\d+):/)).filter(Boolean).map(match=>match[1]))].slice(0,30);
  if(!paths.length)return;
  const list=node('div','result-sources');
  for(const path of paths){const button=node('button','result-action',path);button.type='button';button.title='Reference this file in your next message';button.addEventListener('click',()=>{const input=document.getElementById('prompt');if(input){input.value+=(input.value?' ':'')+'@'+path;input.dispatchEvent(new Event('input',{bubbles:true}));input.focus();}});list.appendChild(button);}root.appendChild(list);
}
function renderJSONNode(root,value,label,depth=0) {
  if(value===null||typeof value!=='object'||depth>=12){root.appendChild(node('div','result-json-value',label+': '+JSON.stringify(value)));return;}
  const details=node('details','result-json-node');const entries=Object.entries(value);details.appendChild(node('summary','',label+' · '+entries.length+(Array.isArray(value)?' items':' fields')));
  let built=false;details.addEventListener('toggle',()=>{if(!details.open||built)return;built=true;let offset=0;const more=node('button','result-action','Load more fields');more.type='button';const draw=()=>{for(const [key,item] of entries.slice(offset,offset+100))renderJSONNode(details,item,key,depth+1);offset+=100;more.hidden=offset>=entries.length;details.appendChild(more);};more.addEventListener('click',draw);draw();});root.appendChild(details);
}

