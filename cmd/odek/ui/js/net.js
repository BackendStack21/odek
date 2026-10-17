// Network helpers shared by every module. Kept import-free so state.js (and
// everyone else) can use them without an import cycle.

// The token arrives in the meta tag only on the tokenized entry URL. It is
// kept in localStorage (origin-scoped, so unlike a cookie it never reaches
// other ports of the same host) and reused when the page is opened without
// the token in the URL.
const TOKEN_STORAGE_KEY = 'odek_ws_token';

export function getWsToken() {
  const meta = document.querySelector('meta[name="odek-ws-token"]');
  const fromMeta = meta ? meta.getAttribute('content') : '';
  try {
    if (fromMeta) {
      localStorage.setItem(TOKEN_STORAGE_KEY, fromMeta);
      return fromMeta;
    }
    return localStorage.getItem(TOKEN_STORAGE_KEY) || '';
  } catch {
    return fromMeta || '';
  }
}

// forgetStoredWsToken drops the remembered token once the server has rejected
// it (typically after `odek serve` restarted and issued a new one).
export function forgetStoredWsToken() {
  try { localStorage.removeItem(TOKEN_STORAGE_KEY); } catch { /* storage unavailable */ }
}

export const STALE_TOKEN_HINT = 'The saved access token was rejected (the server was probably restarted). Open the new token URL printed by `odek serve` in your terminal.';

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
// and cross-site driven reads. The token is never carried by a cookie.
export function apiHeaders(extra) {
  const token = getWsToken();
  const headers = token ? { 'X-Odek-Ws-Token': token } : {};
  if (extra) Object.assign(headers, extra);
  return headers;
}
