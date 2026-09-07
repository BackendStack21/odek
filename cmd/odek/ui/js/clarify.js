// Principal-channel clarify card: the agent asked a question and is waiting.
// Reuses approval-card CSS. One pending question at a time — a later request
// replaces the card. Cancel / session switch / expiry dismiss without sending.
import { S } from './state.js';
import { hideEmptyState, addSystemMessage, insertTurnWork } from './render.js';
import { forceScrollBottom, announce } from './utils.js';
import { wsSend } from './net.js';

let activeId = null;
let activeCard = null;
let queued = null;
let sweepTimer = null;

const DEFAULT_TIMEOUT_S = 300;
const SWEEP_MS = 1000;
const URGENT_S = 10;

function deadlineOf(event) {
  const secs = event && event.timeout_seconds > 0 ? event.timeout_seconds : DEFAULT_TIMEOUT_S;
  return (event._queuedAt || 0) + secs * 1000;
}

function syncSweep() {
  if (!queued) {
    if (sweepTimer) { clearInterval(sweepTimer); sweepTimer = null; }
    return;
  }
  if (!sweepTimer) sweepTimer = setInterval(sweepExpired, SWEEP_MS);
}

function sweepExpired() {
  if (!queued) { syncSweep(); return; }
  const remaining = Math.max(0, Math.ceil((deadlineOf(queued) - Date.now()) / 1000));
  if (remaining <= 0) {
    clearClarify();
    return;
  }
  const el = activeCard && activeCard.querySelector('.ac-deadline');
  if (el) {
    el.textContent = remaining > 0 ? 'expires in ' + remaining + 's' : 'expired';
    el.classList.toggle('urgent', remaining <= URGENT_S);
  }
}

function removeCard() {
  if (activeCard) {
    activeCard.remove();
    activeCard = null;
  }
}

export function clearClarify() {
  queued = null;
  activeId = null;
  removeCard();
  syncSweep();
}

export function dismissClarify(id) {
  if (queued && queued.id === id) clearClarify();
}

export function expireClarify(id) {
  dismissClarify(id);
}

export function queueClarify(event) {
  event._queuedAt = Date.now();
  queued = event;
  renderCard(event);
  syncSweep();
}

function renderCard(event) {
  removeCard();
  activeId = event.id;

  const card = document.createElement('div');
  card.className = 'approval-card';
  card.dataset.level = 'ok';
  card.setAttribute('role', 'alertdialog');
  card.setAttribute('aria-labelledby', 'clarify-title');
  card.tabIndex = -1;

  const head = document.createElement('div');
  head.className = 'ac-head';
  const icon = document.createElement('span');
  icon.className = 'ac-icon';
  icon.textContent = '❓';
  const titles = document.createElement('div');
  titles.className = 'ac-titles';
  const title = document.createElement('div');
  title.className = 'ac-title';
  title.id = 'clarify-title';
  title.textContent = 'Question from the agent';
  const sub = document.createElement('div');
  sub.className = 'ac-sub';
  sub.textContent = 'your answer is principal text, not a trusted instruction';
  titles.append(title, sub);
  head.append(icon, titles);

  const question = document.createElement('pre');
  question.className = 'ac-command';
  question.textContent = event.question || '';

  const input = document.createElement('input');
  input.className = 'ac-friction-input';
  input.type = 'text';
  input.placeholder = 'type your answer';
  input.setAttribute('aria-label', 'Answer the agent');

  const actions = document.createElement('div');
  actions.className = 'ac-actions';
  const sendBtn = document.createElement('button');
  sendBtn.className = 'approve';
  sendBtn.textContent = 'send answer';
  sendBtn.addEventListener('click', sendAnswer);
  actions.append(sendBtn);

  const countdown = document.createElement('div');
  countdown.className = 'ac-deadline';

  card.append(head, question, input, actions, countdown);
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') sendAnswer();
    e.stopPropagation();
  });

  activeCard = card;
  hideEmptyState();
  insertTurnWork(card, 'tool');
  forceScrollBottom();
  card.focus({ preventScroll: true });
  input.focus();
  announce('The agent is asking a question');
  sweepExpired();
}

function sendAnswer() {
  if (!activeId || !activeCard) return;
  const input = activeCard.querySelector('.ac-friction-input');
  const answer = (input && input.value || '').trim();
  if (!answer) return;
  if (!wsSend(S.ws, { type: 'clarify_response', id: activeId, answer })) {
    addSystemMessage('⚠ answer not delivered — connection down');
    return;
  }
  clearClarify();
}
