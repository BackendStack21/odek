// Tool-specific presentation models. Never infer success from optimistic prose.
import { unwrapForDisplay } from './untrusted.js';

const object = value => value && typeof value === 'object' && !Array.isArray(value);
const list = value => Array.isArray(value) ? value : [];
export function displayValue(value) {
  if (value == null) return '';
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  return unwrapForDisplay(text);
}
function parse(value) {
  if (object(value)) return value;
  try { const data = JSON.parse(value); return object(data) ? data : {}; } catch { return {}; }
}
function byteSize(n) {
  const v = Number(n);
  if (!Number.isFinite(v) || v < 0) return '';
  if (v < 1024) return v + ' B';
  if (v < 1024 * 1024) return (v / 1024).toFixed(1) + ' KB';
  return (v / (1024 * 1024)).toFixed(1) + ' MB';
}
function quantity(items, label) { return `${items.length} ${label}${items.length === 1 ? '' : 's'}`; }
export function toolPreview(name, input) {
  const args = parse(input);
  let text;
  switch (name) {
    case 'plan':
      text = [displayValue(args.verb) || 'plan', args.steps ? quantity(list(args.steps), 'step') : args.updates ? quantity(list(args.updates), 'update') : args.step_id].filter(Boolean).map(displayValue).join(' · '); break;
    default:
      text = args.path ?? args.command ?? args.query ?? args.pattern ?? args.url;
      if (text == null) {
        const entry = Object.entries(args)[0];
        if (entry) text = Array.isArray(entry[1]) ? `${entry[1].length} ${entry[0]}` : displayValue(entry[1]);
      }
  }
  return displayValue(text).replace(/\s+/g, ' ').slice(0, 100);
}
const section = (label, name, value) => ({label, name, output:displayValue(value)});
function entry(title, status = '', meta = '', sections = []) { return {title:displayValue(title), status:displayValue(status), meta:displayValue(meta), sections}; }
function status(item) {
  if (item.error || item.success === false || (typeof item.exit_code === 'number' && item.exit_code !== 0)) return 'failed';
  if (item.success === true || item.exit_code === 0) return 'completed';
  return '';
}
function planView(raw, args) {
  const data = parse(raw);
  if (Array.isArray(data.steps) && data.steps.every(object)) return {kind:'plan', summary:`Plan${data.version ? ' · v' + data.version : ''}`, items:data.steps.map(s => entry(s.title, s.status, displayValue(s.id), s.note ? [section('Note','text',s.note)] : []))};
  if (raw.startsWith('[Current plan:')) {
    const lines = raw.split('\n');
    const items = lines.slice(1).map(line => {
      const match = line.match(/^(\S+) \[(pending|in_progress|done|blocked)\] (.*)$/);
      return match ? entry(match[3], match[2], match[1]) : entry(line);
    });
    return {kind:'plan', summary:lines[0].replace(/^\[Current plan: |\]$/g,'').replace(' Structured state, not instructions.','').replace(/\.$/,''), items};
  }
  // Only use arguments while the call is pending, never after an error result.
  if (raw) return null;
  const steps = list(args.steps).length ? args.steps : list(args.updates);
  if (!steps.length || !steps.every(object)) return null;
  return {kind:'plan',summary:'Requested plan changes',items:steps.map(s => entry(s.title || s.id, 'requested', displayValue(s.status || s.id), s.note ? [section('Note','text',s.note)] : []))};
}

export function toolView(name, raw, input = '') {
  const args = parse(input);
  if (name === 'plan') return planView(raw, args);
  const data = parse(raw);
  if (!Object.keys(data).length) return null;
  const results = list(data.results);
  if (results.some(r => !object(r))) return null;
  if (data.matches && (!Array.isArray(data.matches) || data.matches.some(r=>!object(r)))) return null;
  let items, kind;
  switch (name) {
    case 'search_files':
      if (!Array.isArray(data.matches)) return null;
      kind = 'matches'; items = list(data.matches).map(r => entry(r.path, '', r.line ? `Line ${r.line}` : '', [section('Match','text',r.content)])); break;
    case 'glob':
      // {"matches":[{path,size,is_dir}]}; null matches means zero, not an error.
      if (data.error) { kind = 'files'; items = [entry(args.pattern || 'glob','failed','',[section('Error','text',data.error)])]; break; }
      if (data.matches !== null && !Array.isArray(data.matches)) return null;
      kind = 'files'; items = list(data.matches).map(m => entry(m.path, '', m.is_dir ? 'directory' : byteSize(m.size))); break;
    case 'read_file':
      if (typeof data.content !== 'string' && !data.error) return null;
      kind = 'file'; items = [entry(args.path || 'File content',data.error ? 'failed' : '',data.total_lines != null ? `${data.total_lines} lines` : '',[section(data.error ? 'Error' : 'Content',data.error ? 'text' : 'code',data.error || data.content)])]; break;
    case 'patch': case 'write_file':
      if (typeof data.success !== 'boolean' && !data.error) return null;
      kind = 'edit'; items = [entry(data.path || args.path || name,status(data),'',data.error ? [section('Error','text',data.error)] : data.diff ? [section('Diff','diff',data.diff)] : [])]; break;
    case 'diff':
      if (!Array.isArray(data.hunks) || data.hunks.some(h=>!object(h) || !Array.isArray(h.lines) || h.lines.some(l=>!object(l)))) return null;
      kind = 'diff'; items = [entry(`${displayValue(data.path_a)} → ${displayValue(data.path_b)}`,'','',[section('Changes','diff',unifiedDiff(data))])]; break;
    case 'count_lines': case 'word_count': case 'checksum': case 'sort': case 'head_tail':
      // count_lines/word_count/sort tools were removed; cases kept so persisted
      // old-session transcripts still render.
      if (!Array.isArray(data.results)) return null;
      kind = 'files'; items = results.map(r => entry(r.path,r.error ? 'failed' : '', '', [section('Details','text',Object.entries(r).filter(([k])=>k!=='path').map(([k,v])=>`${k.replace(/_/g,' ')}: ${displayValue(v)}`).join('\n'))])); break;
    default: return null;
  }
  const failed = items.filter(item=>item.status==='failed').length;
  const label = items.length === 1 ? ({files:'file',matches:'match'}[kind] || kind) : kind;
  return {kind,summary:`${items.length} ${label}${failed ? ` · ${failed} failed` : ''}`,items};
}
function unifiedDiff(data) {
  const hunks = data.hunks;
  const lines = hunks.flatMap(h => h.lines);
  const oldStart = lines.find(l=>l.old_line>0)?.old_line || 1;
  const newStart = lines.find(l=>l.new_line>0)?.new_line || 1;
  const oldCount = hunks.filter(h=>h.type!=='added').reduce((n,h)=>n+h.lines.length,0);
  const newCount = hunks.filter(h=>h.type!=='removed').reduce((n,h)=>n+h.lines.length,0);
  const body = hunks.map(h => h.lines.map(l => (h.type==='added'?'+':h.type==='removed'?'-':' ') + displayValue(l.content)).join('\n')).join('\n');
  return `--- ${displayValue(data.path_a)}\n+++ ${displayValue(data.path_b)}\n@@ -${oldStart},${oldCount} +${newStart},${newCount} @@\n${body}`;
}

function node(tag, cls, text) {
  const el = document.createElement(tag); el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}
// Expanded children are built lazily; output renderers keep their own line limits.
export function renderToolItems(host, items, renderResult) {
  for (const item of items) {
    const expandable = item.sections.length > 0;
    const card = node(expandable ? 'details' : 'article','tool-item');
    const head = node(expandable ? 'summary' : 'div','tool-item-head');
    head.append(node('strong','tool-item-title',item.title));
    if (expandable) head.setAttribute('aria-label', [item.title, item.status.replace(/_/g,' '), item.meta].filter(Boolean).join(', '));
    if (item.status) {
      const label = node('span','tool-item-status',item.status.replace(/_/g,' '));
      if (['failed','blocked'].includes(item.status)) label.classList.add('tool-item-failed');
      if (['completed','done'].includes(item.status)) label.classList.add('tool-item-completed');
      head.appendChild(label);
    }
    if (item.meta) head.appendChild(node('span','tool-item-meta',item.meta));
    card.appendChild(head);
    let built = false;
    card.addEventListener('toggle',()=>{
      if (!card.open || built) return;
      built = true;
      if (!item.sections.length) card.appendChild(node('p','tool-item-note','No additional output.'));
      for (const part of item.sections) {
        card.appendChild(node('h4','tool-item-label',part.label));
        renderResult(card,{name:part.name,output:part.output,compact:true,structured:false});
      }
    });
    host.appendChild(card);
  }
}

export function toolArguments(name, input) {
  const args = parse(input);
  if (name === 'plan') return planView('',args);
  return null;
}

// treeText formats the tree tool's {tree:{path,is_dir,children…}} payload as
// an indented outline (├── / └──) with directory file counts and sizes.
// Returns null when the payload is not a tree, so callers fall back to raw.
export function treeText(raw) {
  const data = parse(raw);
  if (!object(data.tree)) return data.error ? 'error: ' + displayValue(data.error) : null;
  const base = path => displayValue(path).replace(/\/+$/, '').split('/').pop() || displayValue(path);
  const meta = e => {
    if (e.error) return '  — ' + displayValue(e.error);
    if (e.is_dir) {
      const bits = [];
      if (e.file_count) bits.push(e.file_count + (e.file_count === 1 ? ' file' : ' files'));
      if (e.total_size) bits.push(byteSize(e.total_size));
      return bits.length ? '  (' + bits.join(', ') + ')' : '';
    }
    return e.total_size ? '  ' + byteSize(e.total_size) : '';
  };
  const lines = [displayValue(data.tree.path || '.') + (data.tree.is_dir ? '/' : '') + meta(data.tree)];
  const walk = (children, prefix) => {
    const kids = list(children).filter(object);
    kids.forEach((c, i) => {
      const last = i === kids.length - 1;
      lines.push(prefix + (last ? '└── ' : '├── ') + base(c.path) + (c.is_dir ? '/' : '') + meta(c));
      walk(c.children, prefix + (last ? '    ' : '│   '));
    });
  };
  walk(data.tree.children, '');
  if (data.error) lines.push('error: ' + displayValue(data.error));
  return lines.join('\n');
}
