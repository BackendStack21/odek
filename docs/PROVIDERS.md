# Providers & Models

kode is provider-agnostic. Any endpoint that speaks the OpenAI `/chat/completions` protocol works.

## Deepseek

```bash
export DEEPSEEK_API_KEY=sk-...
kode run --model deepseek-chat "task"
```

## OpenAI

```bash
export OPENAI_API_KEY=sk-...
kode run --model gpt-4o --base-url https://api.openai.com/v1 "task"
```

## Custom / self-hosted

Any endpoint that accepts `POST /chat/completions` with an OpenAI-compatible JSON body works — Ollama, vLLM, LiteLLM, etc. No provider-specific code in kode.

```bash
export OPENAI_API_KEY=not-needed
kode run --model llama3 --base-url http://localhost:11434/v1 "task"
```

---

## Model Profiles

kode ships with built-in **model profiles** that automatically apply sensible defaults based on the model name. Profiles are matched by longest prefix.

| Model | Family | Default Thinking | Timeout | Max Context | Best For |
|-------|--------|-----------------|---------|-------------|----------|
| `deepseek-chat` | DeepSeek (legacy) | (provider default) | 120s | 128K | General |
| `deepseek-v4-flash` | DeepSeek v4 Flash | — (faster/cheaper) | 90s | 128K | Quick tasks, coding |
| `deepseek-v4-pro` | DeepSeek v4 Pro | `enabled` | 180s | **1M** | Deep reasoning |
| *(any other)* | Generic | (profile default) | 120s | (no limit) | Custom models |

### How profiles work

1. Set `--model deepseek-v4-pro` → kode auto-configures `thinking=enabled` + `180s timeout` + 1M context
2. Explicit `--thinking` always wins over profile defaults
3. Unknown models get no profile overrides (provider default behavior)

### Adding a profile

Profiles live in `kode.go` as the `KnownProfiles` slice:

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
kode run --model deepseek-v4-pro "Design a distributed consensus algorithm"

# DeepSeek v4 Flash — no thinking, 90s timeout, 128K
kode run --model deepseek-v4-flash "List the files"

# Override profile default
kode run --model deepseek-v4-pro --thinking disabled "Quick status check"
```

---

## Thinking Levels

The `--thinking` flag controls reasoning depth. kode auto-maps to the provider's native format.

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
kode run --model deepseek-v4-pro "Explain monads"

# OpenAI o1 — deep reasoning
kode run --model o1 --base-url https://api.openai.com/v1 --thinking high "Optimize this algorithm"
```

---

## Context Window Management

kode automatically trims conversation history to stay within each model's context window.

### How it works

1. **Token estimation**: Conservative heuristic (~4 chars/token + structural overhead) — no tokenizer dependency
2. **Safety margin**: 75% of available context for input; 25% reserved for output
3. **Trim strategy**: Before each LLM call, if estimated tokens exceed budget, oldest non-essential pairs (tool call→result) are dropped — system prompt and original task are always preserved
4. **No limit = no trimming**: Models with `MaxContext: 0` have no enforcement

### Example

```
Before trim (6 msgs, ~250K estimated, budget=200K):
  [system] You are kode...
  [user]   Refactor this module...
  [assistant]"                       ← DROPPED
  [tool]                              ← DROPPED
  [assistant] Let me check...         ← KEPT
  [tool]  File: main.go...            ← KEPT

After trim (4 msgs, ~180K estimated):
  [system] You are kode...
  [user]   Refactor this module...
  [assistant] Let me check...
  [tool]  File: main.go...
```
