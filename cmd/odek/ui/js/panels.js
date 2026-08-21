// Management panels drawer: tabbed shell on the right side with three
// panels — memory (facts + pending episodes), skills, and tools. Opened via
// the topbar button or Alt+M. All data flows through js/api.js.
import { showToast, announce, escapeHtml } from './utils.js';
import {
  getMemory, addMemoryFact, removeMemoryFact, promoteEpisode,
  getSkills, getTools,
  listRuns, cancelRun, answerRunApproval, getEvents,
} from './api.js';

const drawer = document.getElementById('panels');
const overlay = document.getElementById('panels-overlay');

// ── Drawer shell ──
export function togglePanels(force) {
  const want = force != null ? force : !drawer.classList.contains('active');
  drawer.classList.toggle('active', want);
  overlay.classList.toggle('active', want);
  if (want) refreshActivePanel();
  else stopRunPolling();
}

function refreshActivePanel() {
  const tab = drawer.querySelector('.ptab.active');
  if (!tab) return;
  const name = tab.dataset.tab;
  if (name === 'memory') loadMemory();
  else if (name === 'skills') loadSkills();
  else if (name === 'tools') loadTools();
  else if (name === 'runs') loadRuns();
  else if (name === 'events') loadEvents();
}

drawer.querySelectorAll('.ptab').forEach(btn => {
  btn.addEventListener('click', () => {
    drawer.querySelectorAll('.ptab').forEach(b => {
      b.classList.toggle('active', b === btn);
      b.setAttribute('aria-selected', String(b === btn));
    });
    drawer.querySelectorAll('.ppanel').forEach(p => {
      p.classList.toggle('active', p.dataset.panel === btn.dataset.tab);
    });
    refreshActivePanel();
  });
});
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
  // Only poll while the runs tab is actually shown.
  const tab = drawer.querySelector('.ptab.active');
  if (!tab || tab.dataset.tab !== 'runs') {
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
