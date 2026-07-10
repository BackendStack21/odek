# Extended Memory Module

This document describes the **Extended Memory** subsystem for odek. It is a reference-level design: what the module does, how it stores and retrieves memories, the trust model that keeps it safe, and the configuration that controls it.

Extended Memory is opt-in. When disabled, odek keeps the existing three-tier memory system described in [MEMORY.md](MEMORY.md) unchanged.

**Implementation status:** Phases P0–P2 are implemented and active: atom store, dedicated LLM wiring, semantic recall, and size-cap eviction. Phases P3–P5 (user-state model, full quarantine promotion flow, and predictive/proactive surfaces) are reserved extension points: their config fields exist, but the runtime behavior is currently a stub or partially implemented.

## Goals

1. **Near-comprehensive recall** of the user's own statements, preferences, goals, and recurring patterns.
2. **Anticipatory context**: the agent should load the context for the question the user is about to ask before they ask it.
3. **Bounded resource usage**: a hard 100 MB default disk cap with intelligent eviction.
4. **Semantic retrieval**: configurable top-K vector search over memory atoms instead of session-level summaries.
5. **Trust-preserving storage**: user speech is trusted by default; everything indirect is tainted and quarantined until explicitly approved.
6. **Cost isolation**: memory work can run on a dedicated, lightweight LLM while the main agent keeps using a powerful reasoning model.

## Mental Model

The existing memory system stores **session episodes**: one narrative summary per finished session. That is good for "what did we do last Tuesday" but poor for "the user prefers short answers" or "we always add tests after refactoring auth code."

Extended Memory stores **atoms**: small, typed, semantically indexed memory objects extracted from the user's own messages. Atoms are the unit of search, ranking, eviction, and provenance. Session episodes remain as an aggregate legacy layer, but atoms become the primary recall surface.

## Trust Model

The single most important design decision is the trust boundary.

| Source Class | Default Trust | Recallable Without Promotion |
|---|---|---|
| `user_said` | Trusted | Yes |
| `user_approved` | Trusted | Yes |
| `inferred` | Tainted / quarantined | No |
| `tool_output` | Tainted | No |
| `file_read` | Tainted | No |
| `web` | Tainted | No |
| `mcp` | Tainted | No |
| `subagent` | Tainted | No |
| `agent_generated` | Tainted | No |

User inputs and explicitly user-approved atoms are trusted because they come from the operator. Indirect content may be prompt-injected, malicious, or simply noisy, so it is never auto-recalled. `inferred` atoms are treated as tainted: they are quarantined until a user explicitly promotes them, matching the code's `IsTaintedSourceClass` classification. Planned promotion paths include inline commands such as:

- `odek, remember that`
- `odek, remember the browser output`
- `odek memory extended promote <atom-id>` (reserved for P4)

Promoted atoms would change source class to `user_approved` and become recallable. In the current release, atom quarantine is implemented but there is no promotion command; tainted atoms can be removed with `odek memory extended forget <atom-id>` or age out via `quarantine_ttl_days`.

## Memory Atom

The atom is the atomic unit of Extended Memory.

```go
type MemoryAtom struct {
    ID          string        // stable identifier, 128-bit random hex (32 chars)
    CreatedAt   time.Time     // UTC
    SourceClass string        // user_said | inferred | user_approved | tool_output | file_read | web | mcp | subagent | agent_generated
    Type        string        // fact | observation | preference | intent
    Text        string        // the memory itself, capped to atom_max_chars
    Context     AtomContext   // session, project, turn, related atom IDs
    Vector      vector.Vector // embedding of Text; NOT persisted in JSON, rebuilt from chunk text
    Confidence  float32       // 0.0 - 1.0
    Pin         bool          // pinned atoms are never evicted
}

type AtomContext struct {
    SessionID      string   `json:"session_id"`      // originating session
    Turn           int      `json:"turn"`            // turn within the session
    Project        string   `json:"project"`         // working directory at creation
    RelatedAtomIDs []string `json:"related_atom_ids"` // semantic/temporal/task links
}
```

### Atom Types

| Type | Meaning |
|---|---|
| `fact` | A durable fact the user asserted about themselves or the project. |
| `observation` | A stable observation worth recalling in future sessions. |
| `preference` | User style choices: verbosity, humor, formality, tool preferences. |
| `intent` | A goal the user stated or strongly implied. |

Additional types such as `decision`, `goal`, `convention`, `file`, `error`, and `question` are reserved for future phases (P3–P5). The current extraction prompt and tool schema only accept the four types above. Unknown types supplied by the LLM are normalized to `observation`.

## On-Disk Layout

```
~/.odek/memory/
├── facts/                       # existing user/env facts
├── episodes/                    # existing session summaries
└── extended/
    ├── atoms.json               # atom metadata + provenance
    ├── chunks/                  # one .md file per atom
    │   └── <atom-id>.md
    ├── vectors.gob              # persisted go-vector store
    ├── vectors.gob.emb          # persisted embedder state (RP vocabulary / HTTP fingerprint)
    ├── vectors_meta.json        # embedding-space fingerprint for invalidation
    └── quarantine.json          # tainted atoms awaiting promotion
```

`user_model.json` and `associations.json` are reserved for future phases (P3/P5) and are not currently written to disk.

All files are written atomically and use `0600` permissions. The vector store and metadata are rebuilt from the chunk files if they are ever corrupted.

## Dedicated Memory LLM

Extended Memory can use its own LLM, separate from the main agent. This is ideal because memory work is a stream of small, structured tasks: atom extraction, user-state inference, and intent prediction. A 7B-14B local model is usually sufficient and costs orders of magnitude less than calling a large reasoning model every turn.

If `memory.extended.llm` is omitted, the module **MUST** use the default global model. The default global model is the fully resolved main agent LLM after all config layers have been merged: top-level `model`, `base_url`, `api_key`, `thinking`, `max_tokens`, `temperature`, and `timeout` from `~/.odek/config.json`, `ODEK_*` environment variables, and CLI flags. Extended Memory does not read any of those values again; it reuses the exact `llm.Client` instance constructed for the main agent loop.

If the default global model has reasoning/thinking enabled, memory extraction and reranking may be expensive. In that case the operator should configure a dedicated `memory.extended.llm` for cost isolation; a warning is emitted when thinking is enabled and no dedicated memory LLM is configured.

### Memory LLM Responsibilities

1. **Per-turn atom extraction**: read the latest user message, emit 0-N JSON atoms.
2. **User-state inference** (reserved, P3): every N turns, update a persistent user model from recent atoms. Currently a no-op stub.
3. **Predictive intent generation** (reserved, P5): produce 2-3 likely follow-up questions before the main agent answers. Currently a no-op stub.
4. **Optional reranking**: rerank semantic-search candidates before they are injected into the system prompt.

### Security Constraint on the Memory LLM

The memory LLM is **data-bound**:

- It only sees user messages and already-trusted atoms.
- It never sees tool output, web content, MCP results, or subagent summaries.
- Its outputs are scanned with `ScanContent` before persistence.
- A compromised memory LLM can only add atoms; tainted atoms are still filtered by provenance.

## Per-Turn Extraction

After every user message, the memory LLM runs a tiny structured extraction.

**System prompt:**

```
You are a memory extraction system. Read the user message and extract durable, reusable atomic memories.

For each atom, output an object with:
- "text": a concise, first-person-paraphrased statement (not a command).
- "type": one of "fact", "observation", "preference", "intent".
- "confidence": a number 0.0-1.0 indicating how certain the memory is.

Rules:
- Only extract stable information worth recalling in future sessions.
- Do NOT extract instructions, commands, or requests to perform actions.
- Do NOT extract ephemeral details specific only to this message.
- If nothing durable is present, return an empty array.

Output ONLY a JSON array. Example:
[{"text":"User prefers concise answers","type":"preference","confidence":0.9}]
```

Extracted atoms are immediately:

1. Wrapped untrusted content (`<untrusted_content_*>`) is stripped from the user message before extraction.
2. Scanned for injection patterns, invisible Unicode, confusable scripts, and credential patterns (`sk-...`, PEM private keys, bearer tokens).
3. Assigned `source_class: user_said`.
4. Embedded and written to the atom store.
5. Checked against the 100 MB size cap; low-retention atoms are evicted if needed.

## Semantic Search

Extended Memory replaces episode-based recall with semantic search over atom vectors.

### Recall Pipeline

1. Embed the latest user message via `memory.extended.embedding`.
2. Query the go-vector store for the top `semantic_search_top_k * semantic_search_overfetch` candidates.
3. Drop tainted atoms and atoms below `semantic_search_min_score`.
4. Compute a composite score: `0.6 * cosine_similarity + 0.4 * retention_score`.
5. Optionally rerank the candidate set with the memory LLM.
6. Return the top-K atoms, bounded by `memory_budget_chars`.

Predictive-intent recall (using `predictive_intents`) is reserved for phase P5 and is currently a stub.

### Ranking Formula

Candidate atoms are scored by a composite function before injection into the system prompt:

```
retention_score = confidence
                    * decay_factor
                    * trust_boost

decay_factor = 0.5 ^ (age_days / decay_half_life_days)
trust_boost  = 1.0 for user_said and user_approved
             = 0.0 for inferred and all tainted source classes

composite_score = 0.6 * cosine_similarity(query_vector, atom_vector)
                  + 0.4 * retention_score
```

The final recall result is also bounded by `memory_budget_chars`. Tainted atoms are excluded regardless of score.

## Predictive Recall

> **Status:** Reserved for phase P5. The config field `predictive_intents` exists and defaults to `3`, but the runtime currently does not generate or search predicted intents. Only the literal user-message query is used for recall.

Predictive recall is the mechanism that creates the "telepathy" effect.

When implemented, the memory LLM will receive:

- The current user message.
- The current user-state model.
- The last 5 user messages.

It will return a JSON array of likely follow-up intents. Each intent will be embedded and searched. The union of literal-query matches and predicted-intent matches will be injected into the main agent's system prompt.

Example:

- User: "Refactor the auth package to remove JWT."
- Predicted intents: "how do I migrate refresh tokens?", "which tests should I update?", "what replaces JWT?"
- Agent's context now includes prior atoms about auth conventions, prior JWT discussions, and the user's preferred test style before the user asks the next question.

## User-State Model

> **Status:** Reserved for phase P3. The `UserModel` type and `infer_user_state` config field exist, but the model is not persisted to `extended/user_model.json` and `Summary()` returns empty.

The user model is a planned live, evolving JSON document stored in `extended/user_model.json`.

```json
{
  "style": {
    "verbosity": "low",
    "humor": "dry",
    "formality": "casual",
    "explanation_depth": "medium"
  },
  "technical": {
    "languages": ["Go"],
    "patterns": ["TDD", "microservices"],
    "tools": ["docker", "git", "go test"]
  },
  "current_focus": {
    "project": "odek",
    "task": "extended memory module",
    "blocker": null
  },
  "interaction_patterns": {
    "common_openers": ["let's", "can you", "what if"],
    "followup_after_refactor": "asks for tests",
    "followup_after_bugfix": "asks for benchmark"
  },
  "inferred_preferences_pending_review": []
}
```

When implemented, the model will be updated in a background goroutine every N turns or whenever the inferred focus changes. Inferred preferences will sit in `inferred_preferences_pending_review` until the user confirms or corrects them.

## Indirect Content Quarantine

Atoms with a tainted `source_class` (`tool_output`, `file_read`, `web`, `mcp`, `subagent`, `agent_generated`) are stored in `extended/quarantine.json` instead of the live atom corpus.

A quarantined atom has the same schema as a trusted atom but:

- `source_class` is one of the tainted classes.
- `trust_boost` is 0.
- It is excluded from semantic search.
- It is subject to `quarantine_ttl_days` and may be auto-deleted.

Per-turn extraction only produces `user_said` atoms, so normal user messages do not land in quarantine. Quarantine is used when an atom is explicitly added (via the tool/API or programmatically) with a tainted source class, or when future flows produce such atoms.

Promotion from quarantine to the live store is reserved for phase P4. Currently the only way to remove a quarantined atom is via `odek memory extended forget <atom-id>` or waiting for the TTL to expire.

## Size Cap and Eviction

Extended Memory enforces a hard disk budget. The default is 100 MB and is configurable via `max_size_mb`. The cap applies only to the `extended/` directory; existing `facts/` and `episodes/` keep their own lifecycle controls and are not counted.

### Storage Budget at 100 MB

| Component | Approx. Budget |
|---|---|
| Atom chunks (`chunks/*.md`) | ~70 MB |
| Vector store (`vectors.gob` + `vectors.gob.emb`) | ~20 MB |
| Vector meta (`vectors_meta.json`) | ~1 MB |
| Metadata (`atoms.json`) | ~8 MB |
| Quarantine (`quarantine.json`) | ~1 MB |

At `atom_max_chars: 300`, this holds roughly 16,000-18,000 atoms.

### Eviction Policy

The default policy is `retention_decay`. When a write would exceed `max_size_mb`, the module evicts atoms with the lowest retention score until the budget is met.

```
retention_score = confidence
                    * decay_factor
                    * trust_boost

pin_boost = infinity for pinned atoms, 1.0 otherwise
decay_factor = 0.5 ^ (age_days / decay_half_life_days)

trust_boost  = 1.0 for user_said and user_approved
             = 0.0 for inferred and all tainted source classes
```

Eviction order:

1. Expired quarantined atoms (beyond `quarantine_ttl_days`).
2. Lowest-scoring trusted atoms.
3. Never evict: pinned atoms.

The vector index is rebuilt from surviving chunks in the background if a large eviction frees enough space (currently >10% of the corpus). The index skeleton (`vectors_meta.json`) and quarantine file are always retained; their growth is bounded by the overall cap. User model and association files are reserved for future phases and are not currently persisted.

## Configuration

Extended Memory is configured under the `memory.extended` section.

> **Security note:** Project-level `./odek.json` is not allowed to set the `memory` or `embedding` sections (they could be used to redirect memory backends or poison the system prompt). Configure `memory.extended` in `~/.odek/config.json`, via `ODEK_MEMORY_EXTENDED_*` environment variables, or with the CLI flags listed below.

```json
{
  "memory": {
    "extended": {
      "enabled": true,
      "max_size_mb": 100,
      "semantic_search_top_k": 10,
      "semantic_search_overfetch": 4,
      "semantic_search_min_score": 0.55,
      "semantic_search_rerank": true,
      "atom_max_chars": 300,
      "memory_budget_chars": 2000,
      "decay_half_life_days": 30,
      "quarantine_ttl_days": 7,
      "eviction_policy": "retention_decay",
      "predictive_intents": 3,
      "auto_extract_per_turn": true,
      "infer_user_state": true,

      "llm": {
        "base_url": "http://localhost:11434/v1",
        "api_key": "",
        "model": "qwen2.5:7b",
        "max_tokens": 1024,
        "temperature": 0.2,
        "timeout_seconds": 30
      },

      "embedding": {
        "provider": "http",
        "base_url": "http://localhost:11434/v1",
        "model": "nomic-embed-text"
      }
    }
  }
}
```

### Field Reference

| Field | Default | Description |
|---|---|---|
| `enabled` | `false` | Master switch for Extended Memory. |
| `max_size_mb` | `100` | Hard disk budget. |
| `semantic_search_top_k` | `10` | Number of atoms returned to the system prompt. |
| `semantic_search_overfetch` | `4` | Candidate multiplier before filtering and reranking. |
| `semantic_search_min_score` | `0.55` | Minimum cosine similarity for a candidate to be considered. |
| `semantic_search_rerank` | `true` | Use the memory LLM to rerank candidates. |
| `atom_max_chars` | `300` | Maximum stored text length per atom. |
| `memory_budget_chars` | `2000` | Maximum injected memory context per turn. |
| `decay_half_life_days` | `30` | Days until an atom's recall/eviction weight halves, based on creation age. |
| `quarantine_ttl_days` | `7` | Days before a tainted atom is auto-deleted. |
| `eviction_policy` | `"retention_decay"` | Eviction algorithm. `"retention_decay"` scores atoms by confidence, age-based decay, and trust; lowest scores are evicted first. Currently the only supported value. |
| `predictive_intents` | `3` | Reserved for phase P5. Config is accepted but ignored by the current runtime. |
| `auto_extract_per_turn` | `true` | Extract atoms after every user message. |
| `infer_user_state` | `true` | Reserved for phase P3. Config is accepted but ignored by the current runtime. |
| `llm` | omitted | Dedicated memory LLM config. **If omitted, the default global model is used.** A warning is emitted if that model has thinking enabled. |
| `embedding` | omitted | Dedicated embedding backend. If omitted, uses the shared `embedding` config. |

### CLI and Environment Overrides

A subset of `memory.extended` fields can be set from the CLI or environment:

| Config field | CLI flag | Environment variable |
|---|---|---|
| `enabled` | `--memory-extended-enabled` | `ODEK_MEMORY_EXTENDED_ENABLED` |
| `max_size_mb` | `--memory-extended-max-size-mb` | `ODEK_MEMORY_EXTENDED_MAX_SIZE_MB` |
| `atom_max_chars` | `--memory-extended-atom-max-chars` | `ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS` |
| `memory_budget_chars` | `--memory-extended-memory-budget-chars` | `ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS` |

## CLI and Tool Surface

The existing `memory` tool is extended with new actions:

```json
{
  "name": "memory",
  "parameters": {
    "action": "add_atom | search_atoms | forget_atom",
    "content": "string",
    "atom_type": "fact | observation | preference | intent",
    "confidence": 0.0-1.0,
    "query": "string",
    "atom_id": "string"
  }
}
```

| Action | Parameters | Effect |
|---|---|---|
| `add_atom` | `content` (required), `atom_type` (default: `observation`), `confidence` (default: `1.0`) | Manually add a user-approved atom. |
| `search_atoms` | `query` (required) | Explicit semantic search over the trusted atom corpus. |
| `forget_atom` | `atom_id` (required) | Remove an atom by ID from the live store or quarantine. |

Additional CLI commands:

```bash
odek memory extended forget <atom-id>
odek memory extended quarantine
odek memory extended compact
```

- `forget` removes an atom from the live store or quarantine.
- `quarantine` lists tainted atoms awaiting promotion.
- `compact` triggers a background rebuild of the vector index to reclaim space.

The `promote` and `pin` actions for quarantined atoms are reserved for phase P4 and are not yet exposed via the CLI or the tool surface. The `odek memory promote <session-id>` command that already exists promotes **episodes**, not atoms. Pinning is only available programmatically via `AtomStore.Pin` in the current release.

## Proactive Behaviors

Once Extended Memory is enabled, the following proactive behaviors are planned:

- **Return after break**: on session resume, summarize where the user left off and what the next likely step is.
- **Anaphora resolution**: the first pronoun in a user message (e.g. "that" or "it") is resolved against recent trusted atoms only when the top atom's semantic similarity is above `semantic_search_min_score`; otherwise the message is passed through unchanged. Only the first pronoun occurrence is replaced.
- **Follow-up anticipation**: the agent pre-loads related conventions, test patterns, and file references based on predicted intent.
- **Style mirroring**: tone, verbosity, and explanation depth adapt automatically to the user model.

These behaviors are reserved for phases P3–P5. The current P0–P2 implementation only performs per-turn atom extraction and literal-query semantic recall. When implemented, they will always be data-driven by trusted atoms and the user model, never by tainted content.

## Security Architecture

Extended Memory inherits and extends the provenance model from [MEMORY.md](MEMORY.md).

- **Source-class tagging**: every atom records where its content came from.
- **Taint quarantine**: indirect content is never embedded into the recallable vector space until promoted.
- **Scan on write**: every atom is scanned for injection patterns, invisible Unicode, and credential patterns before persistence.
- **Untrusted wrapper**: content from file reads, web fetches, MCP, and subagents is wrapped as untrusted before the main model ever sees it; the memory subsystem treats it as tainted.
- **No self-promotion**: the agent cannot promote a quarantined atom. Promotion requires an explicit user action or user message.
- **Bounded storage**: the size cap prevents a memory-DoS where an attacker fills disk with recalled junk.
- **Dedicated LLM isolation**: a compromised memory LLM can only add atoms; it cannot bypass provenance filtering or inject instructions into the main model's prompt.

## Suggested Local Stack

For the best cost/latency trade-off:

```json
{
  "model": "claude-sonnet-4",
  "base_url": "https://api.anthropic.com/v1",
  "memory": {
    "extended": {
      "enabled": true,
      "llm": {
        "base_url": "http://localhost:11434/v1",
        "model": "qwen2.5:7b",
        "max_tokens": 1024,
        "temperature": 0.2,
        "timeout_seconds": 30
      },
      "embedding": {
        "provider": "http",
        "base_url": "http://localhost:11434/v1",
        "model": "nomic-embed-text"
      }
    }
  }
}
```

This runs memory extraction and embedding locally while the main agent uses a powerful remote reasoning model. Predictive intent generation (P5) will run on the same local stack when implemented.

## Implementation Phases

| Phase | Scope | Status |
|---|---|---|
| **P0 — Atom store and dedicated LLM** | Config schema, dedicated `llm.Client` wiring, atom schema, per-turn extraction, trusted write path, `memory` tool extensions. | Implemented |
| **P1 — Vector index and semantic recall** | go-vector store over atoms, top-K semantic search, provenance filtering, min-score gate, optional LLM rerank. | Implemented |
| **P2 — Size enforcement** | 100 MB cap tracking, `retention_decay` eviction, background compaction. | Implemented |
| **P3 — User-state model** | Background inference of a persistent user model, pending-review queue, user correction flow. | Reserved stub |
| **P4 — Quarantine and promotion** | Tainted atom quarantine, inline promotion commands, `quarantine_ttl_days`. Quarantine storage is implemented; atom promotion/pin CLI is reserved. | Partially implemented |
| **P5 — Predictive and proactive surfaces** | Predicted-intent recall, return-after-break summary, anaphora resolution, style mirroring. | Reserved stub |

Phases P0 and P1 deliver the core "remembers nearly everything" behavior. P2 adds the resource bound. P3-P5 create the anticipatory, telepathic feel and will land in follow-up work.

## Relationship to Existing Memory

Extended Memory does not replace the existing three-tier system; it augments it.

- `facts/user.md` and `facts/env.md` remain the frozen snapshot at session start.
- `episodes/` remains for session-level summaries.
- `extended/` adds fine-grained, searchable, anticipatory memory.

The per-turn system prompt injection order is:

1. Frozen facts.
2. Buffer summary.
3. Episode summaries (if still enabled).
4. Extended Memory atoms (ranked, budgeted).

Operators can disable Extended Memory at any time and fall back to the original behavior.

## Open Questions / Future Work

1. Should the 100 MB cap include or exclude the existing `episodes/` and `facts/` directories? Currently it only counts `extended/`.
2. Should inferred preferences require explicit confirmation, or should they be recallable immediately with a confidence threshold?
3. Should quarantined atoms still be searchable via an explicit `memory search_quarantine` tool? The current `quarantine` CLI lists them but does not search by embedding.
4. Should associations be auto-discovered by cosine similarity only, or also extracted explicitly by the memory LLM?
5. When will the P3–P5 reserved extension points (user-state model, full quarantine promotion, predictive/proactive surfaces) be fully implemented?

These questions can be resolved behind config flags so operators can choose their preferred privacy/convenience trade-off.
