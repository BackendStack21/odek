// Workspace navigation, per-session drafts and the inspectable result collection.
import { S } from './state.js';
import { renderResult, resultKind } from './results.js';
import { toolPreview } from './toolviews.js';
import { openTab } from './commands.js';
import { summarizeEvidence } from './evidence.js';

const byId = id => document.getElementById(id);
function textNode(tag, cls, text) {
  const el = document.createElement(tag); el.className = cls; el.textContent = text; return el;
}
const outputs = [];S.activityPruned=false;
export function clearResults() {
  outputs.length = 0;S.activityPruned=false;
  const budget=byId("budget-view");if(budget){budget.textContent="Budget usage appears after the first iteration.";budget.classList.remove("budget-exhausted");}
  S.resetArtifacts?.();
  S.resetSupervision?.();S.clearDecisionReceipts?.();
  const detail=byId('output-detail');if(detail){detail.textContent='';detail.hidden=true;}
  const list=byId('output-list');if(list)list.hidden=false;
  paintResults();
}
function paintResults() {
  const list = byId('output-list');
  if (!list) return;
  list.textContent = '';
  if (!outputs.length) list.appendChild(textNode('p', 'workspace-empty', 'Results will appear here as odek works. Open any result to inspect its full output.'));
  if(S.activityPruned)list.appendChild(textNode('p','management-note','Older raw results were removed from this view to limit memory. Reload the saved session to review persisted history.'));
  const query=(byId('activity-search')?.value || '').toLowerCase();
  for (const item of outputs) {
    if(query && !(item.name+' '+item.preview+' '+item.outcome).toLowerCase().includes(query))continue;
    const button = textNode('button', 'output-item', ''); button.type = 'button';
    button.dataset.outcome=item.outcome;
    button.append(textNode('span','output-outcome',item.outcome==='completed'?'Returned':item.outcome==='failed'?'Failed':'Unknown'),textNode('span', 'output-kind', resultKind(item.name, item.output)), textNode('strong', 'output-name', item.name), textNode('span', 'output-path', item.preview));
    button.addEventListener('click', () => inspect(item)); list.appendChild(button);
  }
  paintEvidence();
}
function inspect(item) {
  const detail = byId('output-detail'); if (!detail) return;
  detail.textContent = '';
  const back = textNode('button', 'result-action', '← All results'); back.type = 'button';
  back.addEventListener('click', () => { detail.hidden = true; byId('output-list').hidden = false; byId('output-list').querySelector('button')?.focus(); });
  const jump = textNode('button', 'result-action', 'Show in conversation'); jump.type = 'button';
  jump.addEventListener('click', () => {
    S.closePanels?.();
    if (item.block.isConnected) { item.block.scrollIntoView({ block: 'center', behavior: 'auto' }); item.block.querySelector('.tb-header')?.focus(); }
  });
  detail.append(back, jump, textNode('h3', 'output-title', item.name));
  renderResult(detail, item);
  detail.hidden = false; byId('output-list').hidden = true;
  openActivityDetails();detail.tabIndex=-1;detail.focus();
}
// Reveal the collapsed in-panel activity view instead of a separate tab.
function openActivityDetails() {
  openTab('outputs');
  const d = byId('activity-details');
  if (d) { d.open = true; d.scrollIntoView?.({ block: 'start' }); }
}
S.openActivityDetails = openActivityDetails;

S.recordResult = (block, output) => {
  const item = { block, output, outcome:block.dataset.outcome || 'unknown', turnId:block.dataset.turnId || '', callId:block.dataset.callId || '', name: block.dataset.toolName || 'tool', args: block.dataset.toolArgs || '', preview: toolPreview(block.dataset.toolName || '', block.dataset.toolArgs || '') };
  item.stepId=(S.plan?.steps || []).find(step=>step.status==='in_progress')?.id || '';
  outputs.push(item);
  let pruned=false;
  while(outputs.length>300 || outputs.reduce((n,result)=>n+result.output.length,0)>16*1024*1024){outputs.shift();pruned=true;}
  if(pruned)S.activityPruned=true;
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
  const open = S.sessionRailOpen;
  document.body.classList.toggle('sessions-open', open);
  const rail = byId('session-rail');
  if (rail) { rail.hidden = !open; rail.setAttribute('aria-hidden', String(!open)); }
  const button = byId('hamburger-btn');
  if (button) { button.setAttribute('aria-expanded', String(open)); button.setAttribute('aria-controls','session-rail'); }
}
S.toggleSessionRail = () => { S.sessionRailOpen = !S.sessionRailOpen; syncSessionRail(); };

// Keep the workspace-wide marker for CSS/behavior branches; the rail hosts
// the single session list on every width.
if (typeof matchMedia === 'function') {
  const wide = matchMedia('(min-width: 1100px)');
  const relocate = () => {
    const target = byId('session-rail');
    const sidebar = byId('sidebar'); if (target && sidebar && sidebar.parentElement !== target) target.appendChild(sidebar);
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
  detail.hidden=false;byId('output-list').hidden=true;openTab('activity');detail.tabIndex=-1;detail.focus();
};

function paintEvidence(){
 const root=byId('completion-view');if(!root)return;root.textContent='';
 const records=outputs;
 const summary=summarizeEvidence(records);S.failedChecks=summary.checks.filter(check=>check.state==='failed').length;
 root.appendChild(textNode('p','management-note','Evidence recorded in this session. Tool execution and task success are distinct.'));
 root.appendChild(textNode('h4','ws-head','Recorded file edits'));
 if(!summary.files.length)root.appendChild(textNode('p','management-note','No file edits recorded by file tools. Shell or external changes may require separate review.'));
 for(const file of summary.files){const button=textNode('button','output-item',file.path);button.type='button';button.addEventListener('click',()=>inspect(file.item));root.appendChild(button);}
 root.appendChild(textNode('h4','ws-head','Validation checks'));
 if(!summary.checks.length)root.appendChild(textNode('p','management-note','No validation checks recorded. Verification has not been established.'));
 for(const check of summary.checks){
  const row=textNode('button','output-item',check.state+' · '+check.command);row.type='button';row.dataset.outcome=check.state;
  if(check.previous.length)row.appendChild(textNode('span','management-note','Earlier attempts: '+check.previous.join(', ')+' · superseded for this exact command.'));
  row.addEventListener('click',()=>inspect(check.item));root.appendChild(row);
 }
 if(summary.failed || summary.unknown)root.appendChild(textNode('p','management-note',summary.failed+' failed tool calls · '+summary.unknown+' calls with unavailable outcomes. Review Activity for unresolved work.'));
 const count=byId('output-count');if(count)count.textContent=String(summary.files.length+summary.checks.length+(S.artifactCount||0));
 S.refreshSupervision?.();
}
S.paintEvidence=paintEvidence;
byId('activity-search')?.addEventListener('input',paintResults);
