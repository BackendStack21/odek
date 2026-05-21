# Self-Learning System

odek can **learn reusable skills** from your agent sessions. Learning is **on by default** — every `odek run` automatically scans the conversation for reusable patterns, auto-saves quality suggestions, and runs micro-curation to keep skills healthy. Use `--no-learn` to disable.

## Architecture

```
odek run --learn "set up CI with GitHub Actions"
         │
         ▼
   ┌─────────────┐
   │ Agent Loop  │  Think → Act → Think → Act → ... → Final answer
   └──────┬──────┘
          │ allMessages (full transcript)
          ▼
   ┌──────────────────┐
   │ learnAndSuggest  │  Converts messages, extracts user input
   └──────┬───────────┘
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
          │ []SkillSuggestion
          ▼
   ┌──────────────────┐
   │ LLM Enhancement  │  (optional) LLM enriches name, description, body
   └──────┬───────────┘
          │
          ▼
   ┌──────────────────────┐
   │ FilterSkipped         │  Suppress previously-skipped suggestions
   └──────┬───────────────┘
          │
          ▼
   ┌──────────────────────┐
   │ AutoSaveSuggestions  │  Auto-save if enabled + quality gate passes
   │  OR                  │  OR show interactive preview + prompt
   │  Interactive prompt  │
   └──────┬───────────────┘
          │ saved skills
          ▼
   ┌──────────────────┐
   │ RunAutoCurate    │  Merge overlaps, delete duplicates,
   │                  │  prune stale, delete skip-threshold skills
   └──────────────────┘
```

**Key design decisions:**
- **Auto-save is default** — quality suggestions are saved automatically (no prompt). Set `auto_save.enabled: false` to require manual approval.
- **LLM enhancement is optional** — when enabled, the LLM enriches heuristic output with better names, structured bodies, and accurate keywords.
- **One skip = permanent suppression** — skip a suggestion once and it won't appear again. Use `odek skill reset-skips` to re-enable.
- **Auto-curation runs silently** — after every session where skills were saved, overlaps are merged, duplicates removed, and stale skills pruned.

## CLI Usage

```bash
# Learning is on by default — no flag needed
odek run "Set up a Docker-based CI pipeline"

# Disable learning for this run
odek run --no-learn "Quick status check"

# Disable auto-save (require manual approval for each suggestion)
# Set in odek.json: {"skills": {"auto_save": {"enabled": false}}}

# Reset skipped suggestions (re-enable suppressed patterns)
odek skill reset-skips
odek skill reset-skips procedure-git

# Run manual curation
odek skill curate --apply
```

**Auto-save (default):** Quality suggestions are saved silently after each session:

```
   ✓ Auto-saved skill "procedure-docker" (multi-step)
   ✓ Auto-saved skill "error-pip" (error-recovery)

🔧 Micro-curation: merged procedure-docker + docker-deploy
   overlapping skills share 2 keywords: docker, container
```

**Interactive mode** (when `auto_save.enabled: false`): suggestions show a body preview and prompt for confirmation:

```
🔍 Learning: detected 1 skill pattern(s)

📝 Skill suggestion: procedure-docker
   Multi-step procedure: docker (4 steps)
   Detected by: multi-step
   Commands:
     • docker build -t app .
     • docker tag app registry.example.com/app
     • docker push registry.example.com/app
     • kubectl rollout restart deployment/app
   ── Preview ──
   ## Overview
   Procedure for: docker
   ## Step-by-Step
   1. docker build -t app .
   2. docker tag app registry.example.com/app
   3. docker push registry.example.com/app
   4. kubectl rollout restart deployment/app
   ## Common Pitfalls
   - Verify each step's output before proceeding
   - Exit code 0 means success

   Save as skill? [Y/n/s=skip always]:
```

Type `y` (or Enter) to save, `n` to skip (temporarily), `s` to skip permanently.

### Skip Persistence

Skip decisions are persisted to `~/.odek/skills/.skipped.json`. A suggestion skipped once is permanently suppressed (default `skip_threshold: 1`). After `skip_reset_days` (default 30), skips expire and suggestions re-appear.

```bash
# View skip list
cat ~/.odek/skills/.skipped.json

# Clear all skips
odek skill reset-skips

# Clear a specific skill
odek skill reset-skips procedure-grep
```

### Auto-Curation

After each session where skills were saved, micro-curation runs automatically:

| Action | Trigger |
|--------|---------|
| **Merge** | Two draft-quality skills share ≥2 topic keywords |
| **Dedup** | Two skills have identical body hash |
| **Skip-delete** | Skill has been skipped ≥ `skip_threshold` times |
| **Stale-prune** | Skill unused ≥ `staleness_days` (requires `auto_prune: true`) |

Merged skills combine trigger keywords and body content. The older skill (alphabetically) is kept; the newer is deleted.

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

Multi-step procedure detected during a odek session.

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

Detects when you **explicitly ask** odek to save something as a skill.

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
author: odek
`odek:
  trigger:
    topic: docker
    action: clone tag push
  auto_load: false
  quality: draft
---
## procedure-docker

Multi-step procedure detected during a odek session.

### Commands
1. docker build -t app .
2. docker tag app registry.example.com/app
3. docker push registry.example.com/app
4. kubectl rollout restart deployment/app
```

- **quality: draft** — always, since these are auto-detected (not curated)
- **auto_load: false** — skills start as lazy (loaded when triggers match), not injected into every session
- **trigger keywords** — derived automatically from the command log (topic = first meaningful word, action = extracted verbs)
- **author: odek** — distinguishes auto-generated skills from manually authored ones

## Examples

### Basic: Let odek learn from a session (auto-save enabled)

```bash
odek run "Set up PostgreSQL with Docker"
```

After the agent completes (with auto-save default):
```
   ✓ Auto-saved skill "procedure-docker" (multi-step)
```

### Interactive mode (auto-save disabled)

In `odek.json`: `{"skills": {"auto_save": {"enabled": false}}}`

```bash
odek run "Set up PostgreSQL with Docker"
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
   ── Preview ──
   ## Overview
   Procedure for: docker
   ...
   Save as skill? [Y/n/s=skip always]: y
   ✓ Saved skill "procedure-docker"
```

Now the skill is available for future runs:
```bash
odek skill list
# procedure-docker  Multi-step procedure: docker  draft
```

### Skip and re-enable

```
📝 Skill suggestion: repeated-ls
   ...
   Save as skill? [Y/n/s=skip always]: s
   Skipped permanently. Use `odek skill reset-skips` to re-enable.
```

```bash
# Later, re-enable the suggestion
odek skill reset-skips repeated-ls
```

## Limitations

- **Heuristic detection is deterministic** — same tool calls always produce the same suggestions. Skip persistence prevents repeats (one skip = permanent suppression).
- **Max 1 per heuristic** — if an agent session has 10 multi-step sequences, only the first is suggested.
- **Max 5 suggestions total** — one per heuristic type.
- **Auto-curation handles dedup** — overlapping skills are automatically merged after each session.
- **Command-only** — the heuristics work on terminal (`shell`) tool calls. Other tools (read_file, write_file) are visible in the transcript but aren't analyzed for patterns.
- **LLM enhancement requires API calls** — when `llm_learn: true`, each suggestion triggers an LLM call for enrichment. Set to `false` for zero-cost heuristic-only mode.
- **No REPL integration** — learning currently only works with `odek run`, not in REPL mode.

## Configuration

```json
{
  "skills": {
    "learn": true,
    "llm_learn": true,
    "llm_curate": true,
    "auto_save": {
      "enabled": true,
      "require_llm": true,
      "max_per_run": 3
    },
    "curation": {
      "staleness_days": 90,
      "auto_prune": false,
      "auto_curate": true,
      "skip_threshold": 1,
      "skip_reset_days": 30
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `learn` | `true` | Enable skill learning |
| `llm_learn` | `true` | Use LLM to enhance detected patterns |
| `llm_curate` | `true` | Use LLM for curation suggestions |
| `auto_save.enabled` | `true` | Auto-save without prompting |
| `auto_save.require_llm` | `true` | Only auto-save LLM-enhanced skills |
| `auto_save.max_per_run` | `3` | Max skills to auto-save per session |
| `curation.auto_curate` | `true` | Run auto-curation after sessions |
| `curation.skip_threshold` | `1` | Skips needed for permanent suppression |
| `curation.skip_reset_days` | `30` | Days before skip expires |
| `curation.auto_prune` | `false` | Auto-delete stale skills |

## Related

- [Skills System](#) — how skills are loaded and used during agent runs
- [CLI Reference](CLI.md) — all CLI flags including `--learn`
