# Configuration

`odek uses a **layered configuration system** with convention over configuration — opt-in files and environment variables, no mandatory setup.

## Priority chain

Each layer overrides the one below it. Unset fields inherit from the layer below:

```
0.  ~/.odek/secrets.env     ← Auto-loaded into process environment on startup
1.  ~/.odek/config.json     ← Global defaults (shared across projects)
2.  ./odek.json             ← Project-specific overrides
3.  ODEK_* env vars         ← Runtime/environment overrides
4.  CLI flags               ← Explicit invocation (highest priority)
```

Layer 0 is unique: it does not hold config fields directly. Instead it injects
`KEY=VALUE` pairs into the process environment so they're available for:

- **Layer 1–2** `${VAR}` substitution in config files
- **Layer 3** `ODEK_*` env var lookups (e.g. `ODEK_API_KEY`)
- **Legacy fallbacks** like `DEEPSEEK_API_KEY` / `OPENAI_API_KEY`

## Config files

### Global defaults (`~/.odek/config.json`)

Shared across all projects:

```json
{
  "model": "deepseek-v4-flash",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "${ODEK_API_KEY}",
  "thinking": "",
  "max_iterations": 90,
  "sandbox": false,
  "interaction_mode": "engaging",
  "no_color": false,
  "no_agents": false,
  "max_tool_parallel": 4,
  "max_concurrency": 4,
  "trusted_proxies": [],
  "tool_progress": "all",
  "tool_progress_cleanup": true,
  "system": ""
}
```

### Project overrides (`./odek.json`)

Same schema as global. Only set the fields you want to override:

```json
{
  "model": "gpt-4o",
  "max_iterations": 30
}
```

> **Security note:** The following fields cannot be set in `./odek.json` because a malicious repository could use them to steal secrets, poison the system prompt, disable safety policy, or redirect data to attacker-controlled backends:
>
> - `base_url` — use `~/.odek/config.json`, `ODEK_BASE_URL`, or `--base-url`
> - `api_key` — use `~/.odek/config.json`, `ODEK_API_KEY`, or `~/.odek/secrets.env`
> - `system` — use `~/.odek/config.json`, `ODEK_SYSTEM`, or `--system`
> - `dangerous` — use `~/.odek/config.json`
> - `embedding` / `memory` / `sessions` / `skills.dirs` / `skills.embedding` / `web_search` — use `~/.odek/config.json`
> - `telegram` — use `~/.odek/config.json` or `ODEK_TELEGRAM_*` env vars
> - `guard` — use `~/.odek/config.json` or `ODEK_GUARD_*` env vars
> - `trusted_proxies` — use `~/.odek/config.json` or `ODEK_TRUSTED_PROXIES`
>
> If any of these appear in `./odek.json`, odek ignores them and prints a warning.

Both files are optional. Missing files are silently ignored. String values support `${VAR}` environment variable substitution — useful for API keys without plaintext storage.

## Secrets file (`~/.odek/secrets.env`)

Auto-loaded on every `odek` invocation before any config file or env var is read.
Each `KEY=VALUE` line is injected into the process environment via `os.Setenv`.

```
ODEK_API_KEY=sk-...
GITHUB_TOKEN=ghp_...
```

Rules:
- **File format:** `KEY=VALUE` — one per line, no `export` keyword needed
- **Blank lines and `#` comments** are skipped
- **Existing env vars are NOT overwritten** — if `ODEK_API_KEY` is already in the environment, the file is ignored for that key
- **Missing/unreadable file** is silently ignored (not an error)
- **Permissions:** keep `0600` (`chmod 600 ~/.odek/secrets.env`)

This lets you keep secrets out of config files entirely:

```json
// ~/.odek/config.json — no plaintext secrets
{
  "model": "deepseek-v4-flash",
  "api_key": "${ODEK_API_KEY}"      // ← resolved from secrets.env at runtime
}
```

## Environment variables

Every config knob has a `ODEK_*` counterpart:

| Variable | Maps to | Type |
|----------|---------|------|
| `ODEK_MODEL` | `--model` | string |
| `ODEK_BASE_URL` | `--base-url` | string |
| `ODEK_API_KEY` | config files only | string |
| `ODEK_THINKING` | `--thinking` | string |
| `ODEK_MAX_ITER` | `--max-iter` | int |
| `ODEK_SANDBOX` | `--sandbox` | bool |
| `ODEK_INTERACTION_MODE` | `--interaction-mode` | string |
| `ODEK_NO_COLOR` | `--no-color` | bool |
| `ODEK_NO_AGENTS` | `--no-agents` | bool |
| `ODEK_SYSTEM` | `--system` | string |
| `ODEK_SKILLS_LEARN` | `skills.learn` | bool |
| `ODEK_PROMPT_CACHING` | `prompt_caching` | bool |
| `ODEK_TOOL_PROGRESS` | `tool_progress` | string (all\|new\|verbose\|off) |
| `ODEK_SANDBOX_IMAGE` | `--sandbox-image` | string |
| `ODEK_SANDBOX_NETWORK` | `--sandbox-network` | string |
| `ODEK_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
| `ODEK_SANDBOX_MEMORY` | `--sandbox-memory` | string |
| `ODEK_SANDBOX_CPUS` | `--sandbox-cpus` | string |
| `ODEK_SANDBOX_USER` | `--sandbox-user` | string |
| `ODEK_APPROVE_PROJECT_SANDBOX` | — | bool | approve project-level `./odek.json` sandbox config without prompting |
| `ODEK_MAX_CONCURRENCY` | `max_concurrency` | int |
| `ODEK_MAX_TOOL_PARALLEL` | `max_tool_parallel` | int |
| `ODEK_TRUSTED_PROXIES` | `trusted_proxies` | string (comma-separated IPs/CIDRs) |
| `ODEK_MEMORY_EXTENDED_ENABLED` | `--memory-extended-enabled` | bool |
| `ODEK_MEMORY_EXTENDED_MAX_SIZE_MB` | `--memory-extended-max-size-mb` | int |
| `ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS` | `--memory-extended-atom-max-chars` | int |
| `ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS` | `--memory-extended-memory-budget-chars` | int |
| `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED` | — | bool |
| `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE` | — | float |
| `ODEK_MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED` | — | bool |
| `ODEK_MEMORY_EXTENDED_NUDGE_MAX_PER_DAY` | — | int |
| `ODEK_MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS` | — | int |
| `ODEK_MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS` | — | int |
| `ODEK_GUARD_PROVIDER` | `--guard-provider` | string |
| `ODEK_GUARD_URL` | `--guard-url` | string |
| `ODEK_GUARD_BATCH_URL` | `--guard-batch-url` | string |
| `ODEK_GUARD_LONG_URL` | `--guard-long-url` | string |
| `ODEK_GUARD_SOCKET_PATH` | `--guard-socket-path` | string |
| `ODEK_GUARD_THRESHOLD` | `--guard-threshold` | float |
| `ODEK_GUARD_TIMEOUT_SECONDS` | `--guard-timeout` | int |
| `ODEK_GUARD_FALLBACK_TO_LOCAL` | `--guard-fallback` / `--guard-no-fallback` | bool |
| `ODEK_GUARD_SCAN_MEMORY` | `--guard-scan-memory` / `--guard-no-scan-memory` | bool |
| `ODEK_GUARD_SCAN_SYSTEM_PROMPT` | `--guard-scan-system-prompt` / `--guard-no-scan-system-prompt` | bool |
| `ODEK_GUARD_SCAN_MCP_DESCRIPTIONS` | `--guard-scan-mcp` / `--guard-no-scan-mcp` | bool |
| `ODEK_GUARD_SCAN_SKILLS` | `--guard-scan-skills` / `--guard-no-scan-skills` | bool |
| `ODEK_GUARD_SCAN_TOOL_OUTPUTS` | `--guard-scan-tool-outputs` / `--guard-no-scan-tool-outputs` | bool |
| `ODEK_GUARD_SCAN_TELEGRAM` | `--guard-scan-telegram` / `--guard-no-scan-telegram` | bool |

## API key fallback order

`ODEK_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`

## Prompt-injection guard

Odek ships a pluggable prompt-injection guard subsystem that can be applied to high-trust surfaces. The guard is **defense-in-depth**: the fast, local rule-based scan (`danger.ScanInjection`) always runs first, and an optional external sidecar (`go-prompt-injection-guard`) can provide a second opinion when configured.

The guard is **off by default** in the sense that no sidecar is needed; the local scan always runs. To enable the optional sidecar, set `provider: "piguard"` and point `url` at the sidecar endpoint.

### Configuration

```json
{
  "guard": {
    "provider": "local",
    "url": "http://127.0.0.1:8080/detect",
    "batch_url": "",
    "long_url": "",
    "socket_path": "",
    "threshold": 0.9,
    "timeout_seconds": 5,
    "fallback_to_local": true,
    "max_text_length": 0,
    "scan": {
      "memory": true,
      "system_prompt": true,
      "mcp_descriptions": true,
      "skills": true,
      "tool_outputs": false,
      "telegram": false
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"local"` | `"local"` uses the built-in rule scan; `"piguard"` uses an external sidecar |
| `url` | `""` | Single-text detection endpoint (e.g. `http://127.0.0.1:8080/detect`) |
| `batch_url` | `""` | Batch detection endpoint; if unset, derived from `url` by substituting the endpoint path |
| `long_url` | `""` | Long-text detection endpoint; if unset, derived from `url` by substituting the endpoint path |
| `socket_path` | `""` | Unix socket of the piguard daemon (alternative to `url`); speaks the daemon's native newline-delimited JSON protocol directly, no HTTP gateway needed |
| `threshold` | `0.9` | Confidence above which an `INJECTION` verdict is treated as injected. The sidecar score is the confidence of the predicted label, so the threshold never applies to `BENIGN` results |
| `timeout_seconds` | `5` | Per-request timeout |
| `fallback_to_local` | `true` | If the sidecar fails, fall back to the local rule scan |
| `max_text_length` | `0` | Truncate text sent to the sidecar; `0` means no limit. The local scan still sees the full text |

### Scan scopes

| Scope | Default | Surfaces covered |
|-------|---------|------------------|
| `memory` | `true` | `memory` add/replace/consolidate, legacy facts, auto-extracted facts, session buffer, and Extended Memory atom extraction/addition/recall/user-model inference |
| `system_prompt` | `true` | `~/.odek/IDENTITY.md`, explicit `--system` / `ODEK_SYSTEM`, and project-level `AGENTS.md` |
| `mcp_descriptions` | `true` | MCP server tool descriptions supplied via `tools/list` |
| `skills` | `true` | Skill bodies loaded at startup; skill save/patch suggestions |
| `tool_outputs` | `false` | External tool outputs wrapped as `<untrusted_content_*>` (warning-only scan) |
| `telegram` | `false` | Telegram photo captions and voice transcripts before injection |

When a scope is not explicitly set, the core surfaces (`memory`, `system_prompt`, `mcp_descriptions`, `skills`) default to `true`; the optional expansion surfaces default to `false`. Regardless of scope, the fast local rule scan always runs on every guarded surface — the scope only toggles the sidecar second opinion.

### Examples

```bash
# Run with a local piguard sidecar
odek run --guard-provider piguard --guard-url http://127.0.0.1:8080/detect "task"

# Enable the optional skill and Telegram guards via environment
ODEK_GUARD_PROVIDER=piguard \
ODEK_GUARD_URL=http://127.0.0.1:8080/detect \
ODEK_GUARD_SCAN_SKILLS=true \
ODEK_GUARD_SCAN_TELEGRAM=true \
odek run "task"
```

> **Security note:** The entire `guard` section is rejected from project-level `./odek.json`. A malicious repository cannot disable the local scan or redirect memory/system-prompt content to an attacker-controlled endpoint.

## Parallel tool execution

When a model emits multiple tool calls in one response (`tool_calls` array with N entries), odek executes them **concurrently** in goroutines bounded by a semaphore.

| Field | Default | Env var | Description |
|-------|---------|---------|-------------|
| `max_tool_parallel` | `4` | `ODEK_MAX_TOOL_PARALLEL` | Max concurrent tool calls per iteration. 0 = default 4. Set to 1 for sequential execution. |

I/O-bound tools (read_file, search_files, shell) benefit most — latency drops from `sum(latencies)` to `max(latency)`.

**Approval gate:** When an approver is configured and the LLM returns multiple tool calls, a single batch approval prompt is shown before any tool executes. If approved, all tools run in parallel. If denied, no tools run.

## Concurrency and reverse-proxy trust

| Field | Default | Env var | Description |
|-------|---------|---------|-------------|
| `max_concurrency` | `4` | `ODEK_MAX_CONCURRENCY` | Max concurrent sub-agent tasks spawned by `delegate_tasks`. 0 = default 4. |
| `trusted_proxies` | `[]` | `ODEK_TRUSTED_PROXIES` | Comma-separated list of IP addresses or CIDR ranges whose `X-Forwarded-For` / `X-Real-Ip` headers are honoured by `odek serve` for rate-limit attribution. Empty list means forwarding headers are ignored. |

`trusted_proxies` is security-relevant: misconfiguring it can allow clients to spoof their IP and bypass the per-IP rate limiters on `/ws` upgrades and session lookups. Configure it only in operator-controlled sources (`~/.odek/config.json` or `ODEK_TRUSTED_PROXIES`).

## Skills configuration

The `skills` section controls the skill system:

```json
{
  "skills": {
    "max_auto_load": 3,
    "max_lazy_slots": 5,
    "learn": true,
    "llm_learn": true,
    "llm_curate": true,
    "import": {
      "max_size_bytes": 1048576,
      "timeout_seconds": 5,
      "require_https": false
    },
    "curation": {
      "staleness_days": 90,
      "auto_prune": false,
      "auto_curate": true,
      "skip_threshold": 1,
      "skip_reset_days": 30
    },
    "auto_save": {
      "enabled": true,
      "require_llm": true,
      "max_per_run": 3
    }
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `max_auto_load` | — | 3 | Max skills injected into system prompt on start |
| `max_lazy_slots` | — | 5 | Max skills loaded per user input via trigger matching |
| `learn` | `ODEK_SKILLS_LEARN` | `true` | Enable skill learning mode (detects patterns, suggests skills). On by default |
| `llm_learn` | — | `true` | Use LLM to enrich detected patterns. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `llm_curate` | — | `true` | Use LLM for curation quality assessment. **Template-only** — set via `odek init`, not parsed from JSON at runtime |
| `dirs` | — | [] | Extra skill directories beyond `~/.odek/skills` and `./.odek/skills` |
| `import.max_size_bytes` | — | 1048576 (1MB) | Max size for fetched skill content |
| `import.timeout_seconds` | — | 5 | HTTP timeout for skill URI fetch |
| `import.require_https` | — | false | Reject http:// URIs when true |
| `curation.staleness_days` | — | 90 | Days without use before flagging as stale |
| `curation.auto_prune` | — | false | Auto-delete stale skills on curate (no prompt) |
| `curation.auto_curate` | — | true | Run auto-curation after sessions (merge, dedup, prune) |
| `curation.skip_threshold` | — | 1 | Times a skill must be skipped before permanent suppression |
| `curation.skip_reset_days` | — | 30 | Days after which a skip expires (re-allows suggestion) |
| `auto_save.enabled` | — | true | Auto-save quality skill suggestions without prompting |
| `auto_save.require_llm` | — | true | Only auto-save if LLM enhancement was applied |
| `auto_save.max_per_run` | — | 3 | Max skills to auto-save per session |
| `embedding` | — | *(inherits top-level `embedding`)* | Optional override of the shared embedding backend for semantic skill matching. When unset, skills inherit the top-level `embedding` default with the per-turn query timeout bounded to 2s. See [Shared embedding backend](#shared-embedding-backend-embedding--memory-sessions--skills). |

## Memory configuration

The `memory` section controls the persistent memory system (see [docs/MEMORY.md](docs/MEMORY.md)):

```json
{
  "memory": {
    "enabled": true,
    "facts_limit_user": 1500,
    "facts_limit_env": 2500,
    "buffer_lines": 20,
    "buffer_enabled": true,
    "merge_on_write": true,
    "consolidate_on_end": true,
    "extract_on_end": true,
    "extract_facts": false,
    "llm_search": true,
    "llm_extract": true,
    "llm_consolidate": true,
    "merge_threshold": 0.7,
    "add_threshold": 0.3,
    "auto_approve_episodes": false,
    "episode_dedup_threshold": 0.92,
    "max_episodes": 500,
    "episode_ttl_days": 0,
    "embedding": {
      "provider": "http",
      "base_url": "http://localhost:11434/v1",
      "model": "nomic-embed-text",
      "api_key": "${OPENAI_API_KEY}",
      "dims": 0,
      "timeout_seconds": 10
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | true | Enable memory system entirely |
| `facts_limit_user` | 1500 | Max chars for `user.md` fact file |
| `facts_limit_env` | 2500 | Max chars for `env.md` fact file |
| `buffer_lines` | 20 | Max turn summaries in session buffer |
| `buffer_enabled` | true | Enable the turn-level buffer |
| `merge_on_write` | true | Use go-vector RP similarity to auto-merge related entries (fast, no LLM — uses simple string merge) |
| `consolidate_on_end` | true | At session end, run an LLM consolidation pass over `user.md` and `env.md` in a background goroutine. This is the quality complement to `merge_on_write`: merge-on-write handles obvious duplicates immediately (no LLM), while consolidation handles near-duplicates and paraphrases at session end with full LLM quality. Requires `llm_consolidate: true`. **Note:** facts in the borderline similarity band (0.3–0.7 cosine) are now always added immediately and only merged by this consolidation pass — if you set `consolidate_on_end: false`, near-duplicate facts will accumulate rather than being merged. |
| `extract_on_end` | true | At session end (≥3 turns), extract a narrative episode summary via LLM for later recall |
| `extract_facts` | **false** | **Opt-in.** At session end (≥3 turns), auto-extract a few **durable** facts (stable user preferences, project invariants) into `user.md`/`env.md`. Off by default — see the security note below. Independent of `extract_on_end`; to disable *all* end-of-session LLM extraction set `llm_extract: false`. |
| `llm_search` | true | Use LLM to rerank candidates for **explicit** `memory search` calls (the `memory` tool). Per-turn recall (`FormatEpisodeContext`) always uses the cached go-vector index — no LLM call on the hot path regardless of this setting. |
| `llm_extract` | true | Use LLM for end-of-session fact extraction |
| `llm_consolidate` | true | Use LLM to merge related fact entries |
| `merge_threshold` | 0.7 | Cosine similarity above which two fact entries are **auto-merged** without an LLM call (0.0–1.0). Raise it to merge less aggressively; lower it to merge more. |
| `add_threshold` | 0.3 | Cosine similarity below which a new fact entry is **auto-added** without an LLM call (0.0–1.0). Between `add_threshold` and `merge_threshold` the LLM decides. Keep `add_threshold` < `merge_threshold`. |
| `auto_approve_episodes` | false | **Security trade-off.** When true, untrusted episodes (sessions that touched web/MCP/out-of-workspace content) are auto-approved at session end so they are recalled without a manual `odek memory promote`. Leaving it `false` keeps the human review gate (recommended). |
| `episode_dedup_threshold` | 0.92 | Cosine similarity above which a newly written episode is treated as a near-duplicate of an existing one and **replaces** it (newest wins). An untrusted episode never replaces a trusted/approved one. `0` disables dedup. |
| `max_episodes` | 500 | Maximum number of stored episodes. On each write, episodes beyond this count are evicted oldest-first (both the summary file and the index entry). `0` disables the cap. |
| `episode_ttl_days` | 0 | Evict episodes older than this many days. `0` (default) disables TTL-based eviction. |
| `embedding` | *(inherits top-level `embedding`)* | Optional override of the embedding backend for episode recall, dedup, the non-LLM episode ranker, and fact merge-on-write. When unset, memory inherits the shared top-level [`embedding`](#shared-embedding-backend-embedding--memory-sessions--skills) default; if neither is set, local RandomProjections (lexical bag-of-words — fast, zero-cost, but no real semantics). See below. |

### Extended Memory (`memory.extended`)

`memory.extended` is an **opt-in** atomic memory layer. It extracts small, typed memory atoms from user messages and recalls them via semantic search over the atom corpus. It does not replace facts, the buffer, or episodes; it adds a fourth source of context that is injected after episodes on each turn. See [docs/EXTENDED_MEMORY.md](EXTENDED_MEMORY.md) for the full design.

> **Security note:** Project-level `./odek.json` cannot set the `memory` or `embedding` sections. Configure `memory.extended` in `~/.odek/config.json`, via the `ODEK_MEMORY_EXTENDED_*` environment variables, or with the CLI flags listed below.

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
      "semantic_dedup_threshold": 0.92,
      "consolidate_similarity_threshold": 0.9,
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

| Field | Default | Env var | CLI flag | Description |
|-------|---------|---------|----------|-------------|
| `enabled` | `false` | `ODEK_MEMORY_EXTENDED_ENABLED` | `--memory-extended-enabled` | Master switch for Extended Memory. |
| `max_size_mb` | `100` | `ODEK_MEMORY_EXTENDED_MAX_SIZE_MB` | `--memory-extended-max-size-mb` | Hard disk budget for the `extended/` directory. |
| `semantic_search_top_k` | `10` | — | — | Number of atoms returned to the system prompt. |
| `semantic_search_overfetch` | `4` | — | — | Candidate multiplier before filtering and reranking. |
| `semantic_search_min_score` | `0.55` | — | — | Minimum cosine similarity for a candidate to be considered. |
| `semantic_search_rerank` | `true` | — | — | Use the memory LLM to rerank candidates. |
| `semantic_dedup_threshold` | `0.92` | — | — | Cosine similarity at or above which an incoming atom is treated as a paraphrase of an existing live atom and refreshes it instead of appending. `0` disables the semantic tier (exact-match dedup always runs). |
| `consolidate_similarity_threshold` | `0.9` | — | — | Pairwise cosine similarity at or above which live atoms are grouped for LLM merging by `odek memory extended consolidate` / `ConsolidateAtoms`. |
| `atom_max_chars` | `300` | `ODEK_MEMORY_EXTENDED_ATOM_MAX_CHARS` | `--memory-extended-atom-max-chars` | Maximum stored text length per atom. |
| `memory_budget_chars` | `2000` | `ODEK_MEMORY_EXTENDED_MEMORY_BUDGET_CHARS` | `--memory-extended-memory-budget-chars` | Maximum injected Extended Memory context per turn. |
| `decay_half_life_days` | `30` | — | — | Days until an atom's recall/eviction weight halves. |
| `quarantine_ttl_days` | `7` | — | — | Days before a tainted atom is auto-deleted from quarantine. |
| `eviction_policy` | `"retention_decay"` | — | — | Eviction algorithm. `"retention_decay"` is the only supported value. |
| `predictive_intents` | `3` | — | — | Reserved for future predictive-intent recall (P5). Currently accepted but ignored. |
| `auto_extract_per_turn` | `true` | — | — | Extract atoms after every user message. |
| `infer_user_state` | `true` | — | — | Reserved for future user-state model inference (P3). Currently accepted but ignored. |
| `follow_up_suggestions_enabled` | `true` | `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTIONS_ENABLED` | — | Capture high-confidence predicted intents at recall time as follow-up suggestions (zero extra LLM cost). |
| `follow_up_suggestion_min_confidence` | `0.6` | `ODEK_MEMORY_EXTENDED_FOLLOW_UP_SUGGESTION_MIN_CONFIDENCE` | — | Minimum predicted-intent confidence for a follow-up suggestion. |
| `proactive_nudges_enabled` | `false` | `ODEK_MEMORY_EXTENDED_PROACTIVE_NUDGES_ENABLED` | — | Master switch for proactive nudge delivery (`TakeNudges`). Opt-in. |
| `nudge_max_per_day` | `1` | `ODEK_MEMORY_EXTENDED_NUDGE_MAX_PER_DAY` | — | Maximum proactive nudges delivered per day. |
| `nudge_cooldown_hours` | `24` | `ODEK_MEMORY_EXTENDED_NUDGE_COOLDOWN_HOURS` | — | Per-kind cooldown before a nudge of the same kind can fire again. |
| `nudge_stale_goal_days` | `7` | `ODEK_MEMORY_EXTENDED_NUDGE_STALE_GOAL_DAYS` | — | Days without activity before a goal/intent atom counts as stale for nudges. |
| `llm` | omitted | — | — | Dedicated memory LLM. If omitted, the main agent LLM is reused. A warning is emitted if that model has thinking enabled. |
| `embedding` | omitted | — | — | Dedicated embedding backend for atoms. If omitted, inherits `memory.embedding` or the shared top-level `embedding`. |

### `embedding` — real semantic embeddings (optional)

By default every similarity computation in memory uses go-vector
**RandomProjections**: a local, zero-dependency bag-of-words embedder. It is
fast but purely lexical — *"fixed the auth bug"* and *"repaired login issue"*
share no tokens and score ~0. Setting `embedding.provider` to `"http"` routes
all of those paths through any **OpenAI-compatible embeddings API** instead
(Ollama, llama.cpp server, LM Studio, vLLM, OpenAI, Voyage…), giving recall
that matches by meaning.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"rp"` | `"rp"` = local RandomProjections; `"http"` = OpenAI-compatible embeddings API. An `"http"` config missing `base_url` or `model` silently falls back to `"rp"` so memory keeps working. |
| `base_url` | — | API root, e.g. `http://localhost:11434/v1` (Ollama) or `https://api.openai.com/v1`. `${ENV_VAR}` expansion supported. |
| `model` | — | Embedding model name, e.g. `nomic-embed-text`, `text-embedding-3-small`. |
| `api_key` | — | Sent as `Authorization: Bearer <key>` when set. `${ENV_VAR}` expansion supported — keep secrets out of config files. |
| `dims` | 0 | Expected vector dimensionality; `0` infers it from the first response (recommended). |
| `timeout_seconds` | 10 | Per-request HTTP timeout. |

Operational notes:

- **Per-turn recall stays cheap.** Episode vectors live in a persisted index; a
  loop turn costs at most one embedding call (the query), bounded by
  `timeout_seconds`. If the backend is down, recall degrades to "no context"
  and rebuilds back off for 30s — the agent loop is never blocked. The index
  rebuild that follows a new episode (session-end) embeds the corpus on a fresh
  client *off* the index lock, so a slow backend never serializes concurrent
  recall; it is one batch call over the episode summaries.
- **Switching backends is safe.** The persisted index records which embedding
  space it was built in; changing `provider`/`model`/`dims` automatically
  invalidates it and rebuilds on next use (one batch embedding call). Note: with
  `dims: 0`, if a server silently changes a model's output dimensionality (e.g.
  a model upgrade under the same name) the fingerprint cannot detect it; recall
  self-heals to "no context" on the dimension mismatch and rebuilds on the next
  write. Pin `dims` if you want such a change to force an explicit rebuild.
- **`base_url` is an egress target — point it only at a server you trust.** Every
  episode summary and fact entry is POSTed there for embedding. The URL is used
  verbatim with no allowlist, so do not point it at internal/metadata endpoints
  (e.g. cloud metadata services) you would not otherwise expose. Prefer a local
  server (Ollama/llama.cpp) when episode/fact text must not leave the machine.

## Shared embedding backend (`embedding`) — memory, sessions & skills

The same embedder that powers memory also powers **semantic session search**
(the `session_search` tool) and **semantic skill matching**. Set one
**top-level `embedding` block** and *every* subsystem inherits it — one endpoint,
consistent embedding-space semantics everywhere. Each subsystem can still
override the default with its own block. The block uses the same fields as
`memory.embedding` above (`provider`/`base_url`/`model`/`api_key`/`dims`/`timeout_seconds`).

```json
{
  "embedding": {
    "provider": "http",
    "base_url": "http://localhost:11434/v1",
    "model": "nomic-embed-text"
  }
}
```

With just that block, memory recall, `session_search`, and skill matching all go
semantic.

| Subsystem | Inherits the shared `embedding`? | Optional override |
|-----------|----------------------------------|-------------------|
| **Memory** | ✅ when `memory.embedding` is unset | `memory.embedding` |
| **Sessions** (`session_search`) | ✅ when `sessions.embedding` is unset | `sessions.embedding` |
| **Skills** (lazy matching) | ✅ when `skills.embedding` is unset (timeout bounded) | `skills.embedding` |

Each override is optional and isolated — e.g. point skills at a smaller/faster
model while memory uses a higher-quality one:

```json
{
  "embedding": { "provider": "http", "base_url": "http://localhost:11434/v1", "model": "nomic-embed-text" },
  "skills":   { "embedding": { "provider": "http", "base_url": "http://localhost:11434/v1", "model": "all-minilm" } }
}
```

Operational notes:

- **Sessions self-heal across backend changes** exactly like memory: a
  `vectors_meta.json` fingerprint records the embedding space; changing
  `provider`/`model`/`dims` forces a one-time rebuild from the session files. A
  down backend degrades `session_search` to its keyword fallback and backs off
  for 30s — it never fails a session save.
- **Skill matching is the hot path — it inherits, but with a bounded timeout.**
  Skill matching runs on *every user turn*, so when skills inherit the shared
  default their per-turn query embed is capped at **2s** (regardless of the
  shared `timeout_seconds`) and any slow/failed/empty result falls back to the
  local keyword matcher. An explicit `skills.embedding` is respected verbatim —
  set its own `timeout_seconds` if you want a different bound. Memory and
  sessions are *not* capped (they embed infrequently and persist their vectors).
- The **egress warning above applies to every subsystem** — session transcripts
  and skill text are POSTed to `base_url`. Point it only at a server you trust.

### ⚠️ `extract_facts` — automatic fact learning (opt-in, off by default)

When enabled, after each session of ≥3 turns odek asks the LLM to pull a few
**durable** facts from the conversation — stable user preferences (`user.md`) and
project/environment invariants (`env.md`) — so it learns them without you calling
the `memory` tool. Facts are injected into **every** system prompt.

**Why it is off by default.** Turning conversation into always-injected memory is
a *persistent prompt-injection* surface. Several guards apply when it is on:

- It runs **only for trusted sessions** — a session that ingested untrusted
  content via tools (web, MCP, out-of-workspace file reads) writes no facts.
- The extractor is instructed to treat the conversation as **data**, never to act
  on instructions in it, and never to record "download-and-run" style content.
- A download-and-execute / pipe-to-shell filter drops the obvious exploit class,
  and the standard injection/credential scan, merge-on-write dedup, and char caps
  all still apply. A per-session count cap limits how many facts one session adds.

**The residual risk these do NOT remove:** the trusted-session gate only covers
content the agent fetched via *tools* — it does **not** cover untrusted text that
enters the *conversation* another way (e.g. you paste an attacker-controlled
snippet into a chat that otherwise stayed trusted). Such text is summarized by
the extractor and a *plausible, non-command* fact could still be stored and then
injected into every future prompt. This cannot be fully eliminated while the
feature is on.

**Recommendation.** Leave `extract_facts: false` (the default) on any host that
processes untrusted input. Enable it only in trusted, single-user setups where
you accept the trade-off, and periodically review stored facts with the `memory`
tool (`read`) — or remove a bad one with `memory remove`. To turn off *all*
end-of-session LLM extraction (episodes and facts), set `llm_extract: false`.

## Sub-agent configuration

The `subagent` section controls task decomposition and parallel sub-agent execution (see [docs/SUBAGENTS.md](docs/SUBAGENTS.md)):

```json
{
  "subagent": {
    "max_concurrency": 3,
    "timeout_seconds": 120,
    "max_iterations": 15
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `max_concurrency` | 3 | Max sub-agents running in parallel (max 8) |
| `timeout_seconds` | 120 | Default timeout per sub-agent (overridden by `--timeout`) |
| `max_iterations` | 15 | Default max think→act cycles per sub-agent (overridden by `--max-iter`) |

This section is optional. Omitted fields inherit sensible defaults.

> **Note**: The `subagent` section is currently read only from `odek.json` by the `odek subagent` command in test code. Runtime values (`max_concurrency`, `timeout_seconds`) are hardcoded in production `odek run`/`odek serve`. This may be wired up fully in a future release.

## MCP server configuration

Connect to **external MCP servers** and expose their tools to the agent.
Any MCP server that works with Claude Code works with odek — same config format.

```json
{
  "mcp_servers": {
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp"]
    },
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `command` | The executable to run |
| `args` | Optional command-line arguments |
| `env` | Optional environment variable overrides (empty string removes from env) |

Tools are registered as `<server_name>__<tool_name>` (e.g., `playwright__navigate`)
and are available in `odek run`, `odek repl`, `odek continue`, and `odek serve`.

See [docs/MCP.md](docs/MCP.md#odek-as-mcp-client) for detailed instructions.

## Telegram

The `telegram` section configures the Telegram bot integration and the `--deliver` flag.

```json
{
  "telegram": {
    "bot_token": "8610437446:AAElHFJ...",
    "allowed_users": [8592463065],
    "allowed_chats": [],
    "poll_interval": 1,
    "poll_timeout": 30,
    "max_msg_length": 4096,
    "max_download_size": 5242880,
    "media_quota_per_chat": 52428800,
    "session_ttl_hours": 24,
    "log_level": "info",
    "log_file": "",
    "default_chat_id": 8592463065
  }
}
```

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `bot_token` | `ODEK_TELEGRAM_BOT_TOKEN` | — (required) | Telegram bot API token from @BotFather |
| `allowed_users` | — | all | Restrict bot to specific user IDs |
| `allowed_chats` | — | all | Restrict bot to specific chat IDs |
| `poll_interval` | — | 1 | Seconds between poll cycles |
| `poll_timeout` | — | 30 | Long-poll timeout (1-60 seconds) |
| `max_msg_length` | — | 4096 | Max characters per message |
| `session_ttl_hours` | — | 24 | Hours an inactive chat's session stays in the in-memory cache before being reloaded from disk. This is cache-only — on-disk session expiry is `maintenance.sessions_max_age_days` (see [MAINTENANCE.md](MAINTENANCE.md)) |
| `max_download_size` | `ODEK_TELEGRAM_MAX_DOWNLOAD_SIZE` | 5242880 (5 MiB) | Per-file byte cap for Telegram voice/photo/document downloads. Set to `-1` to disable. |
| `media_quota_per_chat` | `ODEK_TELEGRAM_MEDIA_QUOTA_PER_CHAT` | 0 (disabled) | Total bytes of downloaded media allowed per chat. `0` disables the quota. |
| `log_level` | — | info | Log level: debug, info, warn, error |
| `log_file` | — | stderr | Log file path (empty = stderr) |
| `default_chat_id` | — | 0 | **Required for `--deliver`** — numeric chat ID where `odek run --deliver` sends results. Get this from your bot's update or use a tool like `@userinfobot`. |

### --deliver flag

The `--deliver` flag on `odek run` sends the agent's final response to the configured
`default_chat_id` as a plain text message. This enables **cron-based scheduled agent
workflows** — no daemon needed.

```bash
# Run an agent task and deliver the result to Telegram
odek run --deliver "Check the CI pipeline status"

# Works with task text first too
odek run "Daily summary" --deliver
```

See [docs/TELEGRAM.md](docs/TELEGRAM.md#cron-integration) for full cron setup instructions.

## Schedules

Configures the native in-process task scheduler (`odek schedule`). Job
definitions live in `~/.odek/schedules.json`; this section only tunes the
engine. Every field has an `ODEK_SCHEDULES_*` environment override.

```json
{
  "schedules": {
    "enabled": true,
    "max_concurrent": 2,
    "timezone": "UTC",
    "catchup": false,
    "allow_telegram_management": true,
    "telegram_admin_chats": [123456789],
    "telegram_admin_users": [987654321]
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_SCHEDULES_ENABLED` | `true` | Run the embedded scheduler inside `odek telegram`. Set false to run only a standalone `odek schedule daemon`. |
| `max_concurrent` | `ODEK_SCHEDULES_MAX_CONCURRENT` | `2` | Maximum scheduled jobs running at once. |
| `timezone` | `ODEK_SCHEDULES_TIMEZONE` | `UTC` | Default timezone for jobs that don't set their own `--tz`. |
| `catchup` | `ODEK_SCHEDULES_CATCHUP` | `false` | Global default for the missed-run policy: run a missed fire once on startup. |
| `allow_telegram_management` | `ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT` | `true` | Allow the Telegram `/schedule` commands to create/remove/toggle/run jobs. When false, the bot still lists and previews jobs but mutations must go through `odek schedule`. |
| `telegram_admin_chats` | `ODEK_SCHEDULES_TELEGRAM_ADMIN_CHATS` | `[]` | Comma-separated list of operator chat IDs. These IDs may use mutating `/schedule` commands **and** `/restart`. When empty, the bot falls back to `telegram.default_chat_id`. Read-only commands are unaffected. |
| `telegram_admin_users` | `ODEK_SCHEDULES_TELEGRAM_ADMIN_USERS` | `[]` | Comma-separated list of operator user IDs. These IDs may use mutating `/schedule` commands **and** `/restart`. Read-only commands are unaffected. |
| `dangerous` | see below | `{}` | Schedule-specific override for the dangerous-operations policy. |

### Schedule-specific dangerous policy

Scheduled jobs run unattended, so by default the scheduler denies any class that would require an approval prompt (`network_egress`, `system_write`, `code_execution`, `install`, `unknown`). You can override this for cron jobs without widening the policy for interactive CLI/REPL/WebUI use.

```json
{
  "schedules": {
    "dangerous": {
      "classes": {
        "network_egress": "allow",
        "system_write": "allow"
      },
      "allowlist": ["curl -s https://example.com/feed.xml"]
    }
  }
}
```

Environment overrides:

| Env | Format |
|---|---|
| `ODEK_SCHEDULES_DANGEROUS_CLASSES` | JSON object, e.g. `{"network_egress":"allow","system_write":"allow"}` |
| `ODEK_SCHEDULES_DANGEROUS_ALLOWLIST` | Comma-separated command strings |
| `ODEK_SCHEDULES_DANGEROUS_DENYLIST` | Comma-separated command strings |
| `ODEK_SCHEDULES_DANGEROUS_ACTION` | Global default action: `allow`, `deny`, or `prompt` |
| `ODEK_SCHEDULES_DANGEROUS_NON_INTERACTIVE` | `allow`, `deny`, or `prompt` (ignored: scheduled runs force `deny`) |

Safety floor that cannot be overridden:
- `non_interactive` is always `deny` (no human is present to approve).
- `destructive` and `blocked` classes are always denied.

Project-level `odek.json` cannot set `schedules.dangerous`; configure it via `~/.odek/config.json` or environment variables.

Full guide: [docs/SCHEDULES.md](SCHEDULES.md).

## Storage maintenance

Configures the background storage janitor (`internal/maintenance`). It runs a
sweep over `~/.odek` every `interval_minutes`: expiring old sessions and
audit records, rotating oversized logs, deleting stale Telegram plans and
downloaded media, and garbage-collecting expired skill skip-list entries.
Every field has an `ODEK_MAINTENANCE_*` environment override.

```json
{
  "maintenance": {
    "enabled": true,
    "interval_minutes": 60,
    "sessions_max_age_days": 30,
    "audit_max_age_days": 14,
    "log_max_mb": 50,
    "plans_max_age_days": 30,
    "skills_skip_max_age_days": 90
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_MAINTENANCE_ENABLED` | `true` | Run the janitor. Set false to disable all storage maintenance. |
| `interval_minutes` | `ODEK_MAINTENANCE_INTERVAL_MINUTES` | `60` | Minutes between sweeps. The first sweep runs after one interval, never at startup. |
| `sessions_max_age_days` | `ODEK_MAINTENANCE_SESSIONS_MAX_AGE_DAYS` | `30` | Delete sessions (and their index/vector-index entries) older than this. `0` = keep forever. |
| `audit_max_age_days` | `ODEK_MAINTENANCE_AUDIT_MAX_AGE_DAYS` | `14` | Delete `~/.odek/sessions/audit/*.json` records older than this. `0` = keep forever. |
| `log_max_mb` | `ODEK_MAINTENANCE_LOG_MAX_MB` | `50` | Rotate `~/.odek/telegram.log` and `~/.odek/schedule.log` larger than this: current log becomes `<name>.1` (one backup generation) and a fresh empty log is started. `0` = no rotation. |
| `plans_max_age_days` | `ODEK_MAINTENANCE_PLANS_MAX_AGE_DAYS` | `30` | Delete Telegram plan files (`~/.odek/plans/**/*.md`) older than this; emptied chat directories are removed. `0` = keep forever. |
| `skills_skip_max_age_days` | `ODEK_MAINTENANCE_SKILLS_SKIP_MAX_AGE_DAYS` | `90` | Remove skill skip-list entries (`~/.odek/skills/.skipped.json`) older than this. `0` = keep forever. |

Downloaded Telegram media (`~/.odek/media/`, including per-chat `chat<id>/`
subdirectories) is always swept after 1 hour; that policy is not configurable.

The `maintenance` section is **operator-only**: it governs deletion of user
data, so the project-level `./odek.json` cannot set it (a `maintenance`
section there is ignored with a stderr warning). Configure it via
`~/.odek/config.json` or the `ODEK_MAINTENANCE_*` environment variables.

## Tool configuration

Control which tools are exposed to the LLM. Use this to deploy locked-down
agents — for example, a chatbot with only `web_search`, `transcribe`, and
`vision`, or a read-only research assistant with no write tools.

```json
{
  "tools": {
    "enabled": ["web_search", "transcribe", "vision"],
    "disabled": ["shell", "write_file", "patch"]
  }
}
```

| Field | Env | Default | Description |
|---|---|---|---|
| `enabled` | `ODEK_TOOLS_ENABLED` | unset | Whitelist. When set, only these tools are registered. Comma-separated in env. |
| `disabled` | `ODEK_TOOLS_DISABLED` | unset | Blacklist. These tools are removed from the default set. Comma-separated in env. |

CLI flags override file and env config:

```bash
# Whitelist mode: only these tools
odek run --tool web_search --tool vision "what's new in Go?"

# Blacklist mode: remove specific tools
odek run --no-tool shell --no-tool write_file "review this code"

# Environment
ODEK_TOOLS_ENABLED=web_search,vision odek run "search and summarize"
```

Resolution rules:

- `enabled` is set by the highest-priority layer that provides it.
- `disabled` is merged across layers.
- If both are present: start from `enabled`, then subtract `disabled`.
- Unknown tool names are silently ignored.
- The `memory` tool is also subject to this filter, so a whitelist must
  include `"memory"` if you want persistent memory.

Project-level `./odek.json` **cannot enable tools** — it may only append to
`disabled`. This prevents a malicious repository from widening the tool
surface.

## Tool Progress

Controls how per-tool progress messages appear inside the Telegram bot during agent runs. Independent from `interaction_mode` — you can have engaging terminal output with minimal Telegram progress, or verbose terminal with rich progress bubbles.

```json
{
  "tool_progress": "all",
  "tool_progress_cleanup": true
}
```

### `tool_progress`

| Value | Behavior | Use case |
|-------|----------|----------|
| `"all"` (default) | Single editable progress bubble with smart previews — e.g. `📝 read_file: "main.go"`. Includes edit throttling (1.5s), tool dedup (`×N` counter for repeated same-tool), and automatic flood-control fallback | General use — shows what the agent is doing without spamming the chat |
| `"new"` | Same as `"all"` but only updates when the tool name changes. Consecutive `read_file` calls produce one line; a `shell` call starts a new line | Long-running agents with repetitive tool chains (e.g. reading 50 files in batch) |
| `"verbose"` | Raw tool arguments as separate messages. Each tool call sends a new message with full JSON args; on completion the result is sent as a new message `✅ (12ms, 2KB)` including execution latency and result size | Debugging — see exactly what the agent passes to each tool and how long it takes |
| `"off"` | No per-tool progress messages at all. Only the initial "🤔 Looking into that..." and final answer are shown | Privacy-sensitive contexts or users who prefer zero noise |

### `tool_progress_cleanup`

Default: `true`. Controls whether the progress message bubble is deleted after the agent's final answer arrives:
- `true` — delete the progress bubble (clean chat, no stale tool traces)
- `false` — keep the progress bubble as a breadcrumb of what the agent did

### How it works

The progress system is an evolving single message that gets edited in-place (similar to an animated status). Each tool call adds a line like:

```
📝 read_file: "main.go"
💻 shell: "npm test"
📝 read_file: "utils.go" (×3)
```

Key behaviors:
- **Smart previews** — instead of showing raw JSON args, the system extracts meaningful context: filename for file tools, the command text for shell, URL for browser, query text for memory/search tools, audio filename for transcribe, file path for vision, query for web_search
- **Edit throttling** — edits are rate-limited to one every 1.5 seconds to avoid hitting Telegram's flood control limits. Rapid tool chains don't produce 429 errors
- **Tool dedup** — when the same tool runs consecutively (common with parallel batch tools like `batch_read`), identical lines are collapsed into a `(×N)` counter instead of repeating N times
- **Flood control fallback** — if an edit message fails with "flood" or "retry after", the system automatically switches to sending new messages instead of editing. This prevents the bot from becoming unresponsive under heavy load
- **Content reset** — when the agent calls `send_message` mid-run to send an interim message, the progress bubble resets below that content, keeping the chat timeline in correct order

## odek init

Create a config file template:

```bash
# Local project config (./odek.json)
odek init

# Global config (~/.odek/config.json)
odek init --global

# Overwrite existing file
odek init --force
```

## Quick examples

```bash
# Set API key via secrets.env (recommended — keeps secrets out of config files)
echo 'ODEK_API_KEY="sk-..."' >> ~/.odek/secrets.env
chmod 600 ~/.odek/secrets.env

# Global config (model and other settings only, no secrets)
echo '{"model": "deepseek-v4-flash"}' > ~/.odek/config.json
odek run "list files"

# Per-project override
echo '{"max_iterations": 30}' > ./odek.json
odek run "quick status"

# Project-level sandbox knobs require explicit approval (or the CI bypass)
# because they can read host env vars and pick arbitrary images/networks.
echo '{"sandbox": true, "sandbox_env": {"X": "${HOME}"}}' > ./odek.json
ODEK_APPROVE_PROJECT_SANDBOX=1 odek run "run untrusted script"

# Env var override for one-off
ODEK_SANDBOX=true odek run "run untrusted script"

# Enable skill learning via env var
ODEK_SKILLS_LEARN=true odek run "set up CI"

# Enable Extended Memory via CLI flag
odek run --memory-extended-enabled "remember that I prefer Go over Python"

# Or configure it globally in ~/.odek/config.json (memory cannot be set in ./odek.json)
# { "memory": { "extended": { "enabled": true } } }

# Sub-agent config (project-level)
echo '{"subagent": {"max_concurrency": 5, "timeout_seconds": 300}}' > ./odek.json

# CLI flag always wins
odek run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```
