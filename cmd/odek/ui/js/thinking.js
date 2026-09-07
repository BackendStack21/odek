// Thinking-level helpers shared by the picker, prompt payload, and tests.
export const THINKING_LEVELS = ['disabled', 'low', 'medium', 'high'];

export function normalizeThinking(raw) {
  const s = String(raw ?? '').trim().toLowerCase();
  if (s === '' || THINKING_LEVELS.includes(s)) return s;
  if (s === 'mid') return 'medium';
  if (s === 'enabled' || s === 'on' || s === 'true' || s === '1') return 'medium';
  if (s === 'max') return 'high';
  if (s === 'off' || s === 'false' || s === '0') return 'disabled';
  return '';
}

// seedThinking picks the initial picker value: persisted choice wins,
// then the server config string (or legacy boolean), else disabled.
export function seedThinking(stored, configThinking) {
  const fromStore = normalizeThinking(stored);
  if (fromStore) return fromStore;
  if (configThinking === true) return 'medium';
  if (configThinking === false || configThinking == null) {
    const fromCfg = normalizeThinking(configThinking);
    return fromCfg || 'disabled';
  }
  return normalizeThinking(configThinking) || 'disabled';
}

export function persistThinking(level) {
  const canon = normalizeThinking(level) || 'disabled';
  try {
    localStorage.setItem('odek_thinking', canon);
  } catch { /* ignore quota / private mode */ }
  return canon;
}
