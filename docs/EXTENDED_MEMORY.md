# Extended Memory Module

This document describes the proposed **Extended Memory** subsystem for odek. It is a reference-level design: what the module does, how it stores and retrieves memories, the trust model that keeps it safe, and the configuration that controls it.

Extended Memory is opt-in. When disabled, odek keeps the existing three-tier memory system described in [MEMORY.md](MEMORY.md) unchanged.

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
| `agent_inferred_from_user` | Trusted, but flagged `inferred` | Yes, with faster decay |
| `tool_output` | Tainted | No |
| `file_read` | Tainted when path escapes workspace | No |
| `web` / `browser` / `http_batch` | Tainted | No |
| `mcp` | Tainted | No |
| `subagent` | Tainted | No |
| `agent_generated` | Tainted | No |

User inputs are trusted because they come from the operator. Indirect content may be prompt-injected, malicious, or simply noisy, so it is never auto-recalled. The user can promote a tainted atom inline with commands such as:

- `odek, remember that`
- `odek, remember the browser output`
- `odek memory promote <atom-id>`

Promoted atoms change source class to `user_approved` and become recallable.

## Memory Atom

The atom is the atomic unit of Extended Memory.

```go
type MemoryAtom struct {
    ID          string        // stable identifier, 128-bit random hex
    CreatedAt   time.Time     // UTC
    SourceClass string        // user_said | inferred | user_approved | tool_output | file_read | web | mcp | subagent | agent_generated
    Type        string        // preference | intent | fact | decision | goal | convention | file | error | question
    Text        string        // the memory itself, capped to atom_max_chars
    Context     AtomContext   // session, project, turn, related atom IDs
    Vector      []float32     // embedding of Text
    Confidence  float32       // 0.0 - 1.0
    // Decay and TrustBoost are computed at recall/eviction time from CreatedAt and SourceClass.
}

type AtomContext struct {
    SessionID      string   `json:"session_id"`      // originating session
    Turn           int      `json:"turn"`            // turn within the session
    Project        string   `json:"project"`         // working directory at creation
    RelatedAtomIDs []string `json:"related_atoms"`   // semantic/temporal/task links
}
```

Atoms are stored as plain text in `extended/chunks/<atom-id>.md` and indexed by `extended/atoms.json`. Vectors live in `extended/vectors.gob` and are managed by go-vector.

### Atom Types

| Type | Meaning |
|---|---|
| `preference` | User style choices: verbosity, humor, formality, tool preferences. |
| `intent` | A goal the user stated or strongly implied. |
| `fact` | A durable fact the user asserted about themselves or the project. |
| `decision` | An architectural or design decision the user made. |
| `goal` | A medium-term objective spanning multiple sessions. |
| `convention` | A recurring pattern the user follows, e.g., "always benchmark after optimization." |
| `file` | A file the user repeatedly references. |
| `error` | A failure pattern or bug the user has hit before. |
| `question` | A recurring or unresolved question. |

## On-Disk Layout

```
~/.odek/memory/
├── facts/                    # existing user/env facts
├── episodes/                 # existing session summaries
└── extended/
    ├── atoms.json            # atom metadata + provenance
    ├── chunks/               # one .md file per atom
    │   └── <atom-id>.md
    ├── vectors.gob           # go-vector store over atoms
    ├── user_model.json       # inferred user model
    ├── quarantine.json       # tainted atoms awaiting promotion
    └── associations.json     # semantic/temporal/task links between atoms
```

All files are written atomically and use `0600` permissions. The vector store and metadata are rebuilt from the chunk files if they are ever corrupted.

## Dedicated Memory LLM

Extended Memory can use its own LLM, separate from the main agent. This is ideal because memory work is a stream of small, structured tasks: atom extraction, user-state inference, and intent prediction. A 7B-14B local model is usually sufficient and costs orders of magnitude less than calling a large reasoning model every turn.

If `memory.extended.llm` is omitted, the module **MUST** use the default global model. The default global model is the fully resolved main agent LLM after all config layers have been merged: top-level `model`, `base_url`, `api_key`, `thinking`, `max_tokens`, `temperature`, and `timeout` from `~/.odek/config.json`, `ODEK_*` environment variables, and CLI flags. Extended Memory does not read any of those values again; it reuses the exact `llm.Client` instance constructed for the main agent loop.

If the default global model has reasoning/thinking enabled, memory extraction and reranking may be expensive. In that case the operator should configure a dedicated `memory.extended.llm` for cost isolation; a warning is emitted when thinking is enabled and no dedicated memory LLM is configured.

### Memory LLM Responsibilities

1. **Per-turn atom extraction**: read the latest user message, emit 0-N JSON atoms.
2. **User-state inference**: every N turns, update `user_model.json` from recent atoms.
3. **Predictive intent generation**: produce 2-3 likely follow-up questions before the main agent answers.
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
You are a memory extraction engine. Read the user's message and emit durable,
atomic memory objects. Only extract from the user's own statements and explicit
preferences. Never extract from code, URLs, tool output, or inferred commands.

Output a JSON array of objects:
[
  {"type": "preference|intent|fact|decision|goal|convention|file|error|question",
   "text": "concise first-person memory",
   "confidence": 0.0-1.0}
]
```

Extracted atoms are immediately:

1. Wrapped untrusted content (`<untrusted_content_*>`) is stripped from the user message before extraction.
2. Scanned for injection patterns and credential patterns.
3. Assigned `source_class: user_said`.
4. Embedded and written to the atom store.
5. Checked against the 100 MB size cap; low-retention atoms are evicted if needed.

## Semantic Search

Extended Memory replaces episode-based recall with semantic search over atom vectors.

### Recall Pipeline

1. Embed the latest user message via `memory.extended.embedding`.
2. Generate 2-3 predicted follow-up intents via the memory LLM.
3. Embed each predicted intent.
4. Query the go-vector store with the union of message vector + predicted-intent vectors.
5. Fetch `semantic_search_top_k * semantic_search_overfetch` candidates.
6. Drop tainted atoms and atoms below `semantic_search_min_score`.
7. Optionally rerank the candidate set with the memory LLM.
8. Return the top-K atoms.

### Ranking Formula

Candidate atoms are scored by a composite function before injection into the system prompt:

```
score = cosine_similarity(query_vector, atom_vector)
        * confidence
        * decay_factor
        * trust_boost

decay_factor = 0.5 ^ (age_days / decay_half_life_days)
trust_boost  = 1.0 for user_said and user_approved
             = 0.8 for inferred
             = 0.0 for tainted
```

The final recall result is also bounded by `memory_budget_chars`.

## Predictive Recall

Predictive recall is the mechanism that creates the "telepathy" effect.

The memory LLM receives:

- The current user message.
- The current user-state model.
- The last 5 user messages.

It returns a JSON array of likely follow-up intents. Each intent is embedded and searched. The union of literal-query matches and predicted-intent matches is injected into the main agent's system prompt.

Example:

- User: "Refactor the auth package to remove JWT."
- Predicted intents: "how do I migrate refresh tokens?", "which tests should I update?", "what replaces JWT?"
- Agent's context now includes prior atoms about auth conventions, prior JWT discussions, and the user's preferred test style before the user asks the next question.

## User-State Model

The user model is a live, evolving JSON document stored in `extended/user_model.json`.

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

The model is updated in a background goroutine every N turns or whenever the inferred focus changes. Inferred preferences sit in `inferred_preferences_pending_review` until the user confirms or corrects them.

## Indirect Content Quarantine

All content that did not originate from the user goes into `extended/quarantine.json`.

A quarantined atom has the same schema as a trusted atom but:

- `source_class` is one of the tainted classes.
- `trust_boost` is 0.
- It is excluded from semantic search.
- It is subject to `quarantine_ttl_days` and may be auto-deleted.

Promotion commands move an atom from `quarantine.json` to `atoms.json` and reclassify it as `user_approved`.

## Size Cap and Eviction

Extended Memory enforces a hard disk budget. The default is 100 MB and is configurable via `max_size_mb`. The cap applies only to the `extended/` directory; existing `facts/` and `episodes/` keep their own lifecycle controls and are not counted.

### Storage Budget at 100 MB

| Component | Approx. Budget |
|---|---|
| Atom chunks (`chunks/*.md`) | ~70 MB |
| Vectors (`vectors.gob`) | ~20 MB |
| Metadata (`atoms.json`) | ~8 MB |
| User model (`user_model.json`) | ~1 MB |
| Quarantine (`quarantine.json`) | ~1 MB |

At `atom_max_chars: 300`, this holds roughly 16,000-18,000 atoms.

### Eviction Policy

The default policy is `retention_decay`. When a write would exceed `max_size_mb`, the module evicts atoms with the lowest retention score until the budget is met.

```
retention_score = confidence
                  * decay_factor
                  * trust_boost
                  * pin_boost

pin_boost = infinity for pinned atoms, 1.0 otherwise
decay_factor = 0.5 ^ (age_days / decay_half_life_days)

trust_boost  = 1.0 for user_said and user_approved
             = 0.8 for inferred
             = 0.0 for tainted
```

Eviction order:

1. Tainted quarantined atoms beyond their TTL.
2. Lowest-scoring trusted atoms.
3. Never evict: pinned atoms, the user model, or the association index skeleton.

### Compaction

The vector store is rewritten periodically to reclaim space left by evicted atoms. This is a background operation and never blocks a turn.

## Configuration

Extended Memory is configured under the `memory.extended` section.

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
| `eviction_policy` | `"retention_decay"` | Eviction algorithm. `"retention_decay"` scores atoms by confidence, age-based decay, and trust; lowest scores are evicted first. |
| `predictive_intents` | `3` | Number of follow-up intents to predict per turn. |
| `auto_extract_per_turn` | `true` | Extract atoms after every user message. |
| `infer_user_state` | `true` | Update the user model in the background. |
| `llm` | omitted | Dedicated memory LLM config. **If omitted, the default global model is used.** A warning is emitted if that model has thinking enabled. |
| `embedding` | omitted | Dedicated embedding backend. If omitted, uses the shared `embedding` config. |

## CLI and Tool Surface

The existing `memory` tool is extended with new actions:

```json
{
  "name": "memory",
  "parameters": {
    "action": "add_atom | search_atoms | promote_atom | pin_atom | forget_atom | read_user_model | list_quarantine"
  }
}
```

Additional CLI commands:

```bash
odek memory extended promote <atom-id>
odek memory extended pin <atom-id>
odek memory extended forget <atom-id>
odek memory extended quarantine
odek memory extended compact
```

## Proactive Behaviors

Once Extended Memory is enabled, the agent can exhibit proactive behaviors:

- **Return after break**: on session resume, summarize where the user left off and what the next likely step is.
- **Anaphora resolution**: pronouns like "that" or "it" are resolved against recent atoms, not just the last buffer line.
- **Follow-up anticipation**: the agent pre-loads related conventions, test patterns, and file references based on predicted intent.
- **Style mirroring**: tone, verbosity, and explanation depth adapt automatically to the user model.

These behaviors are always data-driven by trusted atoms and the user model. They are never driven by tainted content.

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

This runs memory extraction, prediction, and embedding locally while the main agent uses a powerful remote reasoning model.

## Implementation Phases

| Phase | Scope |
|---|---|
| **P0 — Atom store and dedicated LLM** | Config schema, dedicated `llm.Client` wiring, atom schema, per-turn extraction, trusted write path, `memory` tool extensions. |
| **P1 — Vector index and semantic recall** | go-vector store over atoms, top-K semantic search, provenance filtering, min-score gate, optional LLM rerank. |
| **P2 — Size enforcement** | 100 MB cap tracking, `retention_decay` eviction, background compaction. |
| **P3 — User-state model** | Background inference of `user_model.json`, pending-review queue, user correction flow. |
| **P4 — Quarantine and promotion** | Tainted atom quarantine, inline promotion commands, `quarantine_ttl_days`. |
| **P5 — Predictive and proactive surfaces** | Predicted-intent recall, return-after-break summary, anaphora resolution, style mirroring. |

Phases P0 and P1 deliver the core "remembers nearly everything" behavior. P2 adds the resource bound. P3-P5 create the anticipatory, telepathic feel.

## Relationship to Existing Memory

Extended Memory does not replace the existing three-tier system; it augments it.

- `facts/user.md` and `facts/env.md` remain the frozen snapshot at session start.
- `episodes/` remains for session-level summaries.
- `extended/` adds fine-grained, searchable, anticipatory memory.

The per-turn system prompt injection order is:

1. Frozen facts.
2. Buffer summary.
3. Extended Memory atoms (ranked, budgeted).
4. Episode summaries (if still enabled).

Operators can disable Extended Memory at any time and fall back to the original behavior.

## Open Questions

1. Should the 100 MB cap include or exclude the existing `episodes/` and `facts/` directories?
2. Should inferred preferences require explicit confirmation, or should they be recallable immediately with a confidence threshold?
3. Should quarantined atoms still be searchable via an explicit `memory search_quarantine` tool?
4. Should associations be auto-discovered by cosine similarity only, or also extracted explicitly by the memory LLM?

These questions are left to the implementation phase and can be resolved behind config flags so operators can choose their preferred privacy/convenience trade-off.
