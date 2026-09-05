# Memory System

odek has a **three-tier file-based memory** system. Minimal external dependencies from the 21no.de ecosystem (go-vector, go-mcp), all minimal-dependency Go packages.

## Three Tiers

```
~/.odek/memory/
├── facts/
│   ├── user.md          ← Global user profile (4,000 chars)
│   └── env.md           ← Global environment facts (8,000 chars)
├── project-facts/       ← Optional per-project overrides (auto-layered)
│   └── <path-hash>/
│       ├── user.md
│       └── env.md
└── episodes/
    ├── <session-id>.md  ← LLM-extracted summaries
    └── index.json       ← Metadata for search
```

### Tier 1 — Facts (in system prompt)

Two typed files, injected as frozen snapshot at session start. Managed by the agent via the `memory` tool.

| Target | File | Cap | Purpose |
|--------|------|-----|---------|
| `user` | `facts/user.md` | 4,000 | User preferences, style, pet peeves |
| `env` | `facts/env.md` | 8,000 | OS, tools, conventions, architecture |

**Frozen snapshot:** Loaded once at agent start into the system prompt. Live writes via the `memory` tool persist to disk immediately but appear in the prompt next session. This preserves LLM prefix caching.

### Tier 2 — Buffer (in session)

Not a file. Lives in `Session.Buffer []string` — a ring buffer capped at 20 lines. The loop engine appends a one-line summary after each turn:

```
HH:MM  user   "fix TOCTOU race"
HH:MM  agent  read file_tool.go, wrote security_e2e_test.go
HH:MM  agent  pushed 19 tests, tagged v0.8.19
```

- Injected into system prompt only when non-empty.
- Preserved across `odek continue` (serialized in session JSON).
- Oldest evicted when cap reached.

### Tier 3 — Episodes (on-disk, searchable)

After sessions with ≥3 turns, the MemoryManager extracts a session summary. When both episode extraction (`extract_on_end`) and fact extraction (`extract_facts`) are enabled, a **single combined LLM call** produces the episode summary and the durable facts in one JSON response (falling back to the two single-purpose calls if the combined response is unparseable). Written to `episodes/<session-id>.md`. Searchable via `memory(search=...)` — by default an LLM ranker orders episodes by relevance to the query; set `llm_search: false` to use the **RandomProjections** (go-vector) cosine ranker instead (zero LLM calls per search).

Episode extraction runs **asynchronously** — it does not block the agent loop. Session-end work is tracked by the MemoryManager and drained with a bounded wait (~15s) in `Agent.Close`, so episodes survive CLI exit without hanging the process.

## Memory Tool — Unified API

```json
{
  "name": "memory",
  "description": "Manage persistent memory across sessions.",
  "parameters": {
    "action": { "enum": ["add", "replace", "remove", "stats", "consolidate", "read", "search", "view", "add_atom", "search_atoms", "forget_atom", "list_quarantine", "pin_atom", "confirm_pending_review", "reject_pending_review", "list_pending_review"] },
    "target": { "enum": ["user", "env"], "description": "For add/replace/remove/consolidate/stats" },
    "content": { "type": "string", "description": "For add/replace" },
    "old_text": { "type": "string", "description": "Unique substring for replace/remove" },
    "query": { "type": "string", "description": "For search — facts + episodes" }
  }
}
```

### Actions

| Action | Target | Content | old_text | Effect |
|--------|--------|---------|----------|--------|
| `add` | user/env | ✅ new entry | — | Appends to file. Check: dedup + cap + merge |
| `replace` | user/env | ✅ replacement | ✅ substring | Finds entry by substring, replaces it |
| `remove` | user/env | — | ✅ substring | Finds entry by substring, removes it |
| `stats` | user/env | — | — | Per-entry sizes + fill (used/cap) — pre-flight check before writes |
| `consolidate` | user/env | — | — | SimpleCall: merge related entries for density |
| `read` | — | — | — | Returns full content of both user.md + env.md |
| `search` | — | — | ✅ query | LLM ranker by default (relevance-oriented); `llm_search: false` switches to RP cosine ranking (zero LLM calls) |

## Automatic Cap Maintenance (LLM-driven eviction)

The agent maintains its own fact files. When a fact file fills up, the **agent itself** evicts older entries — there is no background rewriter and no silent consolidation. Every eviction is an explicit, auditable `remove`/`replace` call inside the normal agent loop.

Three affordances make this work:

1. **Decision-ready cap errors.** When `add`/`replace` would exceed the cap, the error carries the full entry index — one-based index, size, preview — plus the instruction to free space with `memory remove`/`replace` and retry. The agent can pick an eviction target without an extra read.

   ```
   memory: adding entry (210 chars) would exceed cap (2500 chars); current: 2438, max: 2500.
   Entries (oldest first):
   [1] 480c "20260830 — odek v1.30.0 released: sub-agent cancel flow (PR #156, squas…"
   [2] 1051c "20260830 — Skill Self-Learning removal scoped (SKILL_LEARNING_REMOVAL_P…"
   Free space by removing or replacing older entries (memory remove/replace), then retry
   ```

2. **`stats` action.** `memory(action: "stats", target: "env")` returns `{used, cap, entries: [{index, chars, preview}]}` — a pre-flight fill check before large writes. `view` remains episodes-only by design (provenance gate).

3. **Eviction policy (tool contract).** The `memory` tool description codifies the priority: when a target is at cap, remove or replace the lowest-value entries — records recoverable from git/GitHub (release notes, merged PRs) evict first; pointers to untracked local work (plan docs, pending-review findings) evict last.

Additionally, the system-prompt memory block appends a one-line warning when a fact file is at ≥90% of its cap:

```
⚠ env fact file 97% full — evict stale entries via memory remove before your next add.
```

Caps are configured via `facts_limit_user` / `facts_limit_env` (defaults: 4,000 / 8,000 chars, counted as bytes — consistent with cap accounting). Deliberately **not** built: automatic consolidation on write (a mid-flow LLM rewrite risks dropping load-bearing details and amplifies provider throttling) and date-based auto-eviction (age alone says nothing about value — a days-old pointer to untracked work can be the only durable record of it).

## Merge-on-Write (go-vector Integration)

When adding a fact, a **two-tier merge detector** classifies the new entry:

```
RP.embed(newEntry) → cos similarity vs each existing entry

  cos > 0.7 ──────────────────→ auto-merge (replace old + new)
  cos < 0.3 ──────────────────→ auto-add (no conflict)
  0.3 ≤ cos ≤ 0.7 ──→ SimpleCall judgment → merge or add
```

This saves ~80% of LLM calls on memory writes.

**Implementation:** `internal/memory/merge.go` imports `github.com/BackendStack21/go-vector/pkg/vector` for `RandomProjections` and `Cosine`. The embedder is fit on existing facts when the detector is created, and re-fit whenever facts change. With `memory.embedding` configured (see *Pluggable Embeddings* below), the same classification runs over real semantic vectors from an OpenAI-compatible API instead of RP.

### Durability & Statefulness

Key design property: **facts persist as text; go-vector RP is ephemeral.**

| Component | Persists to disk? | Source of truth |
|-----------|-------------------|-----------------|
| Fact text (`user.md`, `env.md`) | ✅ Yes — plain markdown | The text *is* the durable state |
| Episode summaries (`episodes/*.md`) | ✅ Yes — markdown files | Durable |
| Episode index (`episodes/index.json`) | ✅ Yes — JSON | Durable |
| go-vector RP vocabulary + vectors | ❌ No — ephemeral | Rebuilt from text via `Fit()` |

**Why this is safe:** `RandomProjections` is a stateless model. `Fit(corpus)` builds vocabulary from the input text deterministically — same text always produces the same `(word → random vector)` mappings. On every `AddFact` / `Replace` / `Remove`, `MergeDetector.Fit(entries)` is called, reading all facts from disk and recomputing embeddings. No persistent state needs to be saved or restored.

**On restart:**
1. Fact text loads from disk (durable)
2. `MergeDetector` starts with empty corpus + fresh RP
3. First fact mutation triggers `Fit()` with all persisted facts — full merge protection restored
4. Between restart and first mutation, `Classify()` returns `"nobody"` (empty corpus) → entry is added directly without merge checks

This is fine because `MergeDetector` is an optimization (avoids ~80% of LLM calls), not a correctness requirement. Should you want eager initialization, call `memory(action: "read")` on startup — that reads both fact files without side effects while the system prompt already has the frozen snapshot.

### Cold Start

When `facts/user.md` and `facts/env.md` are empty (fresh install), `Fit()` produces an empty corpus. `Classify()` returns `"nobody"` and the entry is added directly — no merge checks, no SimpleCall. After the first few facts are written, subsequent mutations trigger `Fit()` with the growing corpus, and the detector self-trains.

## Subagent Memory

Subagents (separate OS processes via `odek subagent`) inherit a **read-only snapshot** of facts:

```
odek subagent --memory-snapshot /tmp/kode-mem-<rand>.json
```

The subagent's system prompt includes:
```
# Memory Context (read-only)
── User Profile ──
... (facts/user.md)
── Environment ──
... (facts/env.md)
```

Subagents do NOT get a `memory` tool — they cannot modify parent memory.

## Config

```json
{
  "memory": {
    "enabled": true,
    "facts_limit_user": 4000,
    "facts_limit_env": 8000,
    "buffer_lines": 20,
    "buffer_enabled": true,
    "merge_on_write": true,
    "extract_on_end": true,
    "llm_search": true,        // true (default) = LLM-based ranking; false = RP cosine ranker (zero LLM calls)
    "llm_extract": true,
    "llm_consolidate": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3,
    "extract_facts": false,
    "consolidate_on_end": true,
    "min_turns_for_extraction": 3,
    "episode_dedup_threshold": 0.92,
    "max_episodes": 500,
    "episode_ttl_days": 0,
    "auto_approve_episodes": false
  }
}
```

Additional keys (defaults shown):

| Field | Default | Description |
|-------|---------|-------------|
| `extract_facts` | `false` | Extract durable facts during session-end extraction. **Deliberately off by default**: wrong facts persist across sessions, so treat enabling it as accepting a persistent-poisoning risk (see [SECURITY.md](SECURITY.md)) |
| `consolidate_on_end` | `true` | LLM-merge session-end facts instead of appending raw |
| `min_turns_for_extraction` | `3` | Sessions shorter than this are not summarized |
| `episode_dedup_threshold` | `0.92` | Cosine similarity above which a new episode is dropped as a near-duplicate |
| `max_episodes` | `500` | Episode store cap |
| `episode_ttl_days` | `0` | Days before episodes expire; `0` = never expire |
| `auto_approve_episodes` | `false` | Auto-approve episodes derived from untrusted content instead of holding them for review — security escape valve, keep off |

## Extended Memory (opt-in)

`memory.extended` adds an optional **atomic memory** layer that extracts small, typed memory atoms from user messages and recalls them via semantic search. It does not replace the three-tier system; it augments it.

Key properties:

- **Opt-in only**: `memory.extended.enabled` defaults to `false`.
- **Atoms**, not episodes: stores fine-grained facts, preferences, intents, decisions, goals, conventions, file references, errors, and questions extracted from user messages.
- **Semantic recall**: uses the same embedding backend as episodes (`memory.embedding` or the shared top-level `embedding`) to rank atoms by cosine similarity.
- **Trust boundary**: per-turn extraction only produces `user_said` atoms. Tainted source classes (`tool_output`, `file_read`, `web`, `mcp`, `subagent`, `agent_generated`, `inferred`) can be stored but are quarantined and excluded from recall until promoted.
- **Size cap**: defaults to 100 MB with `retention_decay` eviction; pinned atoms are never evicted.
- **Tool surface**: `memory` tool actions `add_atom`, `search_atoms`, `forget_atom`, `pin_atom`, `list_quarantine`, `confirm_pending_review`, `reject_pending_review`, and `list_pending_review`.
- **CLI surface**: `odek memory extended forget|promote|pin|quarantine|compact|stats|consolidate|nudges|pending|confirm|reject`.

**Proactive nudges** (opt-in): when `memory.extended.proactive_nudges_enabled` is `true` (default `false`), Extended Memory can synthesize short, user-facing nudges from trusted atoms — open questions, stale goals, blockers, and drift. Delivery is capped by `nudge_max_per_day` with a per-kind cooldown (`nudge_cooldown_hours`); goals only become "stale" after `nudge_stale_goal_days`. The Telegram bot pushes at most one nudge after a completed turn (in the background, prefixed with 💡). `odek memory extended nudges` prints a preview of up to 2 nudges without consuming the daily cap.

When enabled, Extended Memory atoms are injected as a separate system message after the legacy memory block and episode summaries on each turn. For the full design, config reference, and implementation status, see [docs/EXTENDED_MEMORY.md](EXTENDED_MEMORY.md).

## Security

All memory content is scanned on write for:
- **Invisible Unicode** (zero-width spaces, RTL override, etc.)
- **Injection patterns** (prompt injection markers)
- **Credential patterns** (`sk-...`, `-----BEGIN`, bearer tokens)

Rejected content returns an error to the agent.

The optional prompt-injection guard subsystem ([docs/CONFIG.md](CONFIG.md#prompt-injection-guard)) applies a second opinion to the `memory` scope. This includes:
- `memory` tool `add`, `replace`, and `consolidate`
- legacy facts and `env.md`/`user.md` writes
- end-of-session auto-extracted facts
- the session buffer
- Extended Memory atom extraction, `add_atom`, and recall paths

The guard runs the local rule scan first, then optionally consults a configured `piguard` sidecar. Legacy fact writes that fail the scan are rejected with an error. Extended Memory atoms that fail the scan are instead **quarantined** with a `scan_rejected` reason (visible via `odek memory extended quarantine`) so guard false positives can be reviewed and promoted instead of silently lost; the `add_atom` tool result then reports "quarantined for human review" rather than "added".

## Observability (lifecycle events)

`odek memory extended stats` prints a snapshot of the Extended Memory store: live/quarantined atom counts, quarantine reason breakdown (`tainted` vs `scan_rejected` — the guard false-positive signal), index vector count and dirty flag, store size, and recall timeout/failure counters, with a degradation warning when recall errors have occurred in the process.

Every memory lifecycle moment emits a `memory.MemoryEvent` so operators can see
activity that was previously silent. Events fan out (via `MultiMemoryNotifier`)
to whichever surfaces are wired:

- **Terminal** — shown in verbose interaction mode (`--interaction verbose`),
  e.g. `🧠 memory[user] added: ...`, `🧠 consolidated memory[env] (5 → 2 entries)`.
- **Web UI** — streamed over the WebSocket as `memory_event` and surfaced as toasts.
- **Telegram** — posted in the chat when the bot runs verbose.
- **Programmatic** — set `Config.MemoryEventHandler` to receive every event.

| Event | Fired when | Key fields |
|---|---|---|
| `fact_added` | a new durable fact is appended (not on a silent dedup) | `Target`, `Content` |
| `fact_merged` | merge-on-write folds a fact into a near-duplicate | `Target`, `Content`, `Similarity` |
| `fact_replaced` | an existing fact is replaced | `Target`, `Content` |
| `fact_removed` | a fact is removed | `Target`, `Content` |
| `fact_consolidated` | LLM consolidation merges entries | `Target`, `Count`→`NewCount` |
| `episode_stored` | a session episode is extracted + persisted | `SessionID`, `Count` (turns), `Untrusted` |
| `episode_deduped` | a new episode replaces a near-duplicate, or an untrusted near-duplicate of a trusted episode is dropped | `SessionID`, `Similarity` |
| `episode_evicted` | episodes pruned by TTL / count cap | `Sessions`, `Count` |
| `episode_promoted` | a tainted episode is user-approved | `SessionID` |
| `episode_pending_review` | an untrusted episode is stored but excluded from recall | `SessionID` |

Notifiers must be non-blocking — fact writes fire mid-loop and episode events
fire from the post-session background goroutines.

The agent loop also emits `loop.SignalEvent`s for previously-silent self-healing
(`context_trimmed` when message groups are dropped to fit the context window,
`tool_recovery` when a repeatedly-failing tool triggers a corrective hint),
surfaced the same way via `Config.AgentSignalHandler`.

## Architecture

### Episode Index Caching

The episode index (`episodes/index.json`) is cached in memory after the first read. Every subsequent `FormatEpisodeContext` call (fires once per agent loop turn) hits the in-memory cache instead of re-reading + unmarshalling from disk. A read-write lock (`sync.RWMutex`) allows concurrent readers without blocking each other — only writes (rare, ~once per session) acquire the exclusive lock. The cache is invalidated after any write.

### Search Ranking

Episode search uses **RandomProjections** (go-vector) for similarity by default:

1. Fit RP embedder on episode summaries + query (64 dims, ~1ms)
2. Embed each summary and the query into 64-dimensional vectors
3. Score by cosine similarity between query vector and each summary vector
4. Return top-3 results sorted by score

This is zero LLM calls per search, ~1ms per search — available with `llm_search: false`. By default (`llm_search: true`) ranking uses an LLM SimpleCall to order episodes by relevance to the query — higher quality, higher latency + token cost.

### Pluggable Embeddings (`memory.embedding`)

RandomProjections is lexical: two texts only score as similar when they share
vocabulary. *"fixed the auth bug"* vs *"repaired login issue"* → cosine ≈ 0,
so recall misses semantically related episodes. The `memory.embedding` config
section replaces RP with a real embedding model behind any OpenAI-compatible
API (Ollama, llama.cpp server, LM Studio, vLLM, OpenAI, …):

```json
{
  "memory": {
    "embedding": {
      "provider": "http",
      "base_url": "http://localhost:11434/v1",
      "model": "nomic-embed-text"
    }
  }
}
```

One config switches **every** similarity path: per-turn episode recall, the
explicit `memory(search=...)` candidate retrieval, episode write-time dedup,
the non-LLM episode ranker, and fact merge-on-write classification.

Mechanics (`internal/embedding`, the shared package; memory references it via
package-local aliases in `internal/memory/embedder.go`):

- All paths go through the `embedding.TextEmbedder` seam — `rp` (default,
  corpus-fitted, bigram-featurized) or `http` (stateless, raw text, cached). The
  same package powers semantic session search and opt-in skill matching, so one
  endpoint config gives consistent embedding-space semantics across subsystems
  (see `docs/CONFIG.md` → *Shared embedding backend*).
- The persisted episode vector index records its embedding space in
  `episodes_index_meta.json`; changing provider/model/dims invalidates and
  rebuilds it automatically. Pre-existing RP indexes (no meta file) keep
  loading without a rebuild.
- The HTTP embedder caches text→vector within an instance, so per-turn query
  embeds are not re-sent. An index rebuild (after a new episode) runs on a
  fresh client *off the index lock* — one batch call over the corpus — so a
  slow backend never serializes concurrent recall.
- Failure mode: embedding errors degrade to "no recall context" / "add fact
  without merge check" / recency ranking — never a wrong dedup (which would
  delete the matched episode). A failed index rebuild backs off for 30s so a
  down backend is not re-hit every loop turn. The agent loop is never blocked.

See `docs/CONFIG.md` → `embedding` for all fields and the privacy note
(summaries are sent to the configured endpoint — use a local server if that
matters).
