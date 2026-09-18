// Extract capability tokens for the session transcripts explicitly referenced
// by a prompt. Tokens are looked up per referenced id; the active session token
// must never be substituted for a foreign transcript.
export function extractReferenceTokens(text, getToken) {
  const out = {};
  const seen = new Set();
  for (const match of String(text || '').matchAll(/@sess:([^\s\)\]\},;]+)/g)) {
    const id = match[1];
    if (seen.has(id)) continue;
    seen.add(id);
    const token = getToken(id);
    if (token) out[id] = token;
  }
  return out;
}
