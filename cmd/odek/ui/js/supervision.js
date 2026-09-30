import { S, getSessionToken, ensureSessionToken } from './state.js';
import { apiFetch, getSession } from './api.js';
import { apiHeaders, wsSend } from './net.js';
import { openTab } from './commands.js';
import { openDialog, closeDialog, announce, showToast } from './utils.js';
import { send } from './input.js';

const byId=id=>document.getElementById(id);
const runSettings=byId('run-settings');
runSettings?.addEventListener('keydown',event=>{if(event.key==='Escape'){runSettings.open=false;runSettings.querySelector('summary')?.focus();event.stopPropagation();}});
document.addEventListener('click',event=>{if(runSettings?.open && !runSettings.contains(event.target))runSettings.open=false;});
function node(tag,cls,text){const el=document.createElement(tag);if(cls)el.className=cls;if(text!=null)el.textContent=text;return el;}
function action(label,fn){const el=node('button','management-action',label);el.type='button';el.addEventListener('click',fn);return el;}
const DIMENSIONS=[['Runtime','max_runtime_seconds','limit-runtime','s'],['Tool calls','max_tool_calls','limit-tools',''],['Input tokens','max_input_tokens','limit-input',''],['Output tokens','max_output_tokens','limit-output',''],['Cost','max_cost_usd','limit-cost',' USD']];
S.readRunLimits=()=>Object.fromEntries(DIMENSIONS.map(([,key,id])=>[key,Number(byId(id)?.value || 0)]).filter(([,v])=>Number.isFinite(v)&&v>0));
let workspace;
function paintLimits(){
 const root=byId('effective-limits');if(!root||!workspace)return;root.textContent='';
 const requested=S.readRunLimits();let count=0;
 for(const [label,key,,unit] of DIMENSIONS){const base=workspace.limits?.[key] || 0;const value=requested[key]>0?(base>0?Math.min(base,requested[key]):requested[key]):base;if(value>0){root.appendChild(node('p','management-note',label+': '+value.toLocaleString()+unit));count++;}}
 if(!count)root.appendChild(node('p','management-note','No execution caps configured.'));
 const prices=workspace.limits || {};const model=S.currentModel || workspace.model;const pair=prices.model_prices?.[model] || {};
 if((requested.max_cost_usd || prices.max_cost_usd) && !((pair.input_cost_per_million_usd || prices.input_cost_per_million_usd)>0 && (pair.output_cost_per_million_usd || prices.output_cost_per_million_usd)>0))root.appendChild(node('p','management-note','Cost enforcement is inactive without configured input and output prices.'));
}
for(const [,,id] of DIMENSIONS)byId(id)?.addEventListener('input',paintLimits);
byId('model-picker')?.addEventListener('change',()=>queueMicrotask(paintLimits));
apiFetch('/api/workspace').then(data=>{workspace=data;S.workspace=data;byId('execution-scope').textContent=data.workspace+' · '+(data.sandbox?'Docker sandbox':'Local execution');
 const policy=node('details','permission-policy');policy.appendChild(node('summary','','Permission policy'));
 for(const [cls,value] of Object.entries(data.policy||{}))policy.appendChild(node('p','management-note',cls.replaceAll('_',' ')+': '+value));byId('effective-limits').parentNode.appendChild(policy);paintLimits();
}).catch(()=>{byId('execution-scope').textContent='Execution context unavailable · check server connection';});

S.requestFollowup=text=>{const input=byId('prompt');input.value=text;input.dispatchEvent(new Event('input',{bubbles:true}));S.closePanels?.();input.focus();};
S.refreshPermissions=()=>wsSend(S.ws,{type:'permissions_get'});
byId('permissions-revoke')?.addEventListener('click',()=>{if(!wsSend(S.ws,{type:'permissions_revoke'}))showToast('Reconnect before revoking grants.');});
S.onPermissions=event=>{const root=byId('permissions-view');if(root)root.textContent=event.classes?.length?'Temporary grants: '+event.classes.map(cls=>cls.replaceAll('_',' ')).join(', '):'No temporary grants. Operator policy applies.';};

S.refreshSupervision=()=>{
 const title=byId('task-status');if(title)title.textContent=S.stopRequested&&S.busy?'Stopping…':S.busy?'Working on your task':S.recoveryReason?'Work needs review':'Ready to continue';
 const root=byId('attention-view');if(!root)return;root.textContent='';
 const items=[];
 if(S.activeApprovalId)items.push(['Approval waiting',()=>{S.closePanels?.();S.activeApprovalCard?.scrollIntoView?.({block:'center'});S.activeApprovalCard?.focus();}]);
 if(S.activeClarifyCard)items.push(['Answer the agent',()=>{S.closePanels?.();S.activeClarifyCard.scrollIntoView?.({block:'center'});S.activeClarifyCard.querySelector('textarea')?.focus();}]);
 for(const step of S.plan?.steps||[])if(step.status==='blocked')items.push(['Blocked: '+step.title,()=>S.requestFollowup('Help resolve blocked step '+step.id+': '+step.title)]);
 if(S.failedChecks)items.push([S.failedChecks+' failed checks · review evidence',()=>openTab('outputs')]);
 if(S.recoveryReason)items.push(['Interrupted work · review saved progress',()=>reviewRecovery()]);
 if(S.knowledgeNeedsReview)items.push(['Review memory before allowing reuse',()=>openTab('memory')]);
 for(const item of S.agentAttention?.values() || [])items.push([item.label+' · '+item.status,()=>S.requestFollowup('Review delegated work: '+item.label+'. Verify its evidence and continue unfinished work.')]);
 const activeJobs=(S.jobs||[]).filter(job=>job.status==='running');if(!S.busy && activeJobs.length)items.push([activeJobs.length+' background jobs still running',()=>openTab('now')]);
 if(!items.length)root.appendChild(node('p','management-note','Nothing needs your decision.'));
 for(const [label,fn] of items)root.appendChild(action(label,fn));
 const chip=byId('attention-chip');if(chip){chip.hidden=!items.length;chip.textContent=items.length+' need attention';}
};

let recoveryVersion=0;
export async function reviewRecovery(){
 const sid=S.sessionId;if(!sid){showToast('Open a session first.');return;}
 openTab('now');const root=byId('recovery-view');root.hidden=false;root.textContent='Checking saved progress…';const version=++recoveryVersion;
 try{
  const data=await apiFetch('/api/sessions/'+encodeURIComponent(sid)+'/recovery',{sessionToken:getSessionToken(sid)});
  if(version!==recoveryVersion||sid!==S.sessionId)return;root.textContent='';root.appendChild(node('h4','ws-head','Saved progress'));
  for(const [key,label] of [['completed','Recorded tool returns'],['failed','Failed actions'],['uncertain','Actions with uncertain outcomes']]){
   root.appendChild(node('p','management-note',label+': '+(data[key]||[]).length));
   for(const item of data[key]||[])root.appendChild(node('p','management-note',item.name+' · '+item.outcome));
  }
  root.appendChild(node('p','management-note',data.warning));
  S.restoreDecisionReceipts?.(data.decisions || []);
  const continueButton=action('Continue from saved progress',async()=>{
   continueButton.disabled=true;
   try{
    const saved=await getSession(sid,getSessionToken(sid));if(sid!==S.sessionId||version!==recoveryVersion)return;
    if(S.busy||S.uploading||S.attachedFiles.length){showToast('Finish the current work and remove attachments before continuing saved progress.');return;}
    if(saved.revision!==data.revision||saved.generation!==data.generation){showToast('Saved progress changed. Refresh recovery.');await reviewRecovery();return;}
    byId('prompt').value='Continue the original task from the saved conversation. Inspect the current workspace and recorded outcomes first. Avoid repeating completed actions; reconcile uncertain side effects before proceeding.';
    S.closePanels?.();byId('prompt').focus();send({recovery_revision:data.revision,recovery_generation:data.generation});
   }catch(e){showToast(e.message);}finally{continueButton.disabled=false;}
  });
  continueButton.disabled=S.busy;
  root.append(continueButton,action('Refresh saved progress',reviewRecovery),action('Prepare to run again',()=>{
   const original=data.original_prompt || ''; 
   if(!original){showToast('Write the prompt again and reattach any files before running.');return;}
   S.requestFollowup(original);showToast('Review before running again and reattach any files. Completed actions may be repeated.');
  }));
 }catch(e){if(version===recoveryVersion)root.textContent=e.message;}
}
byId('recover-btn')?.addEventListener('click',reviewRecovery);
S.onRecoveryNeeded=reason=>{S.recoveryReason=reason;S.refreshSupervision();};
S.resetSupervision=()=>{recoveryVersion++;S.recoveryReason='';S.failedChecks=0;S.agentAttention=new Map();const root=byId('recovery-view');if(root){root.hidden=true;root.textContent='';}S.refreshSupervision();};

// Context references use the existing resolver and session-token contract.
let contextSequence=0;
async function searchContext(){
 const sequence=++contextSequence;const query=byId('context-search').value.trim();const root=byId('context-results');root.textContent='Searching…';
 try{
  const response=await fetch('/api/resources?q='+encodeURIComponent(query)+'&limit=20',{headers:apiHeaders()});if(!response.ok)throw new Error('Context search unavailable');const items=await response.json();if(sequence!==contextSequence)return;
  root.textContent='';
  if(!items.length)root.appendChild(node('p','management-note',query?'No matching context.':'Search a filename, or type sess: to browse saved sessions.'));
  for(const item of items){root.appendChild(action(item.label+' · '+item.type,async()=>{
   const owner=S.sessionId;
   if(item.type==='session'){const sid=item.id.replace(/^@?sess:/,'');if(!await ensureSessionToken(sid)){showToast('Session context unavailable.');return;}}
   if(owner!==S.sessionId)return;
   const input=byId('prompt');const reference=item.id.startsWith('@')?item.id:'@'+item.id;if(!input.value.includes(reference))input.value+=(input.value?' ':'')+reference+' ';
   input.dispatchEvent(new Event('input',{bubbles:true}));announce('Added context: '+item.label);
  }));}
 }catch(e){if(sequence===contextSequence)root.textContent=e.message;}
}
byId('context-btn')?.addEventListener('click',()=>{openDialog(byId('context-overlay'));searchContext();});
let searchTimer;byId('context-search')?.addEventListener('input',()=>{clearTimeout(searchTimer);searchTimer=setTimeout(searchContext,200);});
byId('context-close')?.addEventListener('click',closeDialog);

const size=byId('reading-size');
function setReadingSize(value){const n=[15,17,19].includes(Number(value))?Number(value):15;document.documentElement.style.setProperty('--reading-size',n+'px');if(size)size.value=String(n);try{localStorage.setItem('odek_reading_size',String(n));}catch{/* optional preference */}}
try{setReadingSize(localStorage.getItem('odek_reading_size'));}catch{setReadingSize(15);}
size?.addEventListener('change',()=>setReadingSize(size.value));
byId('all-activity-btn')?.addEventListener('click',()=>openTab('activity'));
byId('attention-chip')?.addEventListener('click',()=>openTab('now'));
S.refreshSupervision();

let decisions=new Map();
function paintDecisions(){
 const root=byId('decision-receipts');if(!root)return;root.textContent='';
 for(const item of decisions.values()){
  const card=node('article','decision-receipt');
  const action=item.action==='trust'?'Temporary grant until disconnect':item.action || item.kind;
  card.append(node('strong','',action+' · '+item.state),node('p','management-note',item.command || 'Principal decision'));
  if(item.risk)card.appendChild(node('p','management-note',item.risk.replaceAll('_',' ')+' · '+(item.turn_id || 'current turn')));
  root.appendChild(card);
 }
}
S.recordDecision=item=>{item={turn_id:S.currentTurnId || undefined,...item};const previous=decisions.get(item.id);if(previous?.state==='accepted' && item.state!=='accepted')return;decisions.set(item.id,item);if(decisions.size>128)decisions.delete(decisions.keys().next().value);paintDecisions();};
S.restoreDecisionReceipts=items=>{decisions=new Map(items.map(item=>[item.id,item]));paintDecisions();};
S.clearDecisionReceipts=()=>{decisions.clear();paintDecisions();};
S.validateRunLimits=()=>{
 for(const [label,,id] of DIMENSIONS){const input=byId(id);if(input?.value && (!input.checkValidity() || !Number.isFinite(Number(input.value)))){showToast('Review the '+label.toLowerCase()+' limit.');input.focus();return false;}}
 return true;
};

S.recordAgentOutcome=item=>{
 S.agentAttention ||= new Map();
 if(item.status==='success')S.agentAttention.delete(item.id);
 else {S.agentAttention.set(item.id,item);if(S.agentAttention.size>128)S.agentAttention.delete(S.agentAttention.keys().next().value);}
 S.refreshSupervision?.();
};
