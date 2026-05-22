# Changelog

## v0.33.0 (2026-05-23) — Performance Release

Six performance improvements across the stack, reducing latency per session by **~30-50%**.

### Connection Pooling
- **LLM client** now reuses TCP/TLS connections across API calls (was: new handshake per request)
- **Telegram bot** uses the same pooled transport for polling and API requests
- Saves ~200-500ms per HTTP call — ~6-15s on a typical 30-call agent session

### Context Trimmer O(n²) → O(n)
- `trimContext` now tracks a running token total instead of re-scanning all messages after every group drop
- For large conversations near the context limit: 1,770 message scans → ~60

### Session Compact JSON
- Session files are now written with `json.Marshal` (compact) instead of `json.MarshalIndent` (pretty-printed)
- ~5% smaller on disk, faster serialization — 410KB → 420KB for a Telegram session

### Memory: LLM Search Disabled by Default
- Episode search now uses RandomProjections (go-vector) by default instead of LLM ranking
- Zero LLM API calls per turn for memory search (was: 1 call per loop iteration)
- Set `llm_search: true` in config to restore LLM-based ranking

### Persistent Skill Cache
- Parsed skills are cached to `~/.odek/skills/.skills_cache.json` across `odek run` invocations
- ~30ms saved per cold start — 152 `stat()` + YAML parses → single cache read + unmarshal
- Auto-invalidated on skill mutations or format version changes

### Episode Index Cache
- Episode index (`index.json`) is cached in memory and invalidated after writes
- Avoids disk I/O + JSON unmarshal on every `FormatEpisodeContext` call
- Saves ~5ms per loop iteration across a session

---

## v0.32.x

See git log for earlier releases.
