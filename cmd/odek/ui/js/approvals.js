// Approval flow: queued approval requests rendered one at a time as an
// inline decision card pinned at the bottom of the message stream.
import { S } from './state.js';
import { messagesEl } from './dom.js';
import { forceScrollBottom, announce } from './utils.js';
import { hideEmptyState } from './render.js';

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
  S.approvalQueue.push(event);
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
  }
  S.ws.send(JSON.stringify({
    type: 'approval_response',
    id: S.activeApprovalId,
    action: action
  }));
  S.approvalQueue.shift();
  removeActiveApprovalCard();
  showNextApproval();
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
