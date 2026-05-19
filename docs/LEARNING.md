# Self-Learning System

kode can **learn reusable skills** from your agent sessions. Learning is **on by default** — every `kode run` automatically scans the conversation for reusable patterns. Use `--no-learn` to disable.

## Architecture

```
kode run --learn "set up CI with GitHub Actions"
         │
         ▼
   ┌─────────────┐
   │ Agent Loop  │  Think → Act → Think → Act → ... → Final answer
   └──────┬──────┘
          │ allMessages (full transcript)
          ▼
   ┌──────────────┐
   │ runLearnLoop │  Converts messages, extracts user input
   └──────┬───────┘
          │ userMessages + toolCalls
          ▼
   ┌────────────────────┐
   │ RunAllHeuristics   │  5 heuristics run in parallel
   │                    │
   │ 1. multi-step      │  Detects 4+ sequential terminal calls
   │ 2. error-recovery  │  Detects fail → fix → succeed patterns
   │ 3. user-correction │  Detects "no, try this instead" corrections
   │ 4. repeated-action │  Detects same command run ≥2 times
   │ 5. explicit-instr  │  Detects "save this as a skill" requests
   └──────┬─────────────┘
          │ []SkillSuggestion (deduplicated, max 1 per heuristic)
          ▼
   ┌──────────────────┐
   │ Display & Prompt │  Shows each suggestion, asks Save as skill? [Y/n]
   └──────┬───────────┘
          │ user confirms (y/yes)
          ▼
   ┌──────────────────┐
   │ SaveSuggestion   │  Writes SKILL.md to ~/.kode/skills/<name>/
   │ SkillManager     │  Reloads to pick up new skill immediately
   │   .Reload()      │
   └──────────────────┘
```

**Key design decision:** The learn loop is **purely heuristic** — zero LLM calls. It runs after the agent completes, adding no latency or cost to the agent run itself.

## CLI Usage

```bash
# Learning is on by default — no flag needed
kode run "Set up a Docker-based CI pipeline"

# Disable learning for this run
kode run --no-learn "Quick status check"

# With a specific model
kode run --model deepseek-v4-pro "Refactor auth module"

# In sandbox (learning works inside containers too)
kode run --sandbox "Install and configure nginx"
```

After the agent finishes, kode scans the transcript and prints any detected patterns:

```
🔍 Learning: detected 2 skill pattern(s)

📝 Skill suggestion: procedure-docker
   Multi-step procedure: docker (4 steps)
   Detected by: multi-step
   Commands:
     • docker build -t app .
     • docker tag app registry.example.com/app
     • docker push registry.example.com/app
     • kubectl rollout restart deployment/app
   Save as skill? [Y/n]:
```

Type `y` (or just press Enter) to save, `n` to skip.

## The Five Heuristics

Each heuristic detects one class of reusable pattern. Max **one suggestion per heuristic** — if multiple matches exist, the first one wins.

### 1. Multi-Step Procedure (`multi-step`)

Detects **4 or more sequential successful terminal calls**. Failed commands break the sequence, but a new sequence can start after the failure.

**What triggers it:**
- 4+ consecutive `shell` tool calls that all succeeded (no `error:` in output)
- Non-terminal tools (read_file, write_file, etc.) are skipped — they don't count but don't break the sequence

**Example:**
```bash
git clone https://github.com/example/repo.git   # step 1
cd repo                                          # step 2
npm install                                      # step 3
npm test                                         # step 4
```
→ Suggestion: `procedure-git` with these 4 commands as a skill.

**Output format:**
```markdown
## procedure-git

Multi-step procedure detected during a kode session.

### Commands
1. git clone https://github.com/example/repo.git
2. cd repo
3. npm install
4. npm test
```

### 2. Error Recovery (`error-recovery`)

Detects the pattern: **fail → fix → succeed**. When a command fails, the agent tries again with a corrected command, and the fix works.

**What triggers it:**
- A terminal call fails (output contains `error:`)
- The next terminal call succeeds
- A word-level diff between the failing and succeeding commands identifies what changed

**Example:**
```
# Failed:
pip install request
→ Error: No matching distribution

# Fixed:
pip install requests
→ Successfully installed requests-2.31.0
```
→ Suggestion: `error-pip` documenting the fix (`request` → `requests`).

**Generated body includes the diff:**
```markdown
### Error Fix
| Before | After |
|--------|-------|
| pip install request | pip install requests |
```

### 3. User Correction (`user-correction`)

Detects when **you** corrected the agent and the correction produced a working result. The agent tried something wrong, you said "no, try X instead", and the new approach worked.

**What triggers it:**
- A user message contains correction keywords: `no`, `instead`, `try`, `actually`, `wrong`, `not what`, `different`
- After the correction, there's a successful terminal command pair

**Example:**
```
Agent: Runs npm run build → fails
You:   "no, try npm run build:prod instead"
Agent: Runs npm run build:prod → succeeds
```
→ Suggestion: `user-correction-build` capturing the corrected command.

### 4. Repeated Action (`repeated-action`)

Detects the **same command run 2 or more times** (in sessions with 6+ terminal calls). Commands are normalized before comparison — paths, flag values, and version numbers are stripped.

**What triggers it:**
- Session has ≥6 terminal calls
- Same normalized command appears ≥2 times

**Normalization examples:**
| Original | Normalized |
|----------|-----------|
| `go test ./pkg/auth/...` | `go test` |
| `go test ./pkg/db/...` | `go test` |
| `docker build -t myapp:v1 .` | `docker build` |
| `curl -H "Auth: xxx" https://api.example.com/data` | `curl <url>` |

**Example:**
```
Agent runs "go test ./..." 3 times across different iterations
```
→ Suggestion: `repeated-go-test` describing this as a repeatable workflow.

### 5. Explicit Instruction (`explicit-instruction`)

Detects when you **explicitly ask** kode to save something as a skill.

**What triggers it:**
- User message contains one of these phrases:
  - `save this`
  - `add a skill`
  - `remember this`
  - `create skill about`
  - `save as skill`
  - `make a skill`

**Example:**
```
You: "save this docker-compose setup as docker-dev"
```
→ Suggestion: `docker-dev` with the surrounding terminal commands as context.

## Generated Skill Format

Saved skills follow the standard SKILL.md format:

```yaml
---
name: procedure-docker
description: Multi-step procedure: docker (4 steps)
version: 1.0.0
author: kode
kode:
  trigger:
    topic: docker
    action: clone tag push
  auto_load: false
  quality: draft
---
## procedure-docker

Multi-step procedure detected during a kode session.

### Commands
1. docker build -t app .
2. docker tag app registry.example.com/app
3. docker push registry.example.com/app
4. kubectl rollout restart deployment/app
```

- **quality: draft** — always, since these are auto-detected (not curated)
- **auto_load: false** — skills start as lazy (loaded when triggers match), not injected into every session
- **trigger keywords** — derived automatically from the command log (topic = first meaningful word, action = extracted verbs)
- **author: kode** — distinguishes auto-generated skills from manually authored ones

## Examples

### Basic: Let kode learn from a session

```bash
kode run "Set up PostgreSQL with Docker"
```

After the agent completes:
```
🔍 Learning: detected 1 skill pattern(s)

📝 Skill suggestion: procedure-docker
   Multi-step procedure: docker (4 steps)
   Detected by: multi-step
   Commands:
     • docker pull postgres:16-alpine
     • docker run -d --name pg -e POSTGRES_PASSWORD=secret postgres:16-alpine
     • docker exec pg psql -U postgres -c "CREATE DATABASE myapp"
     • docker exec pg psql -U postgres -c "SELECT 1"
   Save as skill? [Y/n]: y
   ✓ Saved skill "procedure-docker"
```

Now the skill is available for future runs:
```bash
kode skill list
# procedure-docker  Multi-step procedure: docker  draft
```

### Multiple suggestions (accept some, reject others)

```bash
kode run "Build and deploy the app"
```

```
🔍 Learning: detected 3 skill pattern(s)

📝 Skill suggestion: procedure-docker
   ...
   Save as skill? [Y/n]: y
   ✓ Saved skill "procedure-docker"

📝 Skill suggestion: error-npm
   ...
   Save as skill? [Y/n]: n
   Skipped.

📝 Skill suggestion: repeated-go-test
   ...
   Save as skill? [Y/n]: y
   ✓ Saved skill "repeated-go-test"
```

## Limitations

- **Purely heuristic** — no LLM assessment. The system detects patterns mechanically; it doesn't judge skill quality.
- **Max 1 per heuristic** — if an agent session has 10 multi-step sequences, only the first is suggested.
- **Max 5 suggestions total** — one per heuristic type.
- **No content deduplication** — if you already have a similar skill, the new one is still suggested. Run `kode skill curate` periodically to detect overlaps.
- **Command-only** — the heuristics work on terminal (`shell`) tool calls. Other tools (read_file, write_file) are visible in the transcript but aren't analyzed for patterns.
- **No REPL integration** — learning currently only works with `kode run`, not in REPL mode.

## Related

- [Skills System](#) — how skills are loaded and used during agent runs
- [CLI Reference](CLI.md) — all CLI flags including `--learn`
