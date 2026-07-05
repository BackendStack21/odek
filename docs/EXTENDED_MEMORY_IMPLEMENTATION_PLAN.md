# Extended Memory Implementation Plan

This document turns the reference design in [EXTENDED_MEMORY.md](EXTENDED_MEMORY.md) into an actionable implementation roadmap. It is organized by phase, with concrete files, public APIs, test targets, and acceptance criteria.

## Scope

The first deliverable covers **P0, P1, and P2** from the reference design:

- P0: Atom store + dedicated LLM wiring + trusted write path.
- P1: Vector index + semantic recall (top-K, configurable).
- P2: 100 MB size cap + `retention_decay` eviction + compaction.

P3-P5 (user-state model, quarantine flow, predictive/proactive surfaces) are out of scope for the initial implementation but are designed in and have extension points reserved.

## Guiding Principles

1. **No regression to existing memory**: `facts/`, `episodes/`, and the `memory` tool keep working exactly as before when Extended Memory is disabled.
2. **Opt-in only**: `memory.extended.enabled` defaults to `false`.
3. **Fail-soft**: any Extended Memory failure (LLM down, embedding down, disk full) degrades to "no extended context" and never crashes the agent loop.
4. **Test-first**: every public type gets unit tests; integration tests cover the end-to-end write/search/evict flow.
5. **Security by default**: tainted content is never recallable without user promotion.

## New Files

```
internal/memory/extended/
├── config.go                 # ExtendedConfig + validation
├── atom.go                   # MemoryAtom, AtomContext, source-class constants
├── store.go                  # AtomStore: CRUD, persistence, indexing hooks
├── index.go                  # atomVectorIndex: go-vector wrapper
├── extractor.go              # per-turn atom extraction via memory LLM
├── recall.go                 # semantic search + ranking + budget
├── eviction.go               # size cap + retention_decay policy
├── quarantine.go             # tainted atom storage (P0 stub, P4 full)
├── usermodel.go              # user-state model (P0 stub, P3 full)
├── associations.go           # atom linking (P0 stub, P3 full)
├── extended_memory.go        # top-level orchestrator
├── extended_memory_test.go   # integration tests
└── fixtures_test.go          # shared test helpers

internal/config/
└── (update existing) loader.go, types in FileConfig/ResolvedConfig

cmd/odek/
└── memory_cmd.go             # extend `odek memory` subcommands

odek.go
└── wire ExtendedMemory into agent construction

docs/
└── EXTENDED_MEMORY.md        # already exists, update as features land
```

## Modified Files

| File | Change |
|---|---|
| `internal/memory/memory.go` | Add `Extended *ExtendedMemory` field to `MemoryManager`; delegate per-turn extraction and recall. |
| `internal/memory/tool.go` | Extend `memory` tool actions: `add_atom`, `search_atoms`, `forget_atom`. |
| `internal/config/loader.go` | Parse `memory.extended.*`; merge defaults; wire `llm` fallback to global model. |
| `odek.go` | Construct memory LLM from config or reuse main `llm.Client`; pass to `MemoryManager`. |
| `cmd/odek/memory_cmd.go` | Add `odek memory extended` subcommands. |

## Config Changes

Add to `internal/memory.MemoryConfig`:

```go
Extended *ExtendedConfig `json:"extended,omitempty"`
```

Define `ExtendedConfig` in `internal/memory/extended/config.go`:

```go
type ExtendedConfig struct {
    Enabled                *bool          `json:"enabled,omitempty"`
    MaxSizeMB              int            `json:"max_size_mb,omitempty"`
    SemanticSearchTopK     int            `json:"semantic_search_top_k,omitempty"`
    SemanticSearchOverfetch int          `json:"semantic_search_overfetch,omitempty"`
    SemanticSearchMinScore float32       `json:"semantic_search_min_score,omitempty"`
    SemanticSearchRerank   *bool          `json:"semantic_search_rerank,omitempty"`
    AtomMaxChars           int            `json:"atom_max_chars,omitempty"`
    MemoryBudgetChars      int            `json:"memory_budget_chars,omitempty"`
    DecayHalfLifeDays      int            `json:"decay_half_life_days,omitempty"`
    QuarantineTTLDays      int            `json:"quarantine_ttl_days,omitempty"`
    EvictionPolicy         string         `json:"eviction_policy,omitempty"`
    PredictiveIntents      int            `json:"predictive_intents,omitempty"`
    AutoExtractPerTurn     *bool          `json:"auto_extract_per_turn,omitempty"`
    InferUserState         *bool          `json:"infer_user_state,omitempty"`
    LLM                    *LLMConfig     `json:"llm,omitempty"`
    Embedding              *embedding.Config `json:"embedding,omitempty"`
}
```

Define `LLMConfig`:

```go
type LLMConfig struct {
    BaseURL       string  `json:"base_url,omitempty"`
    APIKey        string  `json:"api_key,omitempty"`
    Model         string  `json:"model,omitempty"`
    Thinking      string  `json:"thinking,omitempty"`
    MaxTokens     int     `json:"max_tokens,omitempty"`
    Temperature   float64 `json:"temperature,omitempty"`
    TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}
```

### Default Values

```go
func DefaultExtendedConfig() ExtendedConfig {
    return ExtendedConfig{
        Enabled:                boolPtr(false),
        MaxSizeMB:              100,
        SemanticSearchTopK:     10,
        SemanticSearchOverfetch: 4,
        SemanticSearchMinScore: 0.55,
        SemanticSearchRerank:   boolPtr(true),
        AtomMaxChars:           300,
        MemoryBudgetChars:      2000,
        DecayHalfLifeDays:      30,
        QuarantineTTLDays:      7,
        EvictionPolicy:         "retention_decay",
        PredictiveIntents:      3,
        AutoExtractPerTurn:     boolPtr(true),
        InferUserState:         boolPtr(true),
    }
}
```

## Phase P0 — Atom Store + Dedicated LLM Wiring

### P0.1 Config plumbing

**Tasks:**

1. Add `ExtendedConfig` and `LLMConfig` types.
2. Extend `internal/memory.MemoryConfig` with `Extended *ExtendedConfig`.
3. Update config loader merge logic:
   - Parse `memory.extended` from global and project config.
   - Apply defaults for any unset fields.
   - If `memory.extended.llm` is omitted, leave it `nil`; the wiring layer will substitute the main `llm.Client`.
4. Add tests in `internal/config/loader_test.go` for parsing and defaults.

**Acceptance criteria:**

- `ODEK_MEMORY_EXTENDED_ENABLED=true` is parsed correctly.
- `./odek.json` with `memory.extended.llm.model: "local"` is merged over `~/.odek/config.json`.
- Omitting `memory.extended.llm` leaves `Extended.LLM == nil`.

### P0.2 LLM wiring

**Tasks:**

1. In `odek.go`, after the main `llm.Client` is built, conditionally build a memory LLM:

```go
var memoryLLM memory.LLMClient = client
if cfg.MemoryConfig.Extended != nil && cfg.MemoryConfig.Extended.LLM != nil {
    lmc := cfg.MemoryConfig.Extended.LLM
    timeout := time.Duration(lmc.TimeoutSeconds) * time.Second
    if timeout <= 0 { timeout = 30 * time.Second }
    memoryLLM = llm.NewWithMaxTokens(
        lmc.BaseURL, lmc.APIKey, lmc.Model,
        lmc.Thinking, 0, lmc.MaxTokens, timeout,
    )
    if lmc.Temperature >= 0 {
        memoryLLM.(*llm.Client).Temperature = lmc.Temperature
    }
}
```

2. Pass `memoryLLM` to `MemoryManager` via a new constructor or setter.
3. Add `MemoryManager.extended *ExtendedMemory` field.
4. Initialize `ExtendedMemory` only when `memory.extended.enabled == true`.

**Acceptance criteria:**

- When `llm` is configured, `MemoryManager` uses a distinct client for extraction.
- When `llm` is omitted, `MemoryManager` uses the main client.
- When Extended Memory is disabled, no extra LLM client is created.

### P0.3 Atom schema and persistence

**Tasks:**

1. Define `MemoryAtom`, `AtomContext`, source-class constants, and type constants.
2. Implement `AtomStore`:
   - `Add(atom MemoryAtom) error` — persist chunk, metadata, vector hook.
   - `Get(id string) (MemoryAtom, error)`
   - `Remove(id string) error`
   - `List() ([]MemoryAtom, error)`
   - `Pin(id string) error`
3. Use `internal/fsatomic.WriteFile` for all writes.
4. Store chunks as `extended/chunks/<id>.md` with `0600`.
5. Store metadata as `extended/atoms.json` with `0600`.
6. Validate atom IDs with the same rules as session IDs.

**Acceptance criteria:**

- Round-trip write/read of an atom passes unit tests.
- Concurrent adds do not corrupt `atoms.json`.
- Invalid atom IDs are rejected.

### P0.4 Per-turn extraction

**Tasks:**

1. Implement `Extractor.Extract(userMessage string) ([]MemoryAtom, error)`.
2. Build a deterministic system prompt that returns JSON only.
3. Parse JSON array into atoms; reject malformed output.
4. Set `SourceClass = "user_said"`, `Confidence` from LLM, `CreatedAt` to UTC now.
5. Run `ScanContent` on each atom text; reject injection patterns.
6. Wire `MemoryManager.AppendBuffer` or a new `MemoryManager.OnUserMessage(msg string)` hook to call `Extractor.Extract` after the user's message is recorded.
7. Add tests with a mock LLM returning JSON.

**Acceptance criteria:**

- A user message produces 0-N atoms.
- Injection-laden extracted text is rejected.
- LLM failures are logged and ignored (fail-soft).

### P0.5 Tool surface

**Tasks:**

1. Extend `internal/memory/tool.go` with actions:
   - `add_atom`: manually add a trusted atom.
   - `search_atoms`: explicit semantic search.
   - `forget_atom`: remove an atom by substring or ID.
2. Keep existing `memory` actions unchanged.
3. Add tool tests.

**Acceptance criteria:**

- `memory(action: "add_atom", text: "...", type: "preference")` persists an atom.
- `memory(action: "search_atoms", query: "...")` returns ranked atoms.

## Phase P1 — Vector Index + Semantic Recall

### P1.1 Embedding backend

**Tasks:**

1. In `ExtendedMemory`, build an embedder using:
   - `memory.extended.embedding` if set,
   - else the shared top-level `embedding` config,
   - else RandomProjections fallback.
2. Reuse `internal/embedding` package; create a `textEmbedder` alias in `extended/`.
3. Embed atom text at write time and store the vector in `MemoryAtom.Vector`.

**Acceptance criteria:**

- HTTP embedding backend produces vectors.
- RP fallback works when no embedding config is present.
- Embedding failures cause the atom to be skipped, not crash.

### P1.2 Vector index

**Tasks:**

1. Implement `atomVectorIndex` in `internal/memory/extended/index.go`.
2. Use `github.com/BackendStack21/go-vector/pkg/vector`.
3. Provide:
   - `Add(id string, vec vector.Vector)`
   - `Remove(id string)`
   - `Search(queryVec vector.Vector, k int) []scoredAtom`
   - `Persist(dir string)` / `Load(dir string)`
   - `FitAndRebuild(atoms []MemoryAtom)`
4. Persist to `extended/vectors.gob`.
5. Add concurrency tests.

**Acceptance criteria:**

- Similar vectors rank higher than dissimilar ones.
- Persist/load round-trip works.
- Concurrent reads during a rebuild are safe.

### P1.3 Semantic recall

**Tasks:**

1. Implement `Recall.Query(query string, k int) ([]MemoryAtom, error)` in `internal/memory/extended/recall.go`.
2. Pipeline:
   - Embed query.
   - Fetch `k * overfetch` candidates.
   - Filter tainted atoms.
   - Apply `min_score` gate.
   - Compute composite score: `cosine * confidence * decay * trust_boost`.
   - Return top `k`.
3. If `semantic_search_rerank` is true, call the memory LLM to rerank candidates.
4. Bound final output by `memory_budget_chars`.
5. Wire `MemoryManager.BuildSystemPrompt` to append the Extended Memory section.

**Acceptance criteria:**

- Searching for a phrase returns atoms containing semantically related text.
- Tainted atoms never appear in results.
- Results respect `memory_budget_chars`.

### P1.4 Integration tests

**Tasks:**

1. Write `extended_memory_test.go` covering:
   - Add atom → search atom → remove atom.
   - Tainted atom is quarantined, not recalled.
   - Embedding backend fallback.
   - Budget enforcement.
2. Use a temp directory for each test.
3. Use mock LLM and mock embedder where possible.

**Acceptance criteria:**

- All tests pass with `go test ./internal/memory/extended/...`.
- Race detector passes: `go test -race ./internal/memory/extended/...`.

## Phase P2 — Size Cap + Eviction

### P2.1 Size tracking

**Tasks:**

1. Maintain a running total of `extended/` size.
2. On every write, compute projected new size:
   - chunk file size,
   - metadata JSON delta,
   - vector store delta.
3. If projected size > `max_size_mb * 1024 * 1024`, trigger eviction before write.

**Acceptance criteria:**

- Size is accurately tracked within 5% of actual disk usage.
- Tests verify cap enforcement.

### P2.2 Eviction policy

**Tasks:**

1. Implement `Evictor` interface with `SelectForEviction(atoms []MemoryAtom, needBytes int64) []string`.
2. Implement `retentionDecayEvictor`:
   - Compute retention score for each atom.
   - Sort ascending.
   - Return IDs whose removal frees `needBytes`.
3. Skip pinned atoms and atoms with `trust_boost == 0` (quarantine handled separately).
4. Call `AtomStore.Remove` for selected IDs and update `atomVectorIndex`.

**Acceptance criteria:**

- Adding atoms beyond the cap evicts the lowest-retention atoms.
- Pinned atoms are never evicted.
- Eviction frees enough space for the new write.

### P2.3 Compaction

**Tasks:**

1. Implement `atomVectorIndex.Compact()`:
   - Rebuild the vector store from current atoms only.
   - Rewrite `vectors.gob`.
2. Trigger compaction after eviction removes >10% of atoms, or on `odek memory extended compact`.
3. Make compaction a background goroutine.

**Acceptance criteria:**

- Compaction reclaims space from removed atoms.
- Compaction does not block recall.

### P2.4 Quarantine accounting

**Tasks:**

1. Ensure quarantined atoms count toward `max_size_mb`.
2. Evict expired quarantined atoms first, before trusted atoms.
3. Tests for mixed trusted/quarantined eviction.

**Acceptance criteria:**

- Expired quarantined atoms are removed first.
- Non-expired quarantined atoms are retained but excluded from recall.

## Testing Strategy

| Level | Command | Coverage Target |
|---|---|---|
| Unit | `go test ./internal/memory/extended/...` | >80% |
| Race | `go test -race ./internal/memory/extended/...` | zero races |
| Integration | `go test ./internal/memory/...` | full P0-P2 flow |
| Config | `go test ./internal/config/...` | extended config parsing |
| E2E | manual + benchmark | agent recall quality |

### Key Test Cases

1. `TestAtomStore_RoundTrip`
2. `TestExtractor_UserSaidAtoms`
3. `TestExtractor_RejectsInjection`
4. `TestRecall_TaintedExcluded`
5. `TestRecall_TopK`
6. `TestRecall_BudgetEnforced`
7. `TestEviction_CapEnforced`
8. `TestEviction_PinProtected`
9. `TestEmbedding_RP_Fallback`
10. `TestConfig_GlobalModelFallback`

## Security Checklist

- [ ] `ScanContent` runs on every atom text before persistence.
- [ ] Indirect content is stored as tainted and never embedded into recall vectors.
- [ ] Atom IDs validated like session IDs to prevent path traversal.
- [ ] All files created with `0600` permissions.
- [ ] Memory LLM only sees user messages and trusted atoms.
- [ ] No self-promotion: agent cannot call `promote_atom` on its own initiative.
- [ ] Size cap prevents disk-DoS.
- [ ] Quarantine TTL prevents indefinite retention of tainted content.
- [ ] Atomic writes prevent corruption on crash.

## Migration and Rollout

1. **Default off**: existing users see no change.
2. **Opt-in**: users add `memory.extended.enabled: true` to `~/.odek/config.json`.
3. **No data migration**: `facts/` and `episodes/` are untouched.
4. **Backwards compatible**: disabling Extended Memory removes the injected context but leaves files on disk.
5. **Version note**: document in release notes that Extended Memory is experimental.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Embedding backend down | Fall back to RandomProjections or skip extended recall. |
| Memory LLM produces bad atoms | Confidence threshold + scan on write + user can forget. |
| Disk cap too small | Configurable `max_size_mb`; users can raise it. |
| Recall noise | `min_score`, reranking, decay, and budget limit injection. |
| Prompt injection via extracted atoms | Only user text is extracted; scan rejects known patterns. |
| Performance regression | Background extraction, cached index, bounded search. |

## Success Metrics

After P0-P2:

- Extended Memory adds <50 ms per turn when extraction is cached.
- Semantic recall returns at least one relevant atom for 70% of follow-up questions in manual evaluation.
- Disk usage stays under `max_size_mb` in long-running use.
- Zero regressions in existing memory tests.

## Out of Scope (P3-P5)

- User-state model inference and pending-review queue (P3).
- Full quarantine promotion flow and inline commands (P4).
- Predictive intent generation, anaphora resolution, return-after-break summary (P5).

These are designed in and have reserved extension points but will be implemented in follow-up work.

## Task Board

```
[P0.1] Config plumbing
[P0.2] LLM wiring
[P0.3] Atom schema and persistence
[P0.4] Per-turn extraction
[P0.5] Tool surface
[P1.1] Embedding backend
[P1.2] Vector index
[P1.3] Semantic recall
[P1.4] Integration tests
[P2.1] Size tracking
[P2.2] Eviction policy
[P2.3] Compaction
[P2.4] Quarantine accounting
[DOC]  Update EXTENDED_MEMORY.md as code lands
[REL]  Release notes and migration guide
```

## First PR Recommendation

The first PR should contain **P0.1, P0.2, and P0.3** only: config, LLM wiring, and atom persistence. This keeps the review focused and establishes the package structure. Each subsequent PR covers one of the remaining phases.
