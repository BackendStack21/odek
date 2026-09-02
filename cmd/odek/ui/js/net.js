// Network helpers shared by every module. Kept import-free so state.js (and
// everyone else) can use them without an import cycle.

export function getWsToken() {
  const meta = document.querySelector('meta[name="odek-ws-token"]');
  return meta ? meta.getAttribute('content') : '';
}

// wsSend sends a JSON payload over `socket` only when it is actually OPEN
// (readyState 1 — the WebSocket.OPEN constant, kept literal so import-free
// test shims work). Returns true when handed to the transport, false when
// the socket is unusable — callers MUST treat false as not-delivered and
// preserve pending state (F-B3: never fake success on a dead socket).
export function wsSend(socket, payload) {
  if (!socket || socket.readyState !== 1) return false;
  socket.send(typeof payload === 'string' ? payload : JSON.stringify(payload));
  return true;
}

// Build API request headers that include the per-instance CSRF token. The
// server requires this token on every /api/* endpoint to block DNS-rebinding
// and cross-site driven reads. Browser same-origin requests also send the
// cookie, but the header defends-in-depth and works when cookies are blocked.
export function apiHeaders(extra) {
  const token = getWsToken();
  const headers = token ? { 'X-Odek-Ws-Token': token } : {};
  if (extra) Object.assign(headers, extra);
  return headers;
}
