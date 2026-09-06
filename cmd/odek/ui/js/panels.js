// Inspector drawer: four workspaces (sessions / now / memory / ops).
// Opened via the topbar button or ⌘. All data flows through js/api.js.
import { S, getSessionToken } from './state.js';
import { showToast, announce, escapeHtml } from './utils.js';
import {
  getMemory, addMemoryFact, removeMemoryFact, promoteEpisode, consolidateMemory,
  getSkills, getTools, promoteSkill,
  listRuns, cancelRun, answerRunApproval, getEvents,
  listJobs, getJobOutput, stopJob, listSubagents,
  getConfig, getMCPServers, getConnections, kickConnection, getUsage,
} from './api.js';
// Plan panel lives in its own module (session-switch hooks from sessions.js
// need it without dragging the whole drawer along); its lifecycle is wired
// into the tab dispatch here like every other panel.
import { refreshPlanPanel, startPlanPolling, stopPlanPolling } from './plan.js';
import { paintIntent } from './render.js';

const drawer = document.getElementById('panels');
const overlay = document.getElementById('panels-overlay');

// ── Drawer shell ──
export function togglePanels(force) {
  const want = force != null ? force : !drawer.classList.contains('active');
  drawer.classList.toggle('active', want);
  overlay.classList.toggle('active', want);
  drawer.setAttribute('aria-hidden', want ? 'false' : 'true');
  const pbtn = document.getElementById('panels-btn');
  if (pbtn) pbtn.setAttribute('aria-expanded', String(want));
  if (want) {
    refreshActivePanel();
    const tab = drawer.querySelector('.ptab.active');
    if (tab && tab.focus) tab.focus();
  } else {
    stopRunPolling();
    stopPlanPolling();
    stopJobsPolling();
    stopAgentsPolling();
    if (pbtn && pbtn.focus) pbtn.focus();
  }
  const hamburger = document.getElementById('hamburger-btn');
  if (hamburger) hamburger.setAttribute('aria-expanded', String(want && activeWorkspace() === 'sessions'));
}
S.closePanels = () => togglePanels(false);

function activeWorkspace() {
  const tab = drawer.querySelector('.ptab.active');
  return tab ? tab.dataset.tab : '';
}

export function badgeNow() {
  const tab = document.getElementById('ptab-now');
  if (!tab) return;
  const jobs = S.jobs || [];
  const live = !!S.busy || jobs.some((j) => j.status === 'running');
  tab.classList.toggle('live', live);
}

function workspaceOpen(name) {
  if (typeof document.hidden === 'boolean' && document.hidden) return false;
  if (!drawer || !drawer.classList.contains('active')) return false;
  const tab = drawer.querySelector('.ptab.active');
  return !!(tab && tab.dataset.tab === name);
}

function refreshActivePanel() {
  const name = activeWorkspace();
  if (!name) return;
  if (name !== 'now') {
    stopPlanPolling();
    stopJobsPolling();
    stopAgentsPolling();
  }
  if (name !== 'ops') stopRunPolling();
  if (name === 'sessions') {
    if (typeof S.refreshSessions === 'function') S.refreshSessions();
  } else if (name === 'now') {
    refreshPlanPanel();
    startPlanPolling();
    loadJobs();
    loadAgents();
    badgeNow();
  } else if (name === 'memory') {
    loadMemory();
    loadSkills();
    loadTools();
  } else if (name === 'ops') {
    loadRuns();
    loadEvents();
    loadConfig();
  }
}

drawer.querySelectorAll('.ptab').forEach(btn => {
  btn.addEventListener('click', () => {
    drawer.querySelectorAll('.ptab').forEach(b => {
      b.classList.toggle('active', b === btn);
      b.setAttribute('aria-selected', String(b === btn));
      b.tabIndex = b === btn ? 0 : -1;
    });
    drawer.querySelectorAll('.ppanel').forEach(p => {
      const on = p.dataset.panel === btn.dataset.tab;
      p.classList.toggle('active', on);
      p.hidden = !on;
      p.setAttribute('aria-hidden', String(!on));
    });
    refreshActivePanel();
    const hamburger = document.getElementById('hamburger-btn');
    if (hamburger) {
      hamburger.setAttribute('aria-expanded', String(drawer.classList.contains('active') && btn.dataset.tab === 'sessions'));
    }
  });
});
const tabsEl = document.getElementById('panels-tabs');
if (tabsEl) {
  tabsEl.addEventListener('keydown', (e) => {
    const tabs = Array.from(drawer.querySelectorAll('.ptab'));
    const i = tabs.indexOf(document.activeElement);
    if (i < 0) return;
    let next = -1;
    if (e.key === 'ArrowRight') next = (i + 1) % tabs.length;
    else if (e.key === 'ArrowLeft') next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    tabs[next].click();
    if (tabs[next].focus) tabs[next].focus();
  });
}
document.getElementById('panels-close').addEventListener('click', () => togglePanels(false));
overlay.addEventListener('click', () => togglePanels(false));

// ── Memory panel ──
async function loadMemory() {
  const userList = document.getElementById('mf-user-list');
  const envList = document.getElementById('mf-env-list');
  const pendingList = document.getElementById('mf-pending-list');
  try {
    const mem = await getMemory();
    renderFacts(userList, (mem.facts && mem.facts.user) || [], 'user');
    renderFacts(envList, (mem.facts && mem.facts.env) || [], 'env');
    const uc = document.getElementById('mf-user-count');
    const ec = document.getElementById('mf-env-count');
    if (uc) uc.textContent = ((mem.facts && mem.facts.user) || []).length + '/' + (mem.fact_limits ? mem.fact_limits.user : '—');
    if (ec) ec.textContent = ((mem.facts && mem.facts.env) || []).length + '/' + (mem.fact_limits ? mem.fact_limits.env : '—');
    renderPending(pendingList, (mem.episodes && mem.episodes.pending) || []);
  } catch (err) {
    userList.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

function renderFacts(container, facts, target) {
  container.textContent = '';
  if (!facts.length) {
    const el = document.createElement('div');
    el.className = 'mf-empty';
    el.textContent = 'no facts yet';
    container.appendChild(el);
    return;
  }
  facts.forEach(fact => {
    const row = document.createElement('div');
    row.className = 'mf-fact';
    const text = document.createElement('span');
    text.className = 'mf-fact-text';
    text.textContent = fact;
    const del = document.createElement('button');
    del.className = 'mf-fact-del';
    del.title = 'Remove fact';
    del.textContent = '✕';
    del.addEventListener('click', async () => {
      del.disabled = true;
      try {
        await removeMemoryFact(target, fact);
        showToast('fact removed');
        loadMemory();
      } catch (err) {
        del.disabled = false;
        showToast('remove failed: ' + err.message);
      }
    });
    row.append(text, del);
    container.appendChild(row);
  });
}

function renderPending(container, pending) {
  container.textContent = '';
  const count = document.getElementById('mf-pending-count');
  if (count) count.textContent = String(pending.length);
  if (!pending.length) {
    const el = document.createElement('div');
    el.className = 'mf-empty';
    el.textContent = 'no episodes pending review';
    container.appendChild(el);
    return;
  }
  pending.forEach(ep => {
    const row = document.createElement('div');
    row.className = 'mf-episode';
    const body = document.createElement('div');
    body.className = 'mf-episode-body';
    const meta = document.createElement('div');
    meta.className = 'mf-episode-meta';
    meta.textContent = (ep.session_id || '').slice(0, 12) + ' · ' + (ep.turns || 0) + ' turns' +
      (ep.created_at ? ' · ' + new Date(ep.created_at).toLocaleString() : '');
    const summary = document.createElement('div');
    summary.className = 'mf-episode-summary';
    summary.textContent = ep.summary || '';
    body.append(meta, summary);
    if (ep.provenance && ep.provenance.sources && ep.provenance.sources.length) {
      const src = document.createElement('div');
      src.className = 'mf-episode-sources';
      src.textContent = 'sources: ' + ep.provenance.sources.join(', ');
      body.appendChild(src);
    }
    const promote = document.createElement('button');
    promote.className = 'mf-promote';
    promote.textContent = 'promote';
    promote.title = 'Mark this episode as trusted — it becomes recallable in future sessions';
    promote.addEventListener('click', async () => {
      promote.disabled = true;
      try {
        await promoteEpisode(ep.session_id);
        showToast('episode promoted');
        announce('Episode promoted');
        loadMemory();
      } catch (err) {
        promote.disabled = false;
        showToast('promote failed: ' + err.message);
      }
    });
    row.append(body, promote);
    container.appendChild(row);
  });
}

// Fact add rows (user + env)
function wireFactAdd(inputId, btnId, target) {
  const input = document.getElementById(inputId);
  const btn = document.getElementById(btnId);
  const commit = async () => {
    const content = input.value.trim();
    if (!content) return;
    btn.disabled = true;
    try {
      await addMemoryFact(target, content);
      input.value = '';
      showToast('fact added');
      loadMemory();
    } catch (err) {
      showToast('add failed: ' + err.message);
    } finally {
      btn.disabled = false;
    }
  };
  btn.addEventListener('click', commit);
  input.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') { e.preventDefault(); commit(); }
  });
}
wireFactAdd('mf-user-input', 'mf-user-add', 'user');
wireFactAdd('mf-env-input', 'mf-env-add', 'env');

function wireConsolidate(btnId, target) {
  const btn = document.getElementById(btnId);
  if (!btn) return;
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    try {
      await consolidateMemory(target);
      showToast('consolidated ' + target);
      loadMemory();
    } catch (err) {
      showToast('consolidate failed: ' + err.message);
    } finally {
      btn.disabled = false;
    }
  });
}
wireConsolidate('mf-user-consolidate', 'user');
wireConsolidate('mf-env-consolidate', 'env');

// ── Skills panel ──
async function loadSkills() {
  const list = document.getElementById('skills-list');
  try {
    const data = await getSkills();
    const skills = (data && data.skills) || [];
    list.textContent = '';
    if (!skills.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no skills discovered';
      list.appendChild(el);
      return;
    }
    skills.forEach(sk => {
      const row = document.createElement('div');
      row.className = 'sk-row';
      const head = document.createElement('div');
      head.className = 'sk-head';
      const name = document.createElement('span');
      name.className = 'sk-name';
      name.textContent = sk.name;
      head.appendChild(name);
      if (sk.needs_review) {
        const b = document.createElement('span');
        b.className = 'sk-badge warn';
        b.title = 'Pinned NeedsReview — excluded from trigger matching until promoted with `odek skill promote`';
        b.textContent = 'needs review';
        head.appendChild(b);
      }
      if (sk.untrusted) {
        const b = document.createElement('span');
        b.className = 'sk-badge danger';
        b.title = 'Originating session ingested external content';
        b.textContent = 'untrusted';
        head.appendChild(b);
      }
      if (sk.auto_load) {
        const b = document.createElement('span');
        b.className = 'sk-badge ok';
        b.textContent = 'auto';
        head.appendChild(b);
      }
      if (sk.usage_count > 0) {
        const b = document.createElement('span');
        b.className = 'sk-badge';
        b.textContent = '×' + sk.usage_count;
        head.appendChild(b);
      }
      const desc = document.createElement('div');
      desc.className = 'sk-desc';
      desc.textContent = sk.description || '';
      const src = document.createElement('div');
      src.className = 'sk-src';
      src.textContent = sk.source || '';
      row.append(head, desc, src);
      if (sk.needs_review) {
        const actions = document.createElement('div');
        actions.className = 'sk-actions';
        const promo = document.createElement('button');
        promo.className = 'sk-promote';
        promo.textContent = sk.untrusted ? 'force promote' : 'promote';
        promo.addEventListener('click', async () => {
          promo.disabled = true;
          try {
            await promoteSkill(sk.name, !!sk.untrusted);
            showToast('skill promoted');
            loadSkills();
          } catch (err) {
            promo.disabled = false;
            showToast('promote failed: ' + err.message);
          }
        });
        actions.appendChild(promo);
        row.appendChild(actions);
      }
      list.appendChild(row);
    });
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

// ── Tools panel ──
async function loadTools() {
  const list = document.getElementById('tools-list');
  try {
    const data = await getTools();
    const tools = (data && data.tools) || [];
    list.textContent = '';
    const header = document.createElement('div');
    header.className = 'tools-summary';
    header.textContent = tools.filter(t => t.enabled).length + ' of ' + tools.length +
      ' enabled' + (data && data.mcp_servers ? ' · ' + data.mcp_servers + ' MCP server(s)' : '');
    list.appendChild(header);
    tools.forEach(t => {
      const row = document.createElement('div');
      row.className = 'tool-row' + (t.enabled ? '' : ' off');
      const name = document.createElement('span');
      name.className = 'tool-name';
      name.textContent = t.name;
      const state = document.createElement('span');
      state.className = 'tool-state ' + (t.enabled ? 'ok' : 'off');
      state.textContent = t.enabled ? 'on' : 'off';
      row.append(name, state);
      list.appendChild(row);
    });
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}


// ── Runs panel ─────────────────────────────────────────────────────────
// Polls /api/runs while the tab is visible. Pending approvals render with
// approve/deny/trust actions wired to the REST approval bridge.

let runsPollTimer = null;

function stopRunPolling() {
  if (runsPollTimer) clearInterval(runsPollTimer);
  runsPollTimer = null;
}

async function loadRuns() {
  stopRunPolling();
  await refreshRuns();
  runsPollTimer = setInterval(refreshRuns, 3000);
}

const RUN_STATUS_CLASS = {
  running: 'run-st-run',
  waiting_approval: 'run-st-wait',
  completed: 'run-st-done',
  failed: 'run-st-fail',
  cancelled: 'run-st-cancel',
};

async function refreshRuns() {
  const list = document.getElementById('runs-list');
  if (!workspaceOpen('ops')) {
    stopRunPolling();
    return;
  }
  try {
    const data = await listRuns(30);
    const runs = (data && data.runs) || [];
    list.textContent = '';
    const header = document.createElement('div');
    header.className = 'tools-summary';
    header.textContent = runs.length + ' run(s) · ' + ((data && data.active) || 0) + ' active';
    list.appendChild(header);
    if (!runs.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no runs yet — POST /api/prompt or prompt from the chat';
      list.appendChild(el);
      return;
    }
    runs.forEach(run => list.appendChild(renderRunRow(run)));
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

function renderRunRow(run) {
  const row = document.createElement('div');
  row.className = 'run-row';

  const head = document.createElement('div');
  head.className = 'run-head';
  const status = document.createElement('span');
  status.className = 'run-status ' + (RUN_STATUS_CLASS[run.status] || '');
  status.textContent = run.status || '?';
  const idEl = document.createElement('span');
  idEl.className = 'run-id';
  idEl.textContent = (run.id || '').slice(0, 14);
  const when = document.createElement('span');
  when.className = 'run-when';
  when.textContent = run.started_at ? new Date(run.started_at).toLocaleTimeString() : '';
  const metaBits = [];
  if (run.input_tokens > 0) metaBits.push('⇥ ' + run.input_tokens);
  if (run.output_tokens > 0) metaBits.push('↦ ' + run.output_tokens);
  const meta = document.createElement('span');
  meta.className = 'run-meta';
  meta.textContent = metaBits.join(' · ');
  head.append(status, idEl, meta, when);

  const body = document.createElement('div');
  body.className = 'run-body';
  const sess = document.createElement('div');
  sess.className = 'run-session';
  sess.textContent = run.session_id ? 'session ' + run.session_id.slice(0, 8) : 'new session';
  body.appendChild(sess);

  if (run.result) {
    const res = document.createElement('div');
    res.className = 'run-result';
    res.textContent = run.result.length > 300 ? run.result.slice(0, 300) + '…' : run.result;
    body.appendChild(res);
  }
  if (run.error) {
    const errEl = document.createElement('div');
    errEl.className = 'run-error';
    errEl.textContent = run.error;
    body.appendChild(errEl);
  }

  row.append(head, body);

  // Pending approvals — inline approve/deny/trust.
  const pending = run.pending_approvals || [];
  pending.forEach(ap => {
    const card = document.createElement('div');
    card.className = 'run-approval';
    const cmd = document.createElement('pre');
    cmd.className = 'run-approval-cmd';
    cmd.textContent = ap.risk + ': ' + (ap.command || '');
    const actions = document.createElement('div');
    actions.className = 'run-approval-actions';
    const mk = (label, action, cls) => {
      const b = document.createElement('button');
      b.className = 'run-approval-btn ' + cls;
      b.textContent = label;
      if (action === 'trust' && ap.allow_trust === false) b.style.display = 'none';
      b.addEventListener('click', async () => {
        b.disabled = true;
        try {
          await answerRunApproval(run.id, ap.id, action);
          showToast('approval ' + action + 'd');
          refreshRuns();
        } catch (e) {
          b.disabled = false;
          showToast('approval failed: ' + e.message);
        }
      });
      return b;
    };
    actions.append(mk('deny', 'deny', 'deny'), mk('trust', 'trust', 'trust'), mk('approve', 'approve', 'approve'));
    card.append(cmd, actions);
    row.appendChild(card);
  });

  // Cancel for live runs.
  if (run.status === 'running' || run.status === 'waiting_approval') {
    const cancel = document.createElement('button');
    cancel.className = 'run-cancel';
    cancel.textContent = 'cancel';
    cancel.addEventListener('click', async () => {
      cancel.disabled = true;
      try {
        await cancelRun(run.id);
        refreshRuns();
      } catch (e) {
        cancel.disabled = false;
        showToast('cancel failed: ' + e.message);
      }
    });
    row.appendChild(cancel);
  }
  return row;
}

// ── Events panel ───────────────────────────────────────────────────────

async function loadEvents() {
  const list = document.getElementById('events-list');
  try {
    const data = await getEvents({ limit: 100 });
    const evs = (data && data.events) || [];
    list.textContent = '';
    const header = document.createElement('div');
    header.className = 'tools-summary';
    header.textContent = evs.length + ' recent event(s) — newest last';
    list.appendChild(header);
    if (!evs.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no runtime events yet — run a prompt';
      list.appendChild(el);
      return;
    }
    evs.forEach(ev => {
      const row = document.createElement('div');
      row.className = 'event-row';
      const t = document.createElement('span');
      t.className = 'event-type';
      t.textContent = ev.type || '?';
      const when = document.createElement('span');
      when.className = 'event-when';
      when.textContent = ev.timestamp ? new Date(ev.timestamp).toLocaleTimeString() : '';
      const ctx = document.createElement('span');
      ctx.className = 'event-ctx';
      const bits = [];
      if (ev.iteration) bits.push('iter ' + ev.iteration);
      if (ev.tool) bits.push(ev.tool);
      if (ev.run_id) bits.push((ev.run_id || '').slice(0, 12));
      ctx.textContent = bits.join(' · ');
      row.append(t, ctx, when);
      list.appendChild(row);
    });
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

// ── Jobs / agents / config ────────────────────────────────────────────

let jobsPollTimer = null;
let agentsPollTimer = null;

function stopJobsPolling() {
  if (jobsPollTimer) clearInterval(jobsPollTimer);
  jobsPollTimer = null;
}
function stopAgentsPolling() {
  if (agentsPollTimer) clearInterval(agentsPollTimer);
  agentsPollTimer = null;
}

async function loadJobs() {
  stopJobsPolling();
  await refreshJobs();
  jobsPollTimer = setInterval(refreshJobs, 3000);
}

async function refreshJobs() {
  const list = document.getElementById('jobs-list');
  if (!list) return;
  if (!workspaceOpen('now')) { stopJobsPolling(); return; }
  if (!S.sessionId) {
    S.jobs = [];
    paintIntent();
    list.textContent = '';
    const el = document.createElement('div');
    el.className = 'mf-empty';
    el.textContent = 'no session — jobs are session-scoped';
    list.appendChild(el);
    return;
  }
  try {
    const data = await listJobs(getSessionToken(S.sessionId) || undefined);
    const jobs = (data && data.jobs) || [];
    S.jobs = jobs;
    paintIntent();
    list.textContent = '';
    const header = document.createElement('div');
    header.className = 'tools-summary';
    header.textContent = jobs.length + ' job(s)';
    list.appendChild(header);
    if (!jobs.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no background jobs in this session';
      list.appendChild(el);
      badgeNow();
      return;
    }
    jobs.forEach((j) => list.appendChild(renderJobRow(j)));
    badgeNow();
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

function renderJobRow(job) {
  const row = document.createElement('div');
  row.className = 'job-row';
  const head = document.createElement('div');
  head.className = 'job-head';
  const st = document.createElement('span');
  st.className = 'job-status job-st-' + (job.status || 'unknown');
  st.textContent = job.status || '?';
  const cmd = document.createElement('span');
  cmd.className = 'job-cmd';
  cmd.textContent = job.command || job.id;
  head.append(st, cmd);
  row.appendChild(head);
  const meta = document.createElement('div');
  meta.className = 'job-meta';
  meta.textContent = [
    job.id,
    job.runtime_s != null ? Number(job.runtime_s).toFixed(1) + 's' : '',
    job.exit_code != null ? 'exit ' + job.exit_code : '',
  ].filter(Boolean).join(' · ');
  row.appendChild(meta);
  const actions = document.createElement('div');
  actions.className = 'job-actions';
  const out = document.createElement('button');
  out.className = 'job-out';
  out.textContent = 'output';
  out.addEventListener('click', () => showJobOutput(job.id, row));
  actions.appendChild(out);
  if (job.status === 'running') {
    const stop = document.createElement('button');
    stop.className = 'job-stop';
    stop.textContent = 'stop';
    stop.addEventListener('click', async () => {
      if (!confirm('Stop this background job?')) return;
      stop.disabled = true;
      try {
        await stopJob(job.id, getSessionToken(S.sessionId) || undefined);
        refreshJobs();
      } catch (err) {
        stop.disabled = false;
        showToast('stop failed: ' + err.message);
      }
    });
    actions.appendChild(stop);
  }
  row.appendChild(actions);
  return row;
}

async function showJobOutput(id, row) {
  let pre = row.querySelector('.job-output');
  if (pre) { pre.remove(); return; }
  pre = document.createElement('pre');
  pre.className = 'job-output';
  pre.textContent = 'loading…';
  row.appendChild(pre);
  try {
    const data = await getJobOutput(id, getSessionToken(S.sessionId) || undefined);
    pre.textContent = (data && data.output) || '(empty)';
  } catch (err) {
    pre.textContent = err.message;
  }
}

async function loadAgents() {
  stopAgentsPolling();
  await refreshAgents();
  agentsPollTimer = setInterval(refreshAgents, 3000);
}

async function refreshAgents() {
  const list = document.getElementById('agents-list');
  if (!list) return;
  if (!workspaceOpen('now')) { stopAgentsPolling(); return; }
  try {
    const data = await listSubagents();
    const entries = (data && data.entries) || [];
    list.textContent = '';
    const header = document.createElement('div');
    header.className = 'tools-summary';
    header.textContent = entries.length + ' sub-agent(s)';
    list.appendChild(header);
    if (!entries.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no recent sub-agents';
      list.appendChild(el);
      return;
    }
    entries.forEach((a) => {
      const row = document.createElement('div');
      row.className = 'agent-row';
      const phase = document.createElement('span');
      phase.className = 'agent-phase agent-ph-' + (a.phase || 'unknown');
      phase.textContent = a.phase || '?';
      const goal = document.createElement('div');
      goal.className = 'agent-goal';
      goal.textContent = a.goal || a.task_id || '';
      const meta = document.createElement('div');
      meta.className = 'agent-meta';
      meta.textContent = [
        a.status,
        a.step,
        a.iterations != null ? 'it ' + a.iterations : '',
        a.tokens_used != null ? formatTok(a.tokens_used) : '',
        a.last_tool,
      ].filter(Boolean).join(' · ');
      row.append(phase, goal, meta);
      if (a.phase === 'started' || a.phase === 'active') {
        const stop = document.createElement('button');
        stop.className = 'agent-stop';
        stop.textContent = 'stop';
        stop.addEventListener('click', () => {
          if (!confirm('Stop this sub-agent?')) return;
          if (S.onSubagentStop && a.task_id) S.onSubagentStop(a.task_id);
        });
        row.appendChild(stop);
      }
      list.appendChild(row);
    });
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}

function formatTok(n) {
  n = Number(n) || 0;
  return n >= 1000 ? (n / 1000).toFixed(1) + 'k tok' : n + ' tok';
}

async function loadConfig() {
  const list = document.getElementById('config-list');
  if (!list) return;
  try {
    const [cfg, mcp, conns, usage] = await Promise.all([
      getConfig(),
      getMCPServers().catch(() => null),
      getConnections().catch(() => null),
      getUsage().catch(() => null),
    ]);
    list.textContent = '';
    const dump = document.createElement('pre');
    dump.className = 'cfg-dump';
    dump.textContent = JSON.stringify(cfg, null, 2);
    list.appendChild(dump);

    if (mcp) {
      const servers = mcp.servers || (Array.isArray(mcp) ? mcp : []);
      if (servers.length) {
        const h = document.createElement('div');
        h.className = 'tools-summary';
        h.textContent = 'MCP servers';
        list.appendChild(h);
        servers.forEach((s) => {
          const row = document.createElement('div');
          row.className = 'mcp-row';
          row.textContent = (s.name || s.command || 'mcp') + (s.project ? ' · project' : '');
          list.appendChild(row);
        });
      }
    }

    if (usage) {
      const h = document.createElement('div');
      h.className = 'tools-summary';
      h.textContent = 'lifetime usage';
      list.appendChild(h);
      const u = document.createElement('div');
      u.className = 'cfg-usage';
      u.textContent = (usage.prompts_completed || 0) + '/' + (usage.prompts_started || 0) +
        ' prompts · ' + (usage.tokens_in || 0) + ' in · ' + (usage.tokens_out || 0) + ' out';
      list.appendChild(u);
    }

    const ch = document.createElement('div');
    ch.className = 'tools-summary';
    ch.textContent = 'connections';
    list.appendChild(ch);
    const connections = (conns && conns.connections) || [];
    if (!connections.length) {
      const el = document.createElement('div');
      el.className = 'mf-empty';
      el.textContent = 'no live connections';
      list.appendChild(el);
    }
    connections.forEach((c) => {
      const row = document.createElement('div');
      row.className = 'conn-row';
      const info = document.createElement('span');
      info.className = 'conn-info';
      info.textContent = (c.id || '') + ' · ' + (c.session_id || '—') + (c.busy ? ' · busy' : '');
      const kick = document.createElement('button');
      kick.className = 'conn-kick';
      kick.textContent = 'kick';
      kick.addEventListener('click', async () => {
        if (kick.dataset.armed !== '1') {
          list.querySelectorAll('.conn-kick').forEach((other) => {
            if (other === kick) return;
            other.dataset.armed = '';
            other.classList.remove('armed');
            other.textContent = 'kick';
          });
          kick.dataset.armed = '1';
          kick.classList.add('armed');
          kick.textContent = 'confirm kick';
          setTimeout(() => {
            if (kick.dataset.armed === '1') {
              kick.dataset.armed = '';
              kick.classList.remove('armed');
              kick.textContent = 'kick';
            }
          }, 4000);
          return;
        }
        kick.dataset.armed = '';
        kick.classList.remove('armed');
        try {
          await kickConnection(c.id);
          showToast('kicked');
          loadConfig();
        } catch (err) {
          showToast('kick failed: ' + err.message);
        }
      });
      row.append(info, kick);
      list.appendChild(row);
    });

    const shut = document.createElement('button');
    shut.className = 'cfg-shutdown';
    shut.textContent = 'shut down serve';
    shut.addEventListener('click', () => {
      const o = document.getElementById('shutdown-overlay');
      if (o) o.classList.add('active');
      const inp = document.getElementById('shutdown-input');
      const btn = document.getElementById('shutdown-confirm');
      if (inp) { inp.value = ''; inp.focus(); }
      if (btn) btn.disabled = true;
    });
    list.appendChild(shut);
  } catch (err) {
    list.innerHTML = '<div class="mf-empty">failed to load: ' + escapeHtml(err.message) + '</div>';
  }
}
