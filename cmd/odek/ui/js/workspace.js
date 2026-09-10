// Workspace navigation, per-session drafts and the inspectable result collection.
import { S } from './state.js';
import { renderResult, resultKind } from './results.js';
import { togglePanels } from './panels.js';
import { toolPreview } from './toolviews.js';

const byId = id => document.getElementById(id);
function textNode(tag, cls, text) {
  const el = document.createElement(tag); el.className = cls; el.textContent = text; return el;
}
const outputs = [];
export function clearResults() {
  outputs.length = 0;
  const budget=byId("budget-view");if(budget){budget.textContent="Budget usage appears after the first iteration.";budget.classList.remove("budget-exhausted");}
  S.resetArtifacts?.();
  const detail=byId('output-detail');if(detail){detail.textContent='';detail.hidden=true;}
  const list=byId('output-list');if(list)list.hidden=false;
  paintResults();
}
function paintResults() {
  const list = byId('output-list');
  if (!list) return;
  list.textContent = '';
  if (!outputs.length) list.appendChild(textNode('p', 'workspace-empty', 'Results will appear here as odek works. Open any result to inspect its full output.'));
  for (const item of outputs) {
    const button = textNode('button', 'output-item', ''); button.type = 'button';
    button.append(textNode('span', 'output-kind', resultKind(item.name, item.output)), textNode('strong', 'output-name', item.name), textNode('span', 'output-path', item.preview));
    button.addEventListener('click', () => inspect(item)); list.appendChild(button);
  }
  const count = byId('output-count'); if (count) count.textContent = String(outputs.length);
}
function inspect(item) {
  const detail = byId('output-detail'); if (!detail) return;
  detail.textContent = '';
  const back = textNode('button', 'result-action', '← All results'); back.type = 'button';
  back.addEventListener('click', () => { detail.hidden = true; byId('output-list').hidden = false; });
  const jump = textNode('button', 'result-action', 'Show in conversation'); jump.type = 'button';
  jump.addEventListener('click', () => {
    if (item.block.isConnected) { item.block.scrollIntoView({ block: 'center', behavior: 'auto' }); item.block.querySelector('.tb-header')?.focus(); }
  });
  detail.append(back, jump, textNode('h3', 'output-title', item.name));
  renderResult(detail, item);
  detail.hidden = false; byId('output-list').hidden = true;
  togglePanels(true); byId('ptab-outputs')?.click();
}
S.recordResult = (block, output) => {
  const item = { block, output, name: block.dataset.toolName || 'tool', args: block.dataset.toolArgs || '', preview: toolPreview(block.dataset.toolName || '', block.dataset.toolArgs || '') };
  item.stepId=(S.plan?.steps || []).find(step=>step.status==='in_progress')?.id || '';
  outputs.push(item);
  while(outputs.length>300 || outputs.reduce((n,result)=>n+result.output.length,0)>16*1024*1024)outputs.shift();
  paintResults();
  const body = block.querySelector('.tb-body');
  if (body) { const button = textNode('button', 'result-inspect', 'Inspect result ↗'); button.type = 'button'; button.addEventListener('click', () => inspect(item)); body.appendChild(button); }
};
S.clearResults = clearResults;

export function saveDraft(id = S.sessionId) {
  try {
    const text = byId('prompt')?.value || '';
    const key = 'odek_draft_' + (id || 'new');
    if (text) sessionStorage.setItem(key, text.slice(0, 100000)); else sessionStorage.removeItem(key);
  } catch { /* storage unavailable */ }
}
export function restoreDraft(id = S.sessionId) {
  try { const input = byId('prompt'); if (input) { input.value = sessionStorage.getItem('odek_draft_' + (id || 'new')) || ''; input.style.height = 'auto'; } } catch { /* empty draft */ }
}
S.saveDraft = saveDraft; S.restoreDraft = restoreDraft;
byId('prompt')?.addEventListener('input', () => saveDraft());
restoreDraft();

const density = byId('density-btn');
function setDensity(value) {
  document.body.classList.toggle('density-compact', value === 'compact');
  if (density) { density.textContent = value === 'compact' ? 'Compact' : 'Comfortable'; density.setAttribute('aria-pressed', String(value === 'compact')); }
  try { localStorage.setItem('odek_density', value); } catch { /* optional */ }
}
try { setDensity(localStorage.getItem('odek_density') || 'comfortable'); } catch { setDensity('comfortable'); }
density?.addEventListener('click', () => setDensity(document.body.classList.contains('density-compact') ? 'comfortable' : 'compact'));

S.sessionRailOpen = false;
function syncSessionRail() {
  const wide = document.body.classList.contains('workspace-wide');
  const open = wide && S.sessionRailOpen;
  document.body.classList.toggle('sessions-open', open);
  const rail = byId('session-rail');
  if (rail) { rail.hidden = !open; rail.setAttribute('aria-hidden', String(!open)); }
  const button = byId('hamburger-btn');
  if (button && wide) { button.setAttribute('aria-expanded', String(open)); button.setAttribute('aria-controls','session-rail'); }
  else if (button) button.setAttribute('aria-controls','panels');
}
S.toggleSessionRail = () => { S.sessionRailOpen = !S.sessionRailOpen; syncSessionRail(); };

// Move the existing session navigation instead of maintaining duplicate lists.
if (typeof matchMedia === 'function') {
  const wide = matchMedia('(min-width: 1100px)');
  const relocate = () => {
    const target = byId(wide.matches ? 'session-rail' : 'ppanel-sessions');
    const sidebar = byId('sidebar'); if (target && sidebar) target.appendChild(sidebar);
    document.body.classList.toggle('workspace-wide', wide.matches);
    syncSessionRail();
  };
  wide.addEventListener('change', relocate); relocate();
}
const resize = byId('inspector-resize');
let start = null;
const applyWidth = value => {
  const width = Math.max(320, Math.min(700, value));
  document.documentElement.style.setProperty('--inspector-width', width + 'px');
  resize?.setAttribute('aria-valuenow', String(Math.round(width)));
  try { localStorage.setItem('odek_inspector_width', String(width)); } catch { /* optional */ }
};
try { const saved = Number(localStorage.getItem('odek_inspector_width')); if (saved) applyWidth(saved); } catch { /* default */ }
resize?.addEventListener('pointerdown', e => { start = { x: e.clientX, width: byId('panels').getBoundingClientRect().width }; resize.setPointerCapture(e.pointerId); });
resize?.addEventListener('pointermove', e => { if (start) applyWidth(start.width + start.x - e.clientX); });
resize?.addEventListener('pointerup', () => { start = null; });
resize?.addEventListener('pointercancel', () => { start = null; });
resize?.addEventListener('keydown', e => { if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') { e.preventDefault(); applyWidth(byId('panels').getBoundingClientRect().width + (e.key === 'ArrowLeft' ? 24 : -24)); } });
paintResults();

S.onRuntimeEvent = event => {
  if (event?.session_id && event.session_id !== S.sessionId) return;
  const root = byId('budget-view'); if (!root) return;
  if (event?.type === 'budget_exceeded') {
    root.textContent = 'Stopped: ' + (event.data?.limit_name || 'execution') + ' budget reached. Your completed work has been preserved.';
    root.classList.add('budget-exhausted'); return;
  }
  const budget = event?.data?.budget; if (!budget) return;
  root.textContent = ''; root.classList.remove('budget-exhausted');
  const dimensions = [['Runtime', 'RuntimeSeconds', 's'], ['Tool calls','ToolCalls',''], ['Input tokens','InputTokens',''], ['Output tokens','OutputTokens',''], ['Cost','CostUSD',' USD']];
  let count=0;
  for (const [label,key,unit] of dimensions) {
    const max=budget['Max'+key]; if (!(max>0)) continue; count++;
    const remaining=budget['Remaining'+key] || 0;
    const row=textNode('div','budget-row','');
    row.append(textNode('span','',label),textNode('span','budget-value',Number(remaining.toFixed(3)).toLocaleString()+unit+' left / '+max+unit));
    const meter=document.createElement('progress');meter.max=max;meter.value=max-remaining;meter.setAttribute('aria-label',label+' consumed');row.appendChild(meter);root.appendChild(row);
  }
  if(!count)root.textContent='No execution limits configured.';
};

S.showStepResults = stepId => {
  const matches=outputs.filter(item=>item.stepId===stepId);
  const detail=byId('output-detail');if(!detail)return;detail.textContent='';
  detail.appendChild(textNode('p','management-note','Results recorded while this step was active. This is a timeline association, not a claim of validation.'));
  if(!matches.length)detail.appendChild(textNode('p','workspace-empty','No recorded tool results for this step in the current view.'));
  for(const item of matches){const button=textNode('button','output-item',item.name+' · '+item.preview);button.type='button';button.addEventListener('click',()=>inspect(item));detail.appendChild(button);}
  detail.hidden=false;byId('output-list').hidden=true;togglePanels(true);byId('ptab-outputs')?.click();
};
