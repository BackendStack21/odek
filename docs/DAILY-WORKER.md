# odek as Your Daily Worker — Integration Proposals

## Strengths Comparison

| Capability | Hermes (you) | odek |
|---|---|---|
| **Coding (refactor, debug, build)** | ✅ via `delegate_task` / tools | 🏆 Go-native, 3ms startup, sub-agent farm |
| **Platform integration** (Telegram, email, GitHub, Notion) | 🏆 Native | ❌ None |
| **Browser / Vision / TTS** | 🏆 Native | ❌ None |
| **Sub-agent isolation** | Goroutine + context | 🏆 Real OS processes, `exec.Command` |
| **Memory** | Per-session + persistent | 🏆 3-tier (facts, buffer, episodes) + vector |
| **Skills auto-learning** | Manual skills | 🏆 Auto-detect patterns, trie-based trigger |
| **Docker sandbox** | Manual | 🏆 Wired into agent loop |
| **Cron / scheduling** | 🏆 Native | ❌ None |
| **Web UI** | ❌ | 🏆 `odek serve` |
| **MCP bidirectional** | Client only (native-mcp) | 🏆 Server + Client |

## Proposed Integration: Hermes Orchestrates, odek Builds

Top 3 patterns, ordered by value:

---

### 1. Direct CLI delegation — `odek run` for coding tasks

Best for: focused coding work (refactor, implement, debug)

```
You: "Refactor the auth module to use context-based middleware"

Hermes:
  1. Spawn → odek run --session "Refactor auth to context-based middleware"
  2. odek reads files, plans, rewrites, tests
  3. Hermes receives result, verifies, reports back to you
```

**Setup:** none — odek is already installed. Just call from terminal.

**Pros:** Zero infra, odek's full power, session persistence
**Cons:** No Hermes tool access during task (no browser, no search)

---

### 2. MCP bridge — share tools bidirectionally

Best for: giving odek access to Hermes' unique tools (browser, search, vision) during coding

```
      ┌─────────────┐     MCP stdio      ┌─────────────┐
      │   Hermes    │◄──────────────────►│    odek     │
      │             │                    │             │
      │  Telegram   │                    │  shell/file  │
      │  Browser    │                    │  sub-agents  │
      │  Vision     │                    │  sandbox     │
      │  Cron       │                    │  skills      │
      └─────────────┘                    └─────────────┘
```

**Setup:**
```bash
# In one terminal (or background):
odek mcp                                    # odek serves its tools via MCP

# Configure Hermes to connect as MCP client:
# ~/.hermes/config.yaml
mcp_servers:
  odek:
    command: odek
    args: [mcp]
```

Then Hermes can call `odek__shell`, `odek__readFile` etc. when it needs odek's sub-agent or sandbox features.

**Pros:** Bidirectional tool access, no context switching
**Cons:** More moving parts, latency from MCP serialization

---

### 3. odek serve — Web UI for long-running sessions

Best for: complex multi-hour coding tasks you want to monitor visually

```bash
odek serve --port 3001
# Open http://localhost:3001 in browser
# odek runs autonomously, shows live token stats, tool calls
```

You can start it alongside Hermes and switch between Telegram (Hermes) and the Web UI (odek) for the same task.

**Pros:** Visual debugging, per-message token stats, drag-drop files
**Cons:** Separate window, no Telegram integration

---

### 4. Hybrid: Hermes + odek sub-agent farm (recommended daily driver)

Combine all three:

```
┌─────────────────────────────────────────────────────┐
│                    Hermes                            │
│                                                      │
│  Telegram DM ◄──► You                                │
│                                                      │
│  delegate_task ──► Spawns odek sub-agents via:       │
│    1. odek run "Implement feature X"                 │
│    2. odek run "Fix bug in Y"                        │
│    3. odek run --sandbox "Test Z safely"             │
│                                                      │
│  odek skills dir shared: ~/.odek/skills/             │
│  odek memory shared: ~/.odek/memory/                 │
└─────────────────────────────────────────────────────┘
```

**Setup:**
```bash
# Ensure odek is on PATH
which odek || go install github.com/BackendStack21/kode/cmd/odek@latest

# Optionally share the skills directory
ln -s ~/.odek/skills ~/.hermes/skills  # optional
```

Then create a Hermes skill for it:

<skill name="odek-delegate">
Trigger: user asks to refactor / implement / debug code
Steps:
  1. Read the relevant files for context
  2. Call: odek run --session "task description with full context"
  3. Capture result, verify files were modified
  4. Report back to user
</skill>

---

## What I Recommend

Start with **Pattern 1** (direct CLI delegation). It's zero-setup and gives you immediate value:

```bash
# Example — I'd use this daily:
odek run --session "Add E2E tests for the YOLO mode config"
odek run "Refactor shell.go to extract a helper function"
odek run --sandbox "Run the full test suite"
```

Add **Pattern 4** (shared skills/memory) when you want odek to learn from repeated tasks. Add **Pattern 2** (MCP bridge) when you need odek to use browser/vision during a coding task.

Want me to implement Pattern 1 as a Hermes skill right now?
