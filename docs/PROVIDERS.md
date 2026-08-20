# Providers & Models

odek is provider-agnostic. Any endpoint that speaks the OpenAI `/chat/completions` protocol works.

## Deepseek

```bash
export ODEK_API_KEY=sk-...
# Or use DEEPSEEK_API_KEY (fallback)
odek run --model deepseek-v4-flash "task"
```

## OpenAI

```bash
export ODEK_API_KEY=sk-...
# Or use OPENAI_API_KEY (final fallback)
odek run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```

## Z.ai (GLM)

```bash
export ODEK_API_KEY=<z.ai api key>
export ODEK_BASE_URL=https://api.z.ai/api/paas/v4
odek run --model glm-5.3 "task"
```

Notes:

- **Thinking control** — GLM models accept a `thinking` object (`{"type": "enabled"|"disabled"}`). odek maps its `--thinking` levels onto it: `low`/`high`/`max` become `thinking: enabled` plus `reasoning_effort` of the same name; `medium` maps to `high` (GLM has no medium level).
- **GLM-5.3 forces thinking** — per z.ai's platform-API docs, `thinking: disabled` fails requests on GLM-5.3, so odek maps `--thinking disabled` to the documented migration form (`enabled` + `reasoning_effort: low`) instead of risking the failure. (The coding-plan endpoint currently accepts and honors `disabled` even for 5.3; the mapping is kept as the safe behavior on both.) GLM-5.2 and GLM-5-Turbo accept `disabled` normally, and `reasoning_effort` was validated against all three models.
- **Billing errors fail fast** — an empty balance comes back as HTTP 429 (`Insufficient balance or no resource package`, code 1113). odek detects billing/quota 429s and reports them immediately instead of retrying into an opaque `context deadline exceeded`.
- Coding Plan subscribers should use the coding endpoint `https://api.z.ai/api/coding/paas/v4` as `ODEK_BASE_URL` instead.

## Custom / self-hosted

Any endpoint that accepts `POST /chat/completions` with an OpenAI-compatible JSON body works — Ollama, vLLM, LiteLLM, etc. No provider-specific code in odek.

```bash
export ODEK_API_KEY=not-needed
odek run --model llama3 --base-url http://localhost:11434/v1 "task"
```

---

## Model Profiles

odek ships with built-in **model profiles** that automatically apply sensible defaults based on the model name. Profiles are matched by longest prefix.

| Model | Family | Default Thinking | Timeout | Max Context | Best For |
|-------|--------|-----------------|---------|-------------|----------|
| `deepseek-chat` | DeepSeek (legacy) | (provider default) | 120s | 128K | General |
| `deepseek-v4-flash` | DeepSeek v4 Flash | — (faster/cheaper) | 90s | 128K | Quick tasks, coding |
| `deepseek-v4-pro` | DeepSeek v4 Pro | `enabled` | 180s | **1M** | Deep reasoning |
| `glm-5.3` | GLM 5.3 (Z.ai) | (always on — forced) | 300s | **1M** | Agentic coding |
| `glm-5.2` | GLM 5.2 (Z.ai) | (provider default) | 300s | **1M** | Agentic coding |
| `glm-5-turbo` | GLM 5 Turbo (Z.ai) | (provider default) | 180s | 200K | Tool-heavy agents |
| `glm-…` (other) | GLM (Z.ai) | (provider default) | 180s | 128K | General |
| `kimi-…` (e.g. `kimi-for-coding`) | Kimi | (provider default) | 300s | 256K | Agentic coding |
| `k3` | Kimi | (provider default) | 300s | **1M** | Agentic coding |
| `k3-256k` | Kimi | (provider default) | 300s | 256K | Agentic coding |
| *(any other)* | Generic | (profile default) | 120s | (no limit) | Custom models |

### How profiles work

1. Set `--model deepseek-v4-pro` → odek auto-configures `thinking=enabled` + `180s timeout` + 1M context
2. Explicit `--thinking` always wins over profile defaults
3. Unknown models get no profile overrides (provider default behavior)

### Adding a profile

Profiles live in `odek.go` as the `KnownProfiles` slice:

```go
{
    Prefix: "claude-sonnet-4",
    Profile: ModelProfile{
        Label:           "Claude Sonnet 4",
        DefaultThinking: "",
        Timeout:         180,
        MaxContext:      200_000,
    },
},
```

No changes to the LLM client, loop, or CLI parsing needed.

### Examples

```bash
# DeepSeek v4 Pro — thinking enabled, 180s timeout, 1M context
odek run --model deepseek-v4-pro "Design a distributed consensus algorithm"

# DeepSeek v4 Flash — no thinking, 90s timeout, 128K
odek run --model deepseek-v4-flash "List the files"

# Override profile default
odek run --model deepseek-v4-pro --thinking disabled "Quick status check"
```

---

## Thinking Levels

The `--thinking` flag controls reasoning depth. odek auto-maps to the provider's native format.

| Value | Deepseek sends | OpenAI o-series sends |
|-------|---------------|----------------------|
| `enabled` | `{"thinking": {"type": "enabled"}}` | — |
| `disabled` | `{"thinking": {"type": "disabled"}}` | — |
| `low` | — | `{"reasoning_effort": "low"}` |
| `medium` | — | `{"reasoning_effort": "medium"}` |
| `high` | — | `{"reasoning_effort": "high"}` |
| (empty) | (not sent) | Provider default |

```bash
# DeepSeek v4 Pro — profile auto-enables thinking
odek run --model deepseek-v4-pro "Explain monads"

# OpenAI o1 — deep reasoning
odek run --model o1 --base-url https://api.openai.com/v1 --thinking high "Optimize this algorithm"
```

---

## Context Window Management

odek automatically trims conversation history to stay within each model's context window.

### How it works

1. **Token estimation**: Conservative heuristic (~4 chars/token + structural overhead) — no tokenizer dependency
2. **Safety margin**: 75% of available context for input; 25% reserved for output
3. **Trim strategy**: Before each LLM call, if estimated tokens exceed budget, oldest non-essential pairs (tool call→result) are dropped — system prompt and original task are always preserved
4. **No limit = no trimming**: Models with `MaxContext: 0` have no enforcement

### Example

```
Before trim (6 msgs, ~250K estimated, budget=200K):
  [system] You are odek...
  [user]   Refactor this module...
  [assistant]"                       ← DROPPED
  [tool]                              ← DROPPED
  [assistant] Let me check...         ← KEPT
  [tool]  File: main.go...            ← KEPT

After trim (4 msgs, ~180K estimated):
  [system] You are odek...
  [user]   Refactor this module...
  [assistant] Let me check...
  [tool]  File: main.go...
```
