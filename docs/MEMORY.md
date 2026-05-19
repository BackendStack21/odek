# Memory System

kode has a **three-tier file-based memory** system. Zero external dependencies beyond go-vector (which is also zero-dep).

## Three Tiers

```
~/.kode/memory/
├── facts/
│   ├── user.md          ← Global user profile (1,500 chars)
│   └── env.md           ← Global environment facts (2,500 chars)
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
| `user` | `facts/user.md` | 1,500 | User preferences, style, pet peeves |
| `env` | `facts/env.md` | 2,500 | OS, tools, conventions, architecture |

**Frozen snapshot:** Loaded once at agent start into the system prompt. Live writes via the `memory` tool persist to disk immediately but appear in the prompt next session. This preserves LLM prefix caching.

### Tier 2 — Buffer (in session)

Not a file. Lives in `Session.Buffer []string` — a ring buffer capped at 20 lines. The loop engine appends a one-line summary after each turn:

```
HH:MM  user   "fix TOCTOU race"
HH:MM  agent  read file_tool.go, wrote security_e2e_test.go
HH:MM  agent  pushed 19 tests, tagged v0.8.19
```

- Injected into system prompt only when non-empty.
- Preserved across `kode continue` (serialized in session JSON).
- Oldest evicted when cap reached.

### Tier 3 — Episodes (on-disk, searchable)

After sessions with ≥3 turns, the MemoryManager runs SimpleCall to extract 1-3 durable facts. Written to `episodes/<session-id>.md`. Searchable via `memory(search=...)` which uses SimpleCall to rank episodes by relevance to the query.

## Memory Tool — Unified API

```json
{
  "name": "memory",
  "description": "Manage persistent memory across sessions.",
  "parameters": {
    "action": { "enum": ["add", "replace", "remove", "consolidate", "read", "search"] },
    "target": { "enum": ["user", "env"], "description": "For add/replace/remove/consolidate" },
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
| `consolidate` | user/env | — | — | SimpleCall: merge related entries for density |
| `read` | — | — | — | Returns full content of both user.md + env.md |
| `search` | — | — | ✅ query | SimpleCall: rank episodes + facts by relevance |

## Merge-on-Write (go-vector Integration)

When adding a fact, a **two-tier merge detector** classifies the new entry:

```
RP.embed(newEntry) → cos similarity vs each existing entry

  cos > 0.7 ──────────────────→ auto-merge (replace old + new)
  cos < 0.3 ──────────────────→ auto-add (no conflict)
  0.3 ≤ cos ≤ 0.7 ──→ SimpleCall judgment → merge or add
```

This saves ~80% of LLM calls on memory writes.

**Implementation:** `internal/memory/merge.go` imports `github.com/BackendStack21/go-vector/pkg/vector` for `RandomProjections` and `Cosine`. The RP embedder is fit on existing facts when the detector is created, and re-fit whenever facts change.

### Cold Start

When `facts/user.md` and `facts/env.md` are empty (fresh install), no RP vocabulary exists. The merge detector gracefully returns "no corpus" and all adds pass through to SimpleCall. After the first few facts are written, the detector self-trains on re-fit.

## Subagent Memory

Subagents (separate OS processes via `kode subagent`) inherit a **read-only snapshot** of facts:

```
kode subagent --memory-snapshot /tmp/kode-mem-<rand>.json
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
    "facts_limit_user": 1500,
    "facts_limit_env": 2500,
    "buffer_lines": 20,
    "buffer_enabled": true,
    "merge_on_write": true,
    "extract_on_end": true,
    "llm_search": true,
    "llm_extract": true,
    "llm_consolidate": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3
  }
}
```

## Security

All memory content is scanned on write for:
- **Invisible Unicode** (zero-width spaces, RTL override, etc.)
- **Injection patterns** (prompt injection markers)
- **Credential patterns** (`sk-...`, `-----BEGIN`, bearer tokens)

Rejected content returns an error to the agent.
