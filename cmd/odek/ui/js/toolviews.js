// Tool-specific presentation models. Never infer success from optimistic prose.
import { parseUntrusted } from './untrusted.js';

const object = value => value && typeof value === 'object' && !Array.isArray(value);
const list = value => Array.isArray(value) ? value : [];
export function displayValue(value) {
  if (value == null) return '';
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  return parseUntrusted(text).map(part => part.body).join('');
}
function parse(value) {
  if (object(value)) return value;
  try { const data = JSON.parse(value); return object(data) ? data : {}; } catch { return {}; }
}
function quantity(items, label) { return `${items.length} ${label}${items.length === 1 ? '' : 's'}`; }
export function toolPreview(name, input) {
  const args = parse(input);
  let text;
  switch (name) {
    case 'plan':
      text = [displayValue(args.verb) || 'plan', args.steps ? quantity(list(args.steps), 'step') : args.updates ? quantity(list(args.updates), 'update') : args.step_id].filter(Boolean).map(displayValue).join(' · '); break;
    case 'parallel_shell': text = quantity(list(args.commands), 'command'); break;
    case 'batch_read': text = quantity(list(args.files), 'file'); break;
    case 'batch_patch': text = quantity(list(args.patches), 'edit'); break;
    case 'http_batch': text = quantity(list(args.requests), 'request'); break;
    case 'multi_grep': text = quantity(list(args.patterns), 'pattern') + (args.path ? ' · ' + displayValue(args.path) : ''); break;
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
  if (results.some(r => !object(r)) && !name.startsWith('batch_')) return null;
  if (['batch_read','batch_patch'].includes(name) && results.some(r=>!object(r))) return null;
  if (data.matches && (!Array.isArray(data.matches) || data.matches.some(r=>!object(r)))) return null;
  let items, kind;
  switch (name) {
    case 'parallel_shell':
      if (!Array.isArray(data.results)) return null;
      kind = 'commands';
      items = results.map((r,i) => entry(r.command || `Command ${i+1}`, status(r), [typeof r.exit_code === 'number' ? `Exit ${r.exit_code}` : 'Exit unavailable', typeof r.duration_ms === 'number' ? `${r.duration_ms} ms` : ''].filter(Boolean).join(' · '), [section('stdout','terminal',r.stdout), ...(r.stderr ? [section('stderr','terminal',r.stderr)] : []), ...(r.error ? [section('Error','text',r.error)] : [])])); break;
    case 'batch_read':
      if (!Array.isArray(data.results)) return null;
      kind = 'files'; items = results.map(r => entry(r.path, r.error ? 'failed' : '', typeof r.total_lines === 'number' ? `${r.total_lines} lines` : '', [section(r.error ? 'Error' : 'Content',r.error ? 'text' : 'read_file',r.error || r.content)])); break;
    case 'batch_patch':
      if (!Array.isArray(data.results)) return null;
      kind = 'edits'; items = results.map(r => entry(r.path, status(r), '', [section(r.error ? 'Error' : 'Diff',r.error ? 'text' : 'diff',r.error || r.diff)])); break;
    case 'http_batch':
      if (!Array.isArray(data.results)) return null;
      kind = 'requests'; items = results.map(r => entry(r.url, r.error || r.status >= 400 ? 'failed' : '', r.error ? 'Request failed' : `HTTP ${r.status ?? 'unknown'}`, [section(r.error ? 'Error' : 'Response','text',r.error || (r.content_length != null ? `${r.content_length} bytes` : 'No response size reported'))])); break;
    case 'multi_grep':
      if (!Array.isArray(data.results)) return null;
      kind = 'searches'; items = results.map(r => entry(r.pattern, r.error ? 'failed' : '', `${r.count ?? list(r.matches).length} matches`, [section(r.error ? 'Error' : 'Matches',r.error ? 'text' : 'search_files',r.error || matchText(r.matches)), ...(list(r.skipped).length ? [section('Skipped','text',r.skipped.join('\n'))] : [])])); break;
    case 'search_files':
      if (!Array.isArray(data.matches)) return null;
      kind = 'matches'; items = list(data.matches).map(r => entry(r.path, '', r.line ? `Line ${r.line}` : '', [section('Match','text',r.content)])); break;
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
      if (!Array.isArray(data.results)) return null;
      kind = 'files'; items = results.map(r => entry(r.path,r.error ? 'failed' : '', '', [section('Details','text',Object.entries(r).filter(([k])=>k!=='path').map(([k,v])=>`${k.replace(/_/g,' ')}: ${displayValue(v)}`).join('\n'))])); break;
    default:
      // Extension batch tools still get per-item inspection without invented semantics.
      if (!name.startsWith('batch_') || !Array.isArray(data.results)) return null;
      kind = 'batch'; items = results.map((r,i)=>entry(object(r) ? r.path || r.name || `Item ${i+1}` : `Item ${i+1}`, '', '', [section('Result','text',r)]));
  }
  const failed = items.filter(item=>item.status==='failed').length;
  const label = items.length === 1 ? ({commands:'command',files:'file',edits:'edit',requests:'request',searches:'search',matches:'match',batch:'item'}[kind] || kind) : kind;
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
function matchText(matches) {
  return list(matches).filter(object).map(r => `${displayValue(r.path)}${r.line ? ':'+r.line : ''}: ${displayValue(r.content)}`).join('\n');
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
  const key = {parallel_shell:'commands',batch_read:'files',batch_patch:'patches',http_batch:'requests',multi_grep:'patterns'}[name];
  if (!key || !Array.isArray(args[key])) return null;
  return {summary:toolPreview(name,input),items:args[key].map((value,i)=>entry(object(value) ? value.command || value.path || value.url || `Item ${i+1}` : value, 'requested', '', [section('Arguments','text',JSON.stringify(value,null,2))]))};
}
