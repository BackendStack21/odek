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
- An **API key** for an OpenAI‑compatible model provider (DeepSeek, OpenAI, Anthropic, a
  local Ollama endpoint, etc.). Odek reads it from `ODEK_API_KEY` (with legacy fallbacks
  `DEEPSEEK_API_KEY` → `OPENAI_API_KEY`).

All files below live in the **repository root** (next to `go.mod`). Create them as shown.

---

## 2. Project layout

After following this guide you will have added:

```
odek/
├── Dockerfile                 # builds the odek binary
├── docker-compose.yml         # restricted + godmode services
├── .env                       # your API key + model settings (gitignored)
├── config.restricted.json     # Restricted permission policy
├── config.godmode.json        # Godmode (YOLO) permission policy
├── workspace/                 # the directory the agent works in (mounted into the container)
└── .odek/                     # Telegram bot state: sessions, skills, lock (mounted in)
```

> Add `.env`, `workspace/`, and `.odek/` to your `.gitignore` so you never commit secrets
> or scratch files.

---

## 3. The Dockerfile

A multi‑stage build: compile the static binary with the Go toolchain, then ship it on a
small runtime image that already has a shell and common tooling for the agent to use.

```dockerfile
# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache modules first
COPY go.mod go.sum ./
RUN go mod download

# Build the static binary (mirrors the Makefile `build` target)
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/odek ./cmd/odek

# ---- runtime stage ----
FROM alpine:latest
# Tooling the agent commonly needs inside the sandbox container.
# Trim or extend this list to taste.
RUN apk add --no-cache ca-certificates git bash coreutils curl jq

# Run as a non-root user — defense in depth even inside the container.
# Pre-create ~/.odek owned by the user so it's writable for config, sessions,
# and the Telegram lock (whether backed by an image dir or a mounted folder).
RUN adduser -D -u 1000 odek \
 && mkdir -p /home/odek/.odek /workspace \
 && chown -R odek:odek /home/odek/.odek /workspace

COPY --from=build /out/odek /usr/local/bin/odek

# Docker does NOT set $HOME from USER, but Odek resolves ~/.odek via $HOME.
# Set it explicitly so config.json, sessions, and the Telegram lock land in
# /home/odek/.odek (where the volume and config bind mounts are).
ENV HOME=/home/odek
USER odek
WORKDIR /workspace

ENTRYPOINT ["odek"]
```

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

# API key (ODEK_API_KEY wins; DEEPSEEK_API_KEY / OPENAI_API_KEY are fallbacks)
ODEK_API_KEY=sk-your-key-here

# Model + provider endpoint (examples — pick one)
ODEK_MODEL=deepseek-v4-flash
ODEK_BASE_URL=https://api.deepseek.com/v1

# We run unsandboxed on purpose (the container IS the boundary), so silence
# the "running without --sandbox" startup warning that run/repl print.
ODEK_SUPPRESS_SANDBOX_WARNING=1

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
`depends_on: [searxng]` lines in the compose file and drop the `web_search` block
from the config files.

---

## 5. Permission policy files

These JSON files are mounted to `/home/odek/.odek/config.json` inside the container
(Odek's global config path), so they apply regardless of which subcommand you run.

### 5a. Restricted policy — `config.restricted.json`

Commands are risk‑classified; destructive and unrecognised ones are denied, the rest
prompt for approval. Crucially, `non_interactive` is set to **`deny`** so that if the
agent runs in a container *without* an attached terminal or Web UI, anything that would
prompt is blocked rather than silently allowed.

```json
{
  "sandbox": false,
  "dangerous": {
    "action": "prompt",
    "non_interactive": "deny",
    "classes": {
      "destructive": "deny",
      "system_write": "prompt",
      "network_egress": "prompt",
      "code_execution": "prompt",
      "install": "prompt",
      "local_write": "allow"
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
| `non_interactive` | What to do with a **prompt**‑level command when there is no human channel (no TTY, no Web UI). `"deny"` blocks it; `"allow"` runs it. Always set this to `"deny"` for unattended/automated containers. |
| `classes` | Per‑class action overrides. The most specific setting — it wins over `action` and the built‑in defaults. Only list the classes you want to pin. |
| `allowlist` | Commands that always run, **exact string match**, no classification. Highest priority of all. Use for a handful of trusted exact commands (e.g. `"npm run deploy"`). |
| `denylist` | Commands that are always denied, **prefix match** after trimming. Beats classification and even godmode — but **not** the allowlist. |

#### How the classes map (built‑in risk model)

| Class | Examples | Built‑in default | This profile |
| --- | --- | --- | --- |
| `safe` | `ls`, `cat`, `grep`, `git status` | allow | prompt¹ |
| `local_write` | write files in the working dir | allow | allow |
| `install` | `npm install`, `pip install`, `apk add` | prompt | prompt |
| `network_egress` | `curl`, `wget`, `ssh`, DNS lookups | prompt | prompt |
| `code_execution` | `curl … \| sh`, `bash -c`, `python -c`, `go run` | prompt | prompt |
| `system_write` | `sudo`, writes to `/etc`, reads of `~/.ssh` | prompt | prompt |
| `unknown` | any command whose program name Odek does **not** recognise | deny | prompt¹ → denied unattended |
| `destructive` | `rm -rf /`, `dd … of=/dev/sda`, `mkfs` | deny | **deny** |
| `blocked` | fork bombs, fully‑specified `dd` to a block device | **always deny** | **always deny** (cannot be overridden) |

> ¹ `safe` and `unknown` are not listed under `classes`, so the global
> `action: "prompt"` applies to them — see the gotcha below. With a human channel
> they prompt; unattended (`non_interactive: "deny"`) they are denied.

Odek **fails closed**: the `unknown` class catches any command whose verb isn't in the
built‑in safe/dangerous tables, so a novel or obfuscated command can't slip through as
"safe". To permit a specific unrecognised tool, add its exact invocation to `allowlist`,
or relax the class with `"unknown": "prompt"`.

#### How an action is resolved (precedence, first match wins)

1. Command exactly matches an **`allowlist`** entry → **allow**.
2. Command starts with a **`denylist`** entry → **deny**.
3. Otherwise classify it, then: explicit **`classes`** entry → `blocked` is **always deny** → global **`action`** (if set) → built‑in class default.
4. If the result is **prompt** and there's no human channel, **`non_interactive`** decides.

> **Gotcha — `action` overrides *every* unlisted class.** Because `action: "prompt"` is
> set, any class you don't list under `classes` resolves to *prompt*, including `safe`.
> So with this profile as written, even `ls` prompts (and is denied unattended). Two ways
> to get the usual "safe commands just run" behavior:
>
> - add `"safe": "allow"` to `classes` (keep `action: "prompt"` as the catch‑all for
>   everything else, including `unknown`), **or**
> - **omit `action` entirely** and only override the classes you care about — then unlisted
>   classes keep their built‑in defaults (safe/local_write allow; destructive/blocked/unknown
>   deny; system_write/network_egress/code_execution/install prompt).
>
> The second form is the better default if you want `unknown` to stay deny‑by‑default
> rather than prompt.

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

Two services share the same image but mount a different policy file. Compose
**profiles** keep them from starting together — you opt into one at a time.

```yaml
# docker-compose.yml

services:
  # ── Restricted (default) — interactive Web UI with approval prompts ──
  odek-restricted:
    profiles: ["restricted"]
    build: .
    image: odek:local
    env_file: .env
    command: ["serve", "--addr", "0.0.0.0:8080", "--no-sandbox"]
    ports:
      - "127.0.0.1:8080:8080"   # Web UI, bound to localhost only
    volumes:
      - ./workspace:/workspace
      - ./config.restricted.json:/home/odek/.odek/config.json:ro
    restart: "no"

  # ── Godmode (all permissions) — non-interactive, disposable container ──
  odek-godmode:
    profiles: ["godmode"]
    build: .
    image: odek:local
    env_file: .env
    # No published ports (no inbound needed). Outbound networking stays on —
    # Odek must reach the LLM provider API to run.
    command: ["serve", "--addr", "0.0.0.0:8080", "--no-sandbox"]
    volumes:
      - ./workspace:/workspace
      - ./config.godmode.json:/home/odek/.odek/config.json:ro
    restart: "no"
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

1. Open **http://127.0.0.1:8080** in your browser.
2. Type a task, e.g. *"List the files in this directory and summarize the README."*
3. When the agent attempts a higher‑risk command (network, install, code execution), an
   **approval modal** appears showing the command and its risk class. Approve or deny.
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
> policy above, `prompt`‑class commands are **denied** (`non_interactive: "deny"`) and
> destructive ones are always denied. Use this for tasks that only need safe / local‑write
> operations, or add specific commands to the policy's `allowlist`.

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
| Agent says "operation denied by configuration" for normal commands | You're running non‑interactively under the Restricted policy (`non_interactive: "deny"`). Use the Web UI / `repl -it`, or add the command to `allowlist`. |
| Approval modal never appears; risky commands just run | The Godmode policy is mounted, or `action` is `allow`. Check `/home/odek/.odek/config.json` inside the container. |
| "no API key" / auth errors | `.env` not loaded or key invalid. Confirm `env_file: .env` is set and `ODEK_API_KEY` is correct. |
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

Add these to `docker-compose.yml`. State (per‑chat sessions, the daily‑budget counter, and
the singleton lock) lives in a local **`./.odek` folder** — an external host folder, just
like `./workspace` — so it survives restarts and is easy to inspect. No `ports` are needed.

```yaml
  # ── Telegram bot — Restricted (approvals via inline keyboards) ──
  odek-telegram-restricted:
    profiles: ["telegram-restricted"]
    build: .
    image: odek:local
    env_file: .env
    command: ["telegram"]
    volumes:
      - ./workspace:/workspace
      - ./.odek:/home/odek/.odek
      - ./config.restricted.json:/home/odek/.odek/config.json:ro
    restart: unless-stopped

  # ── Telegram bot — Godmode (no prompts; disposable container) ──
  odek-telegram-godmode:
    profiles: ["telegram-godmode"]
    build: .
    image: odek:local
    env_file: .env
    command: ["telegram"]
    volumes:
      - ./workspace:/workspace
      - ./.odek:/home/odek/.odek
      - ./config.godmode.json:/home/odek/.odek/config.json:ro
    restart: unless-stopped
```

Create the folder first (so the container's non‑root user can write to it) and gitignore
its contents:

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
  for the other. A singleton PID lock at `~/.odek/telegram.pid` (kept in the shared `./.odek`
  folder) backs this up — a second `odek telegram` that finds a live lock won't start.
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
