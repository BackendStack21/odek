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
└── workspace/                 # the directory the agent works in (mounted into the container)
```

> Add `.env` and `workspace/` to your `.gitignore` so you never commit secrets or
> scratch files.

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
# Pre-create ~/.odek owned by the user so a mounted named volume (used for
# Telegram session state in §13) inherits uid 1000 ownership and is writable.
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
```

---

## 5. Permission policy files

These JSON files are mounted to `/home/odek/.odek/config.json` inside the container
(Odek's global config path), so they apply regardless of which subcommand you run.

### 5a. Restricted policy — `config.restricted.json`

This is essentially Odek's default behavior, made explicit. Commands are risk‑classified;
destructive ones are denied, the rest prompt for approval. Crucially, `non_interactive`
is set to **`deny`** so that if the agent runs in a container *without* an attached
terminal or Web UI, high‑risk commands are blocked rather than silently allowed.

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

**How the classes map** (built‑in risk model):

| Class | Examples | Restricted action |
| --- | --- | --- |
| `safe` | `ls`, `cat`, `echo` | allow |
| `local_write` | write files in the working dir | allow |
| `system_write` | `chmod`, `chown`, `mkdir /etc` | prompt |
| `network_egress` | `curl`, `wget`, DNS lookups | prompt |
| `code_execution` | `go run`, `python x.py` | prompt |
| `install` | `npm install`, `apk add` | prompt |
| `destructive` | `rm -rf`, `git rm`, `docker rm` | **deny** |
| `blocked` | fork bombs, `dd` to block devices | **always deny** (cannot be overridden) |

> Approvals require a human channel: the **Web UI** (`odek serve`, modal approval over
> WebSocket) or an **interactive terminal** (`odek repl` with `docker compose run -it`).
> Without either, `non_interactive: "deny"` is what keeps you safe.

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

Add these to `docker-compose.yml`. Note the **named volume for `/home/odek/.odek`**: it
persists per‑chat sessions, the daily‑budget counter, and the singleton lock across
restarts. No `ports` are needed.

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
      - ./config.restricted.json:/home/odek/.odek/config.json:ro
      - odek-tg-state:/home/odek/.odek
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
      - ./config.godmode.json:/home/odek/.odek/config.json:ro
      - odek-tg-state:/home/odek/.odek
    restart: unless-stopped

volumes:
  odek-tg-state:
```

> The `:ro` config mount and the writable `odek-tg-state` volume both target
> `/home/odek/.odek`. Compose layers them: the bind mount wins for `config.json`, the
> named volume holds everything else (sessions, lock, budget). If your Compose version
> errors on overlapping mounts, drop the `:ro` bind and instead bake the policy into the
> image, or copy it into the volume once at startup.

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

Voice and photo messages are supported too. Sessions persist per chat (visible via
`odek session list` against the mounted state volume).

### 13f. Telegram‑specific gotchas

- **One poller per token.** Telegram allows a single long‑poller per bot token; a second
  one gets `409 Conflict`. So you **cannot run the Restricted and Godmode bot services at
  the same time with the same token** — pick one, or create a second bot via @BotFather
  for the other. A singleton PID lock at `~/.odek/telegram.pid` (kept in the shared state
  volume) backs this up — a second `odek telegram` that finds a live lock won't start.
- **Optional health endpoint.** The `telegram` command takes no CLI flags — configure it
  via env. Set `ODEK_TELEGRAM_HEALTH_ADDR=0.0.0.0:9090` in `.env` (and add a `ports:`
  mapping) to expose `GET /health` for an orchestrator's liveness probe.
- **Don't commit the token.** It lives in `.env` only; treat it like a password.
- **Cost control.** Set `ODEK_TELEGRAM_DAILY_TOKEN_BUDGET` so a runaway or abusive chat
  can't rack up unlimited model spend.

---

## Reference

- `docs/SANDBOXING.md` — Odek's nested‑Docker sandbox model (the `--sandbox` feature).
- `docs/SECURITY.md` — threat model, approval flow, YOLO mode, attack‑vector matrix.
- `docs/CONFIG.md` — full configuration layering and environment variables.
- `docs/CLI.md` — all subcommands and flags, including the `dangerous` schema.
- `docs/WEBUI.md` — Web UI protocol and the WebSocket approval flow.
- `docs/TELEGRAM.md` — Telegram bot architecture, config variables, and slash commands.
