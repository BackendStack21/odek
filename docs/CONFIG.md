     1|# Configuration
     2|
     3|kode uses a **layered configuration system** with convention over configuration — opt-in files and environment variables, no mandatory setup.
     4|
     5|## Priority chain
     6|
     7|Each layer overrides the one below it. Unset fields inherit from the layer below:
     8|
     9|```
    10|1.  ~/kode/config.json    ← Global defaults (shared across projects)
    11|2.  ./kode.json           ← Project-specific overrides
    12|3.  KODE_* env vars       ← Runtime/environment overrides
    13|4.  CLI flags             ← Explicit invocation (highest priority)
    14|```
    15|
    16|## Config files
    17|
    18|### Global defaults (`~/kode/config.json`)
    19|
    20|Shared across all projects:
    21|
    22|```json
    23|{
    24|  "model": "deepseek-v4-flash",
    25|  "base_url": "https://api.deepseek.com/v1",
    26|  "api_key": "${DEEPSEEK_API_KEY}",
    27|  "thinking": "",
    28|  "max_iterations": 90,
    29|  "sandbox": false,
    30|  "no_color": false,
    31|  "no_agents": false,
    32|  "system": ""
    33|}
    34|```
    35|
    36|### Project overrides (`./kode.json`)
    37|
    38|Same schema as global. Only set the fields you want to override:
    39|
    40|```json
    41|{
    42|  "model": "gpt-4o",
    43|  "base_url": "https://api.openai.com/v1",
    44|  "max_iterations": 30
    45|}
    46|```
    47|
    48|Both files are optional. Missing files are silently ignored. String values support `${VAR}` environment variable substitution — useful for API keys without plaintext storage.
    49|
    50|## Environment variables
    51|
    52|Every config knob has a `KODE_*` counterpart:
    53|
    54|| Variable | Maps to | Type |
    55||----------|---------|------|
    56|| `KODE_MODEL` | `--model` | string |
    57|| `KODE_BASE_URL` | `--base-url` | string |
    58|| `KODE_API_KEY` | config files only | string |
    59|| `KODE_THINKING` | `--thinking` | string |
    60|| `KODE_MAX_ITER` | `--max-iter` | int |
    61|| `KODE_SANDBOX` | `--sandbox` | bool |
    62|| `KODE_NO_COLOR` | `--no-color` | bool |
    63|| `KODE_NO_AGENTS` | `--no-agents` | bool |
    64|| `KODE_SYSTEM` | `--system` | string |
    65|| `KODE_SKILLS_LEARN` | `skills.learn` | bool |
    66|| `KODE_SANDBOX_IMAGE` | `--sandbox-image` | string |
    67|| `KODE_SANDBOX_NETWORK` | `--sandbox-network` | string |
    68|| `KODE_SANDBOX_READONLY` | `--sandbox-readonly` | bool |
    69|| `KODE_SANDBOX_MEMORY` | `--sandbox-memory` | string |
    70|| `KODE_SANDBOX_CPUS` | `--sandbox-cpus` | string |
    71|| `KODE_SANDBOX_USER` | `--sandbox-user` | string |
    72|
    73|## API key fallback order
    74|
    75|`KODE_API_KEY` → `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`
    76|
    77|## Skills configuration
    78|
    79|The `skills` section controls the skill system:
    80|
    81|```json
    82|{
    83|  "skills": {
    84|    "max_auto_load": 3,
    85|    "max_lazy_slots": 5,
    86|    "learn": true,
    87|    "llm_learn": true,
    88|    "llm_curate": true,
    89|    "import": {
    90|      "max_size_bytes": 1048576,
    91|      "timeout_seconds": 5,
    92|      "require_https": false
    93|    },
    94|    "curation": {
    95|      "staleness_days": 90,
    96|      "auto_prune": false
    97|    }
    98|  }
    99|}
   100|```
   101|
   102|| Field | Env var | Default | Description |
   103||-------|---------|---------|-------------|
   104|| `max_auto_load` | — | 3 | Max skills injected into system prompt on start |
| `max_lazy_slots` | — | 5 | Max skills loaded per user input via trigger matching |
| `learn` | `KODE_SKILLS_LEARN` | `true` | Enable skill learning mode (detects patterns, suggests skills). On by default |
| `llm_learn` | — | `true` | Use LLM to enrich detected patterns with better names, descriptions, and structured content |
| `llm_curate` | — | `true` | Use LLM for curation quality assessment and improvement suggestions |
   105|| `dirs` | — | [] | Extra skill directories beyond `~/.kode/skills` and `./.kode/skills` |
   106|| `import.max_size_bytes` | — | 1048576 (1MB) | Max size for fetched skill content |
   107|| `import.timeout_seconds` | — | 5 | HTTP timeout for skill URI fetch |
   108|| `import.require_https` | — | false | Reject http:// URIs when true |
   109|| `curation.staleness_days` | — | 90 | Days without use before flagging as stale |
   110|| `curation.auto_prune` | — | false | Auto-delete stale skills on curate (no prompt) |
   111|
   112|## Memory configuration
   113|
   114|The `memory` section controls the persistent memory system (see [docs/MEMORY.md](docs/MEMORY.md)):
   115|
   116|```json
   117|{
   118|  "memory": {
   119|    "enabled": true,
   120|    "facts_limit_user": 1500,
   121|    "facts_limit_env": 2500,
   122|    "buffer_lines": 20,
   123|    "buffer_enabled": true,
   124|    "merge_on_write": true,
   125|    "extract_on_end": true,
   126|    "llm_search": true,
   127|    "llm_extract": true,
   128|    "llm_consolidate": true,
   129|    "merge_threshold": 0.7,
   130|    "add_threshold": 0.3
   131|  }
   132|}
   133|```
   134|
   135|| Field | Default | Description |
   136||-------|---------|-------------|
   137|| `enabled` | true | Enable memory system entirely |
   138|| `facts_limit_user` | 1500 | Max chars for `user.md` fact file |
   139|| `facts_limit_env` | 2500 | Max chars for `env.md` fact file |
   140|| `buffer_lines` | 20 | Max turn summaries in session buffer |
   141|| `buffer_enabled` | true | Enable the turn-level buffer |
   142|| `merge_on_write` | true | Use go-vector RP similarity to auto-merge related entries |
   143|| `extract_on_end` | true | Extract durable facts via LLM at session end (≥3 turns) |
   144|| `llm_search` | true | Use LLM to rank episode search results by relevance |
   145|| `llm_extract` | true | Use LLM for end-of-session fact extraction |
   146|| `llm_consolidate` | true | Use LLM to merge related fact entries |
   147|| `merge_threshold` | 0.7 | go-vector cosine threshold for auto-merge (0.0–1.0) |
   148|| `add_threshold` | 0.3 | go-vector cosine threshold for auto-add (0.0–1.0) |
   149|
   150|## Sub-agent configuration
   151|
   152|The `subagent` section controls task decomposition and parallel sub-agent execution (see [docs/SUBAGENTS.md](docs/SUBAGENTS.md)):
   153|
   154|```json
   155|{
   156|  "subagent": {
   157|    "max_concurrency": 3,
   158|    "timeout_seconds": 120,
   159|    "max_iterations": 15
   160|  }
   161|}
   162|```
   163|
   164|| Field | Default | Description |
   165||-------|---------|-------------|
   166|| `max_concurrency` | 3 | Max sub-agents running in parallel (max 8) |
   167|| `timeout_seconds` | 120 | Default timeout per sub-agent (overridden by `--timeout`) |
   168|| `max_iterations` | 15 | Default max think→act cycles per sub-agent (overridden by `--max-iter`) |
   169|
   170|This section is optional. Omitted fields inherit sensible defaults.
   171|
   172|## kode init
   173|
   174|Create a config file template:
   175|
   176|```bash
   177|# Local project config (./kode.json)
   178|kode init
   179|
   180|# Global config (~/kode/config.json)
   181|kode init --global
   182|
   183|# Overwrite existing file
   184|kode init --force
   185|```
   186|
   187|## Quick examples
   188|
   189|```bash
   190|# Global config
   191|echo '{"api_key": "${DEEPSEEK_API_KEY}", "model": "deepseek-v4-flash"}' > ~/kode/config.json
   192|kode run "list files"
   193|
   194|# Per-project override
   195|echo '{"max_iterations": 30}' > ./kode.json
   196|kode run "quick status"
   197|
   198|# Env var override for one-off
   199|KODE_SANDBOX=true kode run "run untrusted script"
   200|
   201|# Enable skill learning via env var
   202|KODE_SKILLS_LEARN=true kode run "set up CI"
   203|
   204|# Sub-agent config (project-level)
   205|echo '{"subagent": {"max_concurrency": 5, "timeout_seconds": 300}}' > ./kode.json
   206|
   207|# CLI flag always wins
   208|kode run --model gpt-4o --base-url https://api.openai.com/v1 "task"
   209|```
   210|