# Running Odek in Docker — Compose User Guide

This guide walks you through running **Odek** inside Docker using Docker Compose, in
two permission profiles:

| Profile | What it means | When to use |
| --- | --- | --- |
| **Restricted** (default) | Odek classifies every shell command by risk. Destructive commands are denied, and other high‑risk commands require an approval (via the Web UI or an interactive terminal). | Day‑to‑day use, untrusted tasks, anything you want a human in the loop for. |
| **Godmode** (all permissions) | "YOLO" mode — every risk class is auto‑allowed (except a tiny hardcoded blocklist like fork bombs). No prompts. | Sealed, throwaway containers and CI pipelines where the container itself is the only blast‑radius boundary. |

> **Mental model.** Odek is a single static Go binary that runs an agent loop and
> executes shell commands. When you run Odek **inside a container**, the container
> *is* the sandbox: every command the agent runs is confined to that container's
> filesystem and (optionally) its network. You therefore do **not** need Odek's own
> `--sandbox` flag (which spawns nested Docker containers) — the Compose container
> already provides isolation. The two profiles above control *what the agent is
> allowed to do inside that boundary*.

---

## 1. Prerequisites

- **Docker** and the **Docker Compose v2** plugin (`docker compose version` should work).
- An **API key** for your provider (DeepSeek, OpenAI, Anthropic, Z.ai, a
  local Ollama endpoint, etc.). Set `ODEK_PROVIDER` plus the provider env key
  (`DEEPSEEK_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `ZAI_API_KEY`, …).
  `ODEK_API_KEY` is a v1 selected-provider override only.

All files below live in the **`docker/` directory** — not the repository root. The compose
file builds with `context: ..` (repo root) and `dockerfile: docker/Dockerfile`, and all
`docker compose` commands are meant to run from `docker/` so the relative paths and `.env`
resolve. Everything ships with the repository — the only file you create yourself is
`.env` (copied from `.env.example`).

---

## 2. Project layout

After following this guide you will have added:

```
odek/
├── go.mod                         # repo root — the compose build context (`context: ..`)
└── docker/                        # run all `docker compose` commands from here
    ├── Dockerfile                 # 4-stage image build (see §3)
    ├── Dockerfile.embeddings      # llama.cpp embeddings sidecar (bundled GGUF)
    ├── docker-compose.yml         # restricted + godmode + telegram profiles, sidecars
    ├── .env.example               # template — copy to `.env`
    ├── .env                       # your API key + model settings (you create this)
    ├── config.restricted.json     # Restricted permission policy
    ├── config.godmode.json        # Godmode (YOLO) permission policy
    ├── searxng/settings.yml       # SearXNG sidecar settings
    ├── piguard/                   # PIGuard sidecar (model download script, models dir)
    ├── workspace/                 # the directory the agent works in (mounted into the container)
    └── .odek/                     # Telegram bot state: sessions, skills, lock (created on first run)
```

> `.env`, `workspace/`, and `.odek/` are already ignored by the repository's root `.gitignore`
> (`docker/.env`, `docker/workspace/*`, `docker/.odek/*`), so secrets and scratch files are
> never committed.

---

## 3. The Dockerfile

A four‑stage build: compile the static binary with the Go toolchain, build the whisper.cpp
CLI and fetch its multilingual `small` model, build the llama‑mtmd‑cli vision runner and
fetch the MiniCPM‑V model, then assemble a Debian (bookworm‑slim) runtime image with the
agent's tooling. The full file lives at `docker/Dockerfile`; the annotated skeleton below
shows what each stage does.

```dockerfile
# syntax=docker/dockerfile:1
# Build context MUST be the repository root (compose sets
#   build: { context: .., dockerfile: docker/Dockerfile }).

# ---- build stage ----
FROM golang:1.25-alpine AS build
# go mod download → CGO_ENABLED=0 go build -ldflags "-s -w" → /out/odek
# (fully static, so it runs unchanged on the Debian runtime stage)

# ---- whisper stage ----
FROM debian:bookworm-slim AS whisper
# Builds whisper.cpp's CLI from a pinned release (WHISPER_VERSION) and fetches
# the multilingual `small` GGML model (WHISPER_MODEL, default small) into a
# fixed image path so the `transcribe` tool works with zero setup.

# ---- minicpm-v stage ----
FROM debian:bookworm-slim AS minicpm
# Builds llama-mtmd-cli from source (LLAMA_VERSION) and fetches the
# MiniCPM-V GGUF + vision projector (MINICPM_QUANT, default Q4_K_M) so the
# `vision` tool works with zero setup.

# ---- runtime stage ----
FROM debian:bookworm-slim
# Agent tooling via apt: ca-certificates git bash coreutils curl jq ffmpeg
# libstdc++6, plus gh (official apt repo), python3 + venv, Go (official
# tarball, GO_VERSION) and Bun. Copies in the odek binary and the whisper +
# minicpm artifacts. Runs as non-root user `odek` (uid 1000, HOME=/home/odek),
# WORKDIR /workspace, ENTRYPOINT ["odek"].
```

> Model sizes and build args (WHISPER_MODEL, MINICPM_QUANT, GO_VERSION, …) are documented
> inline in `docker/Dockerfile`.

> **Why no `--sandbox` inside the container?** Odek's `--sandbox` mode launches *nested*
> Docker containers for each command, which would require mounting the Docker socket
> (Docker‑in‑Docker) — a much larger attack surface. Running Odek directly in this
> container and relying on the container as the boundary is simpler and safer.

---

## 4. Secrets and model settings (`.env`)

Compose injects these into the container's environment. Odek's config chain reads
`ODEK_*` variables at runtime.

```dotenv
# .env  — DO NOT COMMIT

# Provider identity + key (examples — pick one)
ODEK_PROVIDER=deepseek
ODEK_MODEL=deepseek-v4-flash
DEEPSEEK_API_KEY=sk-your-key-here

# We run unsandboxed on purpose (the container IS the boundary), so silence
# the "running without --sandbox" startup warning that run/repl print.
ODEK_SUPPRESS_SANDBOX_WARNING=1

# If you front `odek serve` with a reverse proxy, list the proxy IPs/CIDRs here
# so X-Forwarded-For / X-Real-Ip are trusted. Empty / unset means ignored.
# ODEK_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12,192.168.0.0/16

# OpenAI:
# ODEK_MODEL=gpt-4o
# ODEK_BASE_URL=https://api.openai.com/v1

# Anthropic (OpenAI-compatible endpoint; matches the claude-sonnet-4 profile):
# ODEK_MODEL=claude-sonnet-4-5
# ODEK_BASE_URL=https://api.anthropic.com/v1

# Web search: secret for the bundled SearXNG sidecar (web_search tool).
# Generate with `openssl rand -hex 32`. The instance is internal-only.
SEARXNG_SECRET=change-me-run-openssl-rand-hex-32
```

The compose file also runs a private **SearXNG** metasearch sidecar that backs the
`web_search` tool (see [docker/README.md](../docker/README.md#web-search-out-of-the-box)).
It co-starts with every profile, is reachable only by the odek containers at
`http://searxng:8080` (no host port), and needs only `SEARXNG_SECRET` set above —
no Redis/Valkey. To disable web search, comment the `searxng` service and the
`depends_on` entries in the compose file and drop the `web_search` block
from the config files.

A second sidecar, **`llama-embeddings`** (a llama.cpp server with a bundled
`nomic-embed-text-v1.5` GGUF), gives odek real semantic embeddings —
see [docker/README.md](../docker/README.md#local-semantic-embeddings-out-of-the-box).
It also co-starts on every profile, is reachable only at `http://llama-embeddings:8080`
(no host port, no key, no first-run download), and both bundled configs set the top-level
`embedding` block to it — so memory, `session_search`, and skill matching all run
semantically. Disable the same way: comment the service + its `depends_on` entries and
drop the top-level `embedding` block (every subsystem falls back to RandomProjections).

---

## 5. Permission policy files

These JSON files are mounted to `/home/odek/.odek/config.json` inside the container
(Odek's global config path), so they apply regardless of which subcommand you run.

### 5a. Restricted policy — `config.restricted.json`

Commands are risk‑classified. Safe reads and local writes run without approval — as does
network egress, which the LLM API and sidecars need. Installs, code execution, system
writes, and persistence attempts prompt for approval; `unknown` and `destructive` are
denied outright. For headless runs, `non_interactive` is **`read_only`**: read‑only
inspection proceeds without a human channel, and anything that would prompt is denied.

```json
{
  "sandbox": false,
  "dangerous": {
    "non_interactive": "read_only",
    "classes": {
      "safe": "allow",
      "local_write": "allow",
      "install": "prompt",
      "network_egress": "allow",
      "code_execution": "prompt",
      "persistence": "prompt",
      "unread_exec": "prompt",
      "system_write": "prompt",
      "unknown": "deny",
      "destructive": "deny",
      "blocked": "deny"
    },
    "allowlist": [],
    "denylist": ["rm -rf /"]
  }
}
```

#### What each field does

| Field | Meaning |
| --- | --- |
| `sandbox` | `false` runs commands directly in this container (the Compose setup already *is* the sandbox). `true` would nest a second Docker sandbox — not what you want here. |
| `action` | **Global default** action for any class **not** listed under `classes`. `"prompt"` here, `"allow"` = godmode, `"deny"` = lockdown. ⚠️ This overrides the *built‑in* per‑class defaults (see the gotcha below). |
| `non_interactive` | What to do with a **prompt**‑level command when there is no human channel (no TTY, no Web UI). `"deny"` blocks it; `"allow"` runs it; `"read_only"` (the shipped Restricted value) lets read‑only/inspection commands proceed and denies the rest. |
| `classes` | Per‑class action overrides. The most specific setting — it wins over `action` and the built‑in defaults. Only list the classes you want to pin. |
| `allowlist` | Commands that always run, **exact string match**, no classification. Highest priority of all. Use for a handful of trusted exact commands (e.g. `"npm run deploy"`). |
| `denylist` | Commands that are always denied, **prefix match** after trimming. Beats classification and even godmode — but **not** the allowlist. |

#### How the classes map (built‑in risk model)

| Class | Examples | Built‑in default | This profile |
| --- | --- | --- | --- |
| `safe` | `ls`, `cat`, `grep`, `git status` | allow | allow |
| `local_write` | write files in the working dir | allow | allow |
| `install` | `npm install`, `pip install`, `apk add` | prompt | prompt |
| `network_egress` | `curl`, `wget`, `ssh`, DNS lookups | prompt | allow |
| `code_execution` | `curl … \| sh`, `bash -c`, `python -c`, `go run` | prompt | prompt |
| `system_write` | `sudo`, writes to `/etc`, reads of `~/.ssh` | prompt | prompt |
| `unknown` | any command whose program name Odek does **not** recognise | deny | deny |
| `destructive` | `rm -rf /`, `dd … of=/dev/sda`, `mkfs` | deny | **deny** |
| `blocked` | fork bombs, fully‑specified `dd` to a block device | **always deny** | **always deny** (cannot be overridden) |

> The shipped Restricted file pins the classes explicitly: `safe`, `local_write`, and
> `network_egress` are allowed; `install`, `code_execution`, `persistence`, `unread_exec`,
> and `system_write` prompt; `unknown`, `destructive`, and `blocked` are denied. See the
> gotcha below before adding a global `action`.

Odek **fails closed**: the `unknown` class catches any command whose verb isn't in the
built‑in safe/dangerous tables, so a novel or obfuscated command can't slip through as
"safe". To permit a specific unrecognised tool, add its exact invocation to `allowlist`,
or relax the class with `"unknown": "prompt"`.

#### How an action is resolved (precedence, first match wins)

1. Command exactly matches an **`allowlist`** entry → **allow**.
2. Command starts with a **`denylist`** entry → **deny**.
3. Otherwise classify it, then: explicit **`classes`** entry → `blocked` is **always deny** → global **`action`** (if set) → built‑in class default.
4. If the result is **prompt** and there's no human channel, **`non_interactive`** decides.

> **Gotcha — `action` overrides *every* unlisted class.** The shipped Restricted file
> does **not** set `action`; it pins the classes it cares about explicitly (see the table
> above), so any class it omits keeps its built‑in default. If you add a global `action`,
> it becomes the default for every class you don't list — e.g. `action: "prompt"` would
> make even `ls` prompt (and be denied unattended). Either keep pinning classes
> explicitly, or pick `action` with that catch‑all behavior in mind.

> Approvals require a human channel: the **Web UI** (`odek serve`, modal approval over
> WebSocket) or an **interactive terminal** (`odek repl` with `docker compose run -it`).
> Without either, `non_interactive: "deny"` is what keeps you safe.

#### Customising the policy

```jsonc
// Tighter: also block all outbound network and package installs.
"classes": { "network_egress": "deny", "install": "deny", /* … */ }

// Looser: pre‑approve a few exact commands you trust, keep everything else gated.
"allowlist": ["npm ci", "npm run build", "go build ./..."]

// Allow one normally‑unrecognised tool without loosening the whole class:
"allowlist": ["terraform plan"]          // exact match only

// Full lockdown: deny everything except the allowlist.
"action": "deny"
```

### 5b. Godmode policy — `config.godmode.json`

YOLO mode. Every risk class returns `allow`; no prompts. The only thing still blocked is
the hardcoded `blocked` class (fork bombs, etc.) — that is intentional and not
configurable.

```json
{
  "sandbox": false,
  "dangerous": {
    "action": "allow",
    "non_interactive": "allow"
  }
}
```

> ⚠️ **Godmode gives the agent free rein inside the container.** Only use it with a
> throwaway container, never mount sensitive host paths or the Docker socket, and keep
> the only writable mount scoped to `./workspace`. (The container still needs outbound
> network to reach the LLM API — see the notes under §6 — so isolation comes from the
> container boundary, not from cutting the network.)

---

## 6. The Compose file

Four odek services share the same image but mount a different policy file (the Telegram
pair also mounts a writable `./.odek` state folder). Compose **profiles** keep them from
starting together — you opt into one at a time. Every odek service also co‑starts three
sidecars — `searxng`, `llama-embeddings`, and `piguard-gateway` — via `depends_on`; the
excerpt below is abridged.

```yaml
# docker-compose.yml

services:
  # ── Restricted (default) — interactive Web UI with approval prompts ──
  odek-restricted:
    profiles: ["restricted"]
    build:
      context: ..                  # repo root
      dockerfile: docker/Dockerfile
    image: odek:local
    env_file: .env
    command: ["serve", "--addr", "0.0.0.0:8080", "--no-sandbox"]
    ports:
      - "127.0.0.1:8080:8080"   # Web UI, bound to localhost only
    volumes:
      - ./workspace:/workspace
      - ./config.restricted.json:/home/odek/.odek/config.json:ro
    restart: "no"
    depends_on: [searxng, llama-embeddings, piguard-gateway]

  # ── Godmode (all permissions) — non-interactive, disposable container ──
  odek-godmode:
    profiles: ["godmode"]
    build:
      context: ..                  # repo root
      dockerfile: docker/Dockerfile
    image: odek:local
    env_file: .env
    # No published ports (no inbound needed). Outbound networking stays on —
    # Odek must reach the LLM provider API to run.
    command: ["serve", "--addr", "0.0.0.0:8080", "--no-sandbox"]
    volumes:
      - ./workspace:/workspace
      - ./config.godmode.json:/home/odek/.odek/config.json:ro
    restart: "no"
    depends_on: [searxng, llama-embeddings, piguard-gateway]
```

Notes:

- `--no-sandbox` is required **for `serve` only**: `odek serve` turns the nested‑Docker
  sandbox on by default, so without this flag it would try to launch sandbox containers and
  fail. `odek run`, `odek repl`, and `odek telegram` are already unsandboxed by default and
  do **not** accept a `--no-sandbox` flag (it would be parsed as part of the task).
- The Web UI binds to `0.0.0.0:8080` *inside* the container; the `ports` mapping exposes
  it only on the host's `127.0.0.1`. Use a reverse proxy (Caddy/nginx) if you need remote
  access.
- **Don't use `network_mode: "none"`.** Odek calls the LLM provider over the network every
  turn, so a no‑network container can't run at all. The container's isolation comes from
  the boundary itself, the non‑root user, and mounting only `./workspace`. To restrict the
  *agent's own* egress while still letting Odek reach the model, put it on a network behind
  an allowlisting egress proxy (advanced — out of scope here).

---

## 7. Running — Restricted (default)

This is the recommended interactive mode. The Web UI shows an approval modal whenever the
agent wants to run a `prompt`‑class command, and blocks `destructive` ones outright.

```bash
# 1. Create the workspace dir the agent will operate in
mkdir -p workspace

# 2. Build and start the Restricted service
docker compose --profile restricted up --build
```

Then:

1. Look at the container logs for the full Web UI URL with the per-instance
   WebSocket token, e.g. `http://127.0.0.1:8080/?token=...`. Open that exact URL
   in your browser (plain `http://127.0.0.1:8080` no longer receives the token).
2. Type a task, e.g. *"List the files in this directory and summarize the README."*
3. When the agent attempts a higher‑risk command (an install, code execution, system
   write, or persistence attempt), an **approval modal** appears showing the command and
   its risk class. Approve or deny.
4. Destructive commands are rejected automatically — you'll see the denial in the stream.

Stop with `Ctrl‑C`, then `docker compose --profile restricted down`.

### Restricted in a plain terminal (no Web UI)

Prefer a REPL over the terminal? Approval prompts then come from the TTY, which requires
an interactive container (`-it`):

```bash
docker compose run --rm -it \
  -v "$PWD/workspace:/workspace" \
  -v "$PWD/config.restricted.json:/home/odek/.odek/config.json:ro" \
  odek-restricted repl
```

> `repl` (like `run`) is unsandboxed by default, so no `--no-sandbox` is needed — only
> `serve` requires it. The `command:` in the Compose service is overridden by the `repl`
> argument here.

> One‑shot `odek run "<task>"` works too, but it is non‑interactive: with the Restricted
> policy above, `non_interactive: "read_only"` lets read‑only/inspection commands proceed
> and denies everything that would prompt (`unknown` and `destructive` are denied
> regardless). Use this for tasks that only need safe / local‑write operations, or add
> specific commands to the policy's `allowlist`.

---

## 8. Running — Godmode (all permissions)

No prompts, no human in the loop. Best for disposable containers.

### One‑shot task

```bash
mkdir -p workspace

docker compose --profile godmode run --rm odek-godmode \
  run "Clone nothing — just create build.sh, make it executable, and run it."
```

The trailing `run "<task>"` overrides the service's default `command:` (`serve`). No
`--no-sandbox` is needed — `run` is unsandboxed by default.

Every command the agent issues runs immediately. The blast radius is the container: the
only writable host mount is `./workspace`, everything else is the container's ephemeral
filesystem, and it runs as a non‑root user. (The container does have outbound network —
Odek needs it to reach the LLM — so this is isolation by *boundary*, not by airgap.)

### Long‑running / Web UI

If you want the Web UI in Godmode too (e.g. a personal automation box):

```bash
docker compose --profile godmode up --build
```

Then add a `ports:` mapping to the `odek-godmode` service so you can reach the UI. **Only
do this on a trusted host** — in Godmode the UI grants unrestricted command execution
inside the container.

---

## 9. Verifying the permission behavior

A quick sanity check that the two profiles really differ:

**Restricted** — ask the agent (via the Web UI) to run something destructive, e.g.
*"Delete every file in /workspace with rm -rf."* It should be **denied** with a
configuration message, never executed.

**Godmode** — the same instruction executes without a prompt. (Run it against a throwaway
`workspace/` so you don't lose anything you care about.)

You can also confirm the active policy by printing the mounted config:

```bash
docker compose --profile restricted run --rm --entrypoint cat \
  odek-restricted /home/odek/.odek/config.json
```

---

## 10. Tuning the policy

The `dangerous` block is flexible. A few common adjustments to
`config.restricted.json`:

- **Pre‑approve specific commands** (exact match bypasses all checks):
  ```json
  "allowlist": ["npm test", "go build ./..."]
  ```
- **Always block specific commands** (prefix match, wins even in Godmode):
  ```json
  "denylist": ["rm -rf /", "git push --force"]
  ```
- **Loosen one class** while keeping the rest strict:
  ```json
  "classes": { "network_egress": "allow" }
  ```
- **Lockdown mode** — deny everything unless explicitly allowlisted:
  ```json
  "dangerous": { "action": "deny", "allowlist": ["go test ./..."] }
  ```

Precedence, highest first: **allowlist** → **denylist** → per‑class `classes` override →
global `action` → built‑in defaults. The `blocked` class is always denied regardless.

---

## 11. Security checklist

- ✅ Container runs as **non‑root** (`USER odek`) — keep it that way.
- ✅ Mount only the directories the agent needs (`./workspace`). Never mount `/`, `$HOME`,
  SSH keys, cloud credentials, or `/var/run/docker.sock`.
- ✅ Keep the Web UI bound to `127.0.0.1` on the host; front it with an authenticated
  reverse proxy for any remote access.
- ✅ Remember the container needs **outbound** network for the LLM API, so `network_mode:
  none` isn't an option. To fence the agent's *own* egress, use a firewalled network or an
  allowlisting egress proxy rather than relying on Docker's network mode.
- ✅ Treat **Godmode containers as disposable**: `--rm`, no persistent secrets beyond the
  injected API key, throwaway `workspace/`.
- ✅ Keep `.env` out of version control.

---

## 12. Troubleshooting

| Symptom | Likely cause / fix |
| --- | --- |
| `odek serve` exits complaining about sandbox / Docker | You omitted `--no-sandbox`. Odek tried to start nested sandbox containers. Add `--no-sandbox` to the `command`. |
| Agent says "operation denied by configuration" for normal commands | You're running non‑interactively under the Restricted policy (`non_interactive: "read_only"` — only read‑only commands proceed). Use the Web UI / `repl -it`, or add the command to `allowlist`. |
| Approval modal never appears; risky commands just run | The Godmode policy is mounted, or `action` is `allow`. Check `/home/odek/.odek/config.json` inside the container. |
| "no API key" / auth errors | `.env` not loaded or key invalid. Confirm `env_file: .env` is set and the provider env key (`DEEPSEEK_API_KEY`, `ZAI_API_KEY`, …) matches `ODEK_PROVIDER`. |
| Config changes ignored | The file is mounted read‑only at startup; recreate the container (`docker compose ... up` again) after editing the JSON. |
| Web UI unreachable | Ensure the service has a `ports:` mapping and the container command binds `--addr 0.0.0.0:8080` (not `127.0.0.1`, which would only listen inside the container). |

---

## 13. Running as a Telegram bot

Odek ships a built‑in Telegram bot (`odek telegram`) that drives the same agent from a
chat. It's an excellent fit for Docker because it uses **outbound long‑polling** — the
container reaches *out* to `api.telegram.org`, so you need **no published ports and no
inbound networking**. It works behind NAT, and both permission profiles apply unchanged.

**How approvals work over Telegram.** In Restricted mode, the human‑in‑the‑loop channel is
the chat itself: when the agent wants to run a `prompt`‑class command, the bot sends an
inline keyboard — **`[Approve] [Deny] [Trust]`** — and blocks until you tap one (120 s
timeout → treated as deny). `destructive` is still auto‑denied; Godmode (YOLO) sends no
keyboards at all. This means the Restricted policy from §5a works as‑is over Telegram —
no Web UI or TTY required.

### 13a. Get a token and lock the bot down

1. Message **@BotFather** on Telegram → `/newbot` → copy the **bot token**.
2. Find your **numeric chat ID** (e.g. message **@userinfobot**, or check the bot's logs
   on first message).

> ⚠️ **Always set an allowlist.** A bot token is a public endpoint — anyone who finds it
> can message your bot and drive the agent. Restrict it to your own chat/user ID. Denied
> updates are rejected *before* any tool call runs.

### 13b. Add Telegram settings to `.env`

```dotenv
# Telegram (append to the .env from §4)
ODEK_TELEGRAM_BOT_TOKEN=123456:ABC-your-bot-token
ODEK_TELEGRAM_ALLOWED_CHATS=11111111        # comma-separated chat IDs — your own
ODEK_TELEGRAM_ALLOWED_USERS=11111111        # comma-separated user IDs (optional)
ODEK_TELEGRAM_DAILY_TOKEN_BUDGET=2000000    # optional cost cap; 0 / unset = unlimited
ODEK_TELEGRAM_SESSION_TTL_HOURS=24          # optional
```

### 13c. Compose services

The compose file ships these two services (shown abridged). State (per‑chat sessions, the
daily‑budget counter, and the singleton lock) lives in a local **`./.odek` folder** — an
external host folder, just like `./workspace` — so it survives restarts and is easy to
inspect. No `ports` are needed.

```yaml
  # ── Telegram bot — Restricted (approvals via inline keyboards) ──
  odek-telegram-restricted:
    profiles: ["telegram-restricted"]
    build:
      context: ..                  # repo root
      dockerfile: docker/Dockerfile
    image: odek:local
    env_file: .env
    command: ["telegram"]
    init: true   # reaps agent child processes + forwards SIGTERM for clean shutdown
    volumes:
      - ./workspace:/workspace
      - ./.odek:/home/odek/.odek
      - ./config.restricted.json:/home/odek/.odek/config.json:ro
    restart: unless-stopped
    depends_on: [searxng, llama-embeddings, piguard-gateway]

  # ── Telegram bot — Godmode (no prompts; disposable container) ──
  odek-telegram-godmode:
    profiles: ["telegram-godmode"]
    build:
      context: ..                  # repo root
      dockerfile: docker/Dockerfile
    image: odek:local
    env_file: .env
    command: ["telegram"]
    init: true
    volumes:
      - ./workspace:/workspace
      - ./.odek:/home/odek/.odek
      - ./config.godmode.json:/home/odek/.odek/config.json:ro
    restart: unless-stopped
    depends_on: [searxng, llama-embeddings, piguard-gateway]
```

Create the folder first (so the container's non‑root user can write to it) — the repo's
root `.gitignore` already ignores its contents (`docker/.odek/*`):

```bash
mkdir -p .odek && chmod 777 .odek && touch .odek/.gitkeep
```

> The `./.odek` bind mounts at `/home/odek/.odek`, and `config.json` is bind‑mounted on top
> of it — a nested file‑over‑directory mount. Compose layers them: the `:ro` `config.json`
> wins for that one file, and `./.odek` holds everything else (sessions, lock, budget).
> Docker leaves a harmless empty `./.odek/config.json` stub on the host as the mount point.

### 13d. Run it

**Restricted** (recommended — you approve risky commands from your phone):

```bash
docker compose --profile telegram-restricted up --build -d
docker compose --profile telegram-restricted logs -f   # watch it come online
```

Message your bot: `/start`, then try a task. When the agent hits a `prompt`‑class command
you'll get an inline keyboard — tap **Approve**, **Deny**, or **Trust** (trust = allow that
risk class for the rest of the session).

**Godmode** (no prompts — only on a trusted host):

```bash
docker compose --profile telegram-godmode up --build -d
```

Stop either with `docker compose --profile telegram-restricted down` (matching profile).

### 13e. Useful in‑chat commands

| Command | Action |
| --- | --- |
| `/start` | Welcome / bot info |
| `/help` | List all commands |
| `/new` | Archive the current session, start fresh |

Voice and photo messages are supported too. Sessions persist per chat in the local
`./.odek` folder (inspect with `odek session list` against that directory).

### 13f. Telegram‑specific gotchas

- **One poller per token.** Telegram allows a single long‑poller per bot token; a second
  one gets `409 Conflict`. So you **cannot run the Restricted and Godmode bot services at
  the same time with the same token** — pick one, or create a second bot via @BotFather
  for the other. A singleton advisory lock on `~/.odek/telegram.lock` (kept in the shared
  `./.odek` folder) backs this up — a second `odek telegram` that holds the lock won't start.
- **Optional health endpoint.** The `telegram` command takes no CLI flags — configure it
  via env. Set `ODEK_TELEGRAM_HEALTH_ADDR=0.0.0.0:9090` in `.env` (and add a `ports:`
  mapping) to expose `GET /health` for an orchestrator's liveness probe.
- **Don't commit the token.** It lives in `.env` only; treat it like a password.
- **Cost control.** Set `ODEK_TELEGRAM_DAILY_TOKEN_BUDGET` so a runaway or abusive chat
  can't rack up unlimited model spend.

---

## Reference

- [`SANDBOXING.md`](SANDBOXING.md) — Odek's nested‑Docker sandbox model (the `--sandbox` feature).
- [`SECURITY.md`](SECURITY.md) — threat model, approval flow, YOLO mode, attack‑vector matrix.
- [`CONFIG.md`](CONFIG.md) — full configuration layering and environment variables.
- [`CLI.md`](CLI.md) — all subcommands and flags, including the `dangerous` schema.
- [`WEBUI.md`](WEBUI.md) — Web UI protocol and the WebSocket approval flow.
- [`TELEGRAM.md`](TELEGRAM.md) — Telegram bot architecture, config variables, and slash commands.
