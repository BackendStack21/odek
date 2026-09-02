// Approval flow: queued approval requests rendered one at a time as an
// inline decision card pinned at the bottom of the message stream.
import { S } from './state.js';
import { messagesEl } from './dom.js';
import { forceScrollBottom, announce } from './utils.js';
import { hideEmptyState, addSystemMessage } from './render.js';
import { wsSend } from './net.js';

// Approval requests are queued and rendered one at a time as an inline
// decision card pinned at the bottom of the message stream — the user can
// scroll back through the transcript while deciding. Cards are fully
// keyboard-operable (a = approve, d = deny, t = trust session) and announce
// themselves to assistive technology via role="alertdialog" + focus move.
// An approval_ack from the server (answer given on another client) dismisses
// the matching request.

// Risk-class presentation metadata. The server sends the class string
// (system_write, destructive, …); level drives the badge color and the
// "why" line explains the class in plain language.
export const APPROVAL_RISK_META = {
  local_write:    { icon: '🟡', level: 'warn',   why: 'Writes or deletes files in the working directory.' },
  system_write:   { icon: '⚠️', level: 'warn',   why: 'Modifies system files or settings outside the workspace.' },
  destructive:    { icon: '🚫', level: 'danger', why: 'Irreversibly destroys data. This cannot be undone.' },
  network_egress: { icon: '🌐', level: 'warn',   why: 'Sends data out to the network.' },
  code_execution: { icon: '⚠️', level: 'warn',   why: 'Executes arbitrary code.' },
  install:        { icon: '📦', level: 'warn',   why: 'Installs packages or dependencies.' },
  unknown:        { icon: '🚫', level: 'danger', why: 'Unrecognized command — the gate fails closed on these.' },
  blocked:        { icon: '🚫', level: 'danger', why: 'Hard-blocked, unrecoverable operation.' },
  safe:           { icon: '✅', level: 'ok',     why: 'Read-only operation.' },
};

export function approvalRiskMeta(risk) {
  return APPROVAL_RISK_META[risk] || { icon: '🛡️', level: 'warn', why: 'Requires your explicit approval.' };
}

export function queueApproval(event) {
  // F-A1: stamp the arrival time so the local expiry deadline mirrors the
  // server's per-approval wait (frame timeout_seconds, 60s default).
  event._queuedAt = Date.now();
  S.approvalQueue.push(event);
  syncSweep();
  if (!S.activeApprovalId) {
    showNextApproval();
    return;
  }
  // A card is already shown — it was rendered before this request arrived,
  // so its "1 of N" hint (if any) is stale. Refresh it in place instead of
  // re-rendering, which would wipe any friction-mode typing in progress.
  updateQueuePosition(S.activeApprovalCard);
}

// updateQueuePosition adds, refreshes, or removes the ".ac-queue-pos" hint
// on card so it always reflects the current queue depth.
function updateQueuePosition(card) {
  if (!card) return;
  let pos = card.querySelector('.ac-queue-pos');
  if (S.approvalQueue.length <= 1) {
    if (pos) pos.remove();
    return;
  }
  const text = 'request 1 of ' + S.approvalQueue.length + ' — more waiting';
  if (!pos) {
    pos = document.createElement('div');
    pos.className = 'ac-queue-pos';
    card.appendChild(pos); // after the actions row, matching render order
  }
  pos.textContent = text;
}

// dismissApproval removes a request from the queue (and its card if shown)
// without sending a response — used when the server acks an answer that
// came from another client.
export function dismissApproval(id) {
  const idx = S.approvalQueue.findIndex(e => e.id === id);
  if (idx >= 0) S.approvalQueue.splice(idx, 1);
  if (S.activeApprovalId === id) {
    removeActiveApprovalCard();
    showNextApproval();
  } else if (idx >= 0) {
    // A queued (not shown) request was answered on another client — the
    // active card's position hint just went stale.
    updateQueuePosition(S.activeApprovalCard);
  }
  syncSweep();
}

// expireApproval handles the server's approval_expired frame (F-A1): the
// wait lapsed without an answer, so the matching request is dead. Idempotent
// by construction — ids already answered, swept, or unknown are no-ops, so
// a late frame (or a late approval_ack for the same id) can never resurrect
// a closed card.
export function expireApproval(id) {
  dismissApproval(id);
}

// ── Expiry sweep (F-A1) ──
// The server enforces a per-approval wait and emits approval_expired when
// it lapses, but a frame can be lost across a socket blip — so the client
// mirrors the deadline locally. A 1s sweep autocloses ONLY expired cards
// (live countdown + urgent class in the last 10s) and stops itself whenever
// the queue is empty, so no interval ever outlives the approvals.
const DEFAULT_TIMEOUT_S = 60;
const SWEEP_MS = 1000;
const URGENT_S = 10;
let sweepTimer = null;

function deadlineOf(event) {
  const secs = event && event.timeout_seconds > 0 ? event.timeout_seconds : DEFAULT_TIMEOUT_S;
  return (event._queuedAt || 0) + secs * 1000;
}

function syncSweep() {
  if (S.approvalQueue.length === 0) {
    if (sweepTimer) { clearInterval(sweepTimer); sweepTimer = null; }
  } else if (!sweepTimer) {
    sweepTimer = setInterval(sweepExpired, SWEEP_MS);
  }
}

function renderCountdown(card, remainingS) {
  if (!card) return;
  const el = card.querySelector('.ac-deadline');
  if (!el) return;
  el.textContent = remainingS > 0 ? 'expires in ' + remainingS + 's' : 'expired';
  el.classList.toggle('urgent', remainingS <= URGENT_S);
}

function sweepExpired() {
  const now = Date.now();
  // Expired queued-but-hidden requests: their approval_expired frame may
  // still arrive later — expireApproval is a no-op for unknown ids.
  for (let i = S.approvalQueue.length - 1; i >= 1; i--) {
    if (now >= deadlineOf(S.approvalQueue[i])) S.approvalQueue.splice(i, 1);
  }
  const active = S.approvalQueue[0];
  if (active && now >= deadlineOf(active)) {
    S.approvalQueue.shift();
    removeActiveApprovalCard();
    showNextApproval();
    updateQueuePosition(S.activeApprovalCard);
  } else if (active) {
    renderCountdown(S.activeApprovalCard, Math.max(0, Math.ceil((deadlineOf(active) - now) / 1000)));
  }
  syncSweep();
}

function showNextApproval() {
  const event = S.approvalQueue[0];
  if (!event) {
    S.activeApprovalId = null;
    S.activeApprovalCard = null;
    return;
  }
  S.activeApprovalId = event.id;
  renderApprovalCard(event);
}

// removeActiveApprovalCard removes the rendered card only. It must NOT touch
// S.activeApprovalId: renderApprovalCard calls this first, and clearing the
// id here left every freshly rendered card with a null id — sendApproval's
// guard then silently dropped every button press (the "approval buttons do
// nothing" regression). The id is owned exclusively by showNextApproval,
// which sets it to the shown request or null when the queue is empty.
export function removeActiveApprovalCard() {
  if (S.activeApprovalCard) {
    S.activeApprovalCard.remove();
    S.activeApprovalCard = null;
  }
}

// clearApprovals drops every pending request and the rendered card — used on
// session switch / new session, where pending approvals belong to the
// previous run. This is the only teardown that resets all three pieces of
// approval state together (queue + card + active id).
export function clearApprovals() {
  S.approvalQueue.length = 0;
  removeActiveApprovalCard();
  S.activeApprovalId = null;
  syncSweep();
}

function renderApprovalCard(event) {
  removeActiveApprovalCard();
  const meta = approvalRiskMeta(event.risk);

  const card = document.createElement('div');
  card.className = 'approval-card';
  card.dataset.level = meta.level;
  card.setAttribute('role', 'alertdialog');
  card.setAttribute('aria-labelledby', 'ac-title');
  card.setAttribute('aria-describedby', 'ac-why');
  card.tabIndex = -1;

  // Header: icon, titles, risk badge
  const head = document.createElement('div');
  head.className = 'ac-head';
  const icon = document.createElement('span');
  icon.className = 'ac-icon';
  icon.textContent = meta.icon;
  const titles = document.createElement('div');
  titles.className = 'ac-titles';
  const title = document.createElement('div');
  title.className = 'ac-title';
  title.id = 'ac-title';
  title.textContent = 'Approval required';
  const sub = document.createElement('div');
  sub.className = 'ac-sub';
  sub.textContent = 'the agent wants to run this operation';
  titles.append(title, sub);
  const risk = document.createElement('span');
  risk.className = 'ac-risk';
  risk.dataset.level = meta.level;
  risk.textContent = event.risk || 'unknown';
  head.append(icon, titles, risk);

  // Plain-language explanation of the risk class
  const why = document.createElement('div');
  why.className = 'ac-why';
  why.id = 'ac-why';
  why.textContent = meta.why;

  // The command / operation, verbatim
  const command = document.createElement('pre');
  command.className = 'ac-command';
  command.textContent = event.command;

  card.append(head, why, command);

  if (event.description) {
    const desc = document.createElement('div');
    desc.className = 'ac-desc';
    desc.textContent = event.description;
    card.appendChild(desc);
  }

  // Friction mode: after repeated recent approvals of this class, require
  // typing the literal word 'approve' (after a 1.5s gate).
  let frictionInput = null;
  if (event.friction) {
    const fr = document.createElement('div');
    fr.className = 'ac-friction';
    const msg = document.createElement('div');
    msg.className = 'ac-friction-msg';
    msg.textContent = '⚠️ You approved ' + (event.friction_approvals || 0) + ' ' + (event.risk || '') +
      ' operations in the last minute. Type the word “approve” to proceed.';
    frictionInput = document.createElement('input');
    frictionInput.className = 'ac-friction-input';
    frictionInput.type = 'text';
    frictionInput.placeholder = 'type: approve';
    frictionInput.setAttribute('aria-label', 'Type approve to confirm');
    fr.append(msg, frictionInput);
    card.appendChild(fr);
  }

  // Actions
  const actions = document.createElement('div');
  actions.className = 'ac-actions';
  const denyBtn = document.createElement('button');
  denyBtn.className = 'deny';
  denyBtn.innerHTML = 'deny <kbd>d</kbd>';
  denyBtn.title = 'Deny [d]';
  denyBtn.addEventListener('click', () => sendApproval('deny'));
  const trustBtn = document.createElement('button');
  trustBtn.className = 'trust';
  trustBtn.innerHTML = 'trust session <kbd>t</kbd>';
  trustBtn.title = 'Trust this risk class for the rest of the session [t]';
  trustBtn.addEventListener('click', () => sendApproval('trust'));
  const approveBtn = document.createElement('button');
  approveBtn.className = 'approve';
  approveBtn.innerHTML = 'approve <kbd>a</kbd>';
  approveBtn.title = 'Approve [a]';
  approveBtn.addEventListener('click', () => sendApproval('approve'));
  actions.append(denyBtn, trustBtn, approveBtn);
  card.appendChild(actions);

  // Live expiry countdown (F-A1): stamped from the frame's timeout_seconds
  // (60s default for pre-timeout servers) and kept fresh by the sweep.
  const countdown = document.createElement('div');
  countdown.className = 'ac-deadline';
  card.appendChild(countdown);
  renderCountdown(card, Math.max(0, Math.ceil((deadlineOf(event) - Date.now()) / 1000)));

  // Trust shortcut is suppressed for destructive / blocked / unknown.
  if (event.allow_trust === false) trustBtn.style.display = 'none';

  // Queue position indicator (kept live by updateQueuePosition when later
  // requests arrive or queued ones are answered elsewhere).
  updateQueuePosition(card);

  // Friction gating: approve stays disabled until the word is typed and
  // 1.5s have elapsed.
  if (event.friction && frictionInput) {
    approveBtn.disabled = true;
    setTimeout(() => {
      frictionInput.addEventListener('input', () => {
        approveBtn.disabled = frictionInput.value.trim().toLowerCase() !== 'approve';
      });
    }, 1500);
    frictionInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !approveBtn.disabled) sendApproval('approve');
      e.stopPropagation();
    });
  }

  S.activeApprovalCard = card;
  // F-B4: stamp arrival so every approve path (button, Enter, global 'a')
  // enforces the same 1.5s friction cool-down.
  card.dataset.shownAt = String(Date.now());
  hideEmptyState();
  messagesEl.appendChild(card);
  forceScrollBottom();
  card.focus({ preventScroll: true });
  announce('Approval required: ' + (event.risk || 'unknown') + ' risk operation');
}

export function sendApproval(action) {
  if (!S.activeApprovalId) return;
  const event = S.approvalQueue[0];
  // Honor the friction gate for keyboard-triggered approvals too.
  if (event && event.friction && action === 'approve') {
    const input = S.activeApprovalCard && S.activeApprovalCard.querySelector('.ac-friction-input');
    if (input && input.value.trim().toLowerCase() !== 'approve') return;
    // F-B4: the global 'a' shortcut used to bypass the 1.5s cool-down the
    // button/input paths enforce — every approve path waits it out now.
    const shownAt = Number(S.activeApprovalCard && S.activeApprovalCard.dataset.shownAt) || 0;
    if (Date.now() - shownAt < 1500) return;
  }
  // F-B3: route through the guarded send. A dead socket must not destroy
  // the decision — keep the card up (no success announcement) and tell the
  // user, instead of removing the card for an answer the server will never
  // see.
  if (!wsSend(S.ws, {
    type: 'approval_response',
    id: S.activeApprovalId,
    action: action
  })) {
    addSystemMessage('⚠ approval not delivered — connection down');
    return;
  }
  S.approvalQueue.shift();
  removeActiveApprovalCard();
  showNextApproval();
  syncSweep();
  announce(action === 'trust' ? 'Risk class trusted for this session' :
           action === 'approve' ? 'Approved' : 'Denied');
}

// Keyboard operation while an approval card is active. Ignored when the
// user is typing in an input/textarea (the friction input handles its own
// Enter) or when modifier keys are held.
document.addEventListener('keydown', (e) => {
  if (!S.activeApprovalId) return;
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const tag = (e.target && e.target.tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  const trustVisible = S.activeApprovalCard &&
    !S.activeApprovalCard.querySelector('.trust').style.display;
  if (e.key === 'a' || e.key === 'A') {
    e.preventDefault();
    sendApproval('approve');
  } else if (e.key === 'd' || e.key === 'D') {
    e.preventDefault();
    sendApproval('deny');
  } else if ((e.key === 't' || e.key === 'T') && trustVisible) {
    e.preventDefault();
    sendApproval('trust');
  }
});
