// Operator management forms use the same authenticated REST contracts as chat.
import { getCapabilities, listSchedules, saveSchedule, removeSchedule, getMaintenance, runMaintenance, startRun } from './api.js';
import { showToast } from './utils.js';
const byId = id => document.getElementById(id);
function el(tag, cls, text) { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; }
function field(form, title, value = '', type = 'text') {
  const label = el('label', 'management-field', title);
  const input = el(type === 'textarea' ? 'textarea' : 'input');
  if (type !== 'textarea') input.type = type;
  input.value = value; label.appendChild(input); form.appendChild(label); return input;
}
function action(title, fn) { const b = el('button', 'management-action', title); b.type = 'button'; b.addEventListener('click', async () => { b.disabled = true; try { await fn(); } catch (e) { showToast(e.message); } finally { b.disabled = false; } }); return b; }
let generation = 0;
async function loadManagement() {
  const version = ++generation;
  const root = byId('management-body'); if (!root) return;
  root.textContent = 'Loading…';
  try {
    const caps = await getCapabilities();
    if (version !== generation) return;
    root.textContent = '';
    if (caps.features?.schedules) {
      root.appendChild(el('h3', 'management-title', 'Scheduled work'));
      root.appendChild(el('p', 'management-note', 'Manage recurring tasks. A running scheduler daemon or Telegram host executes enabled schedules.'));
      root.appendChild(action('New schedule', () => editSchedule(root)));
      const data = await listSchedules();
      if (version !== generation) return;
      for (const job of data.jobs || []) {
        const row = el('article', 'management-card');
        row.append(el('strong', '', job.name || job.id), el('p', 'management-note', job.cron + ' · ' + (job.timezone || 'UTC') + ' · ' + (job.enabled ? 'Enabled' : 'Paused')));
        if (data.next?.[job.id]) row.appendChild(el('p', 'management-note', 'Next: ' + new Date(data.next[job.id]).toLocaleString()));
        const state = data.states?.[job.id]; if (state) row.appendChild(el('p', 'management-note', 'Last run: ' + (state.last_status || 'Never') + (state.last_error ? ' · ' + state.last_error : '')));
        row.append(action('Edit', () => editSchedule(root, job)), action(job.enabled ? 'Pause' : 'Enable', async () => { await saveSchedule({...job, enabled:!job.enabled}); await loadManagement(); }), action('Delete', async () => { if (confirm('Delete schedule “' + (job.name || job.id) + '”?')) { await removeSchedule(job.id); await loadManagement(); } }));
        root.appendChild(row);
      }
      if (!(data.jobs || []).length) root.appendChild(el('p', 'workspace-empty', 'No schedules yet. Create one to make recurring work easier.'));
    }
    if (caps.features?.maintenance) {
      const data = await getMaintenance(); if (version !== generation) return;
      root.appendChild(el('h3', 'management-title', 'Storage maintenance'));
      root.appendChild(el('p', 'management-note', data.description));
      const policy = el('dl', 'management-policy');
      for (const [key,value] of Object.entries(data.policy || {})) { policy.append(el('dt','',key.replace(/_/g,' ').replace(/([A-Z])/g,' $1').trim()),el('dd','',typeof value==='number'?value.toLocaleString():String(value))); }
      root.appendChild(policy);
      const form = el('div','management-card');
      const confirmInput = field(form, 'Type cleanup to apply the retention policy');
      form.appendChild(action('Run cleanup', async () => { if (confirmInput.value !== 'cleanup') { showToast('Type cleanup first'); return; } const report = await runMaintenance(confirmInput.value); confirmInput.value = ''; const result=el('pre','management-report',JSON.stringify(report,null,2));form.appendChild(result); }));
      root.appendChild(form);
    }
    if (!caps.features?.schedules && !caps.features?.maintenance) root.appendChild(el('p','workspace-empty','This server does not expose scheduling or maintenance controls.'));
  } catch (e) { if (version === generation) root.textContent = 'Management unavailable: ' + e.message; }
}
function editSchedule(root, job = {}) {
  root.querySelector('.schedule-editor')?.remove();
  const form = el('form','schedule-editor management-card');
  const name=field(form,'Name',job.name || ''); name.maxLength=200;
  const task=field(form,'Task',job.task || '','textarea');task.required=true;task.maxLength=32000;
  const cron=field(form,'Schedule (cron)',job.cron || '0 9 * * 1-5');cron.required=true;
  const tz=field(form,'Timezone',job.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC');
  const deliveryLabel=el('label','management-field','Deliver result to');const delivery=el('select');
  for(const kind of ['log','stdout','telegram']) {const option=el('option','',kind);option.value=kind;delivery.appendChild(option);} delivery.value=job.deliver?.kind || 'log';deliveryLabel.appendChild(delivery);form.appendChild(deliveryLabel);
  const chat=field(form,'Telegram chat ID (0 uses default)',String(job.deliver?.chat_id || 0),'number');
  const enabled=field(form,'Enabled','','checkbox');enabled.checked=!!job.enabled;
  const catchup=field(form,'Run once if a scheduled fire was missed','','checkbox');catchup.checked=!!job.catchup;
  const submit=el('button','management-action','Save schedule');submit.type='submit';
  form.append(submit,action('Cancel',()=>form.remove()));
  form.addEventListener('submit',async e=>{e.preventDefault();submit.disabled=true;try {await saveSchedule({...job,name:name.value,task:task.value,cron:cron.value,timezone:tz.value,enabled:enabled.checked,catchup:catchup.checked,deliver:{kind:delivery.value,chat_id:Number(chat.value)||0}});await loadManagement();showToast('Schedule saved');}catch(err){showToast(err.message);submit.disabled=false;}});
  root.prepend(form);name.focus();
}
byId('ptab-manage')?.addEventListener('click',loadManagement);
const runForm=byId('run-create-form');
runForm?.addEventListener('submit',async e=>{e.preventDefault();const input=byId('run-create-prompt');const button=byId('run-create-submit');if(!input.value.trim())return;button.disabled=true;try {const run=await startRun(input.value);input.value='';showToast('Run started: '+run.run_id);byId('ptab-ops')?.click();}catch(err){showToast(err.message);}finally{button.disabled=false;}});
