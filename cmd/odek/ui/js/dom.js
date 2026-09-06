// DOM element references. Captured once at module load; the elements are all
// static index.html markup, so the refs stay valid for the page lifetime.
// This module must stay import-free (leaf of the import graph).

export const messagesEl = document.getElementById('messages');
export const promptEl = document.getElementById('prompt');
export const sendBtn = document.getElementById('send-btn');
export const completionEl = document.getElementById('completion');
export const statusEl = document.getElementById('ws-status');
export const dotEl = document.getElementById('ws-dot');
export const modelLabel = document.getElementById('model-label');
export const sessionListEl = document.getElementById('session-list');
export const sidebarSearch = document.getElementById('sidebar-search');
export const emptyState = document.getElementById('empty-state');
export const cancelBtn = document.getElementById('cancel-btn');
export const scrollBottomBtn = document.getElementById('scroll-bottom-btn');
export const skeletonEl = document.getElementById('loading-skeleton');
export const sidebarOverlay = document.getElementById('sidebar-overlay');
export const fileInput = document.getElementById('file-input');
export const attachBtn = document.getElementById('attach-btn');
export const fileChips = document.getElementById('file-chips');
