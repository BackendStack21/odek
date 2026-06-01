# Odek — Docker Compose examples

Ready-to-run Compose setup for Odek in two permission profiles:

| Profile | Meaning | Use for |
| --- | --- | --- |
| **Restricted** (default) | Commands are risk-classified; destructive ones denied, the rest require approval. | Day-to-day use, untrusted tasks, human-in-the-loop. |
| **Godmode** (all permissions) | "YOLO" mode — every risk class auto-allowed (except a hardcoded blocklist like fork bombs). No prompts. | Sealed, throwaway containers / CI. |

> **Why no `--sandbox`?** Odek's own `--sandbox` flag spawns *nested* Docker
> containers. Here the Compose container **is** the sandbox, so commands run
> directly inside it. The profile controls *what the agent may do inside that
> boundary*. (`serve` defaults sandbox on, so its command passes `--no-sandbox`;
> `run`/`repl`/`telegram` are unsandboxed by default.)

For the full walkthrough, threat model, and tuning, see
[`../DOCKER_COMPOSE_USER_GUIDE.md`](../DOCKER_COMPOSE_USER_GUIDE.md).

## Files

```
docker/
├── Dockerfile               # multi-stage build of the odek binary
├── docker-compose.yml       # 4 services across 4 profiles
├── config.restricted.json   # Restricted permission policy
├── config.godmode.json      # Godmode (YOLO) permission policy
├── .env.example             # copy to .env, add your API key
└── workspace/               # the dir the agent works in (mounted in)
```

## Quick start

All commands are run **from this `docker/` directory** so relative paths and
`.env` resolve correctly:

```bash
cd docker
cp .env.example .env        # then edit .env: set ODEK_API_KEY (+ model/base URL)
```

### Restricted (recommended)

Interactive Web UI with approval prompts.

```bash
docker compose --profile restricted up --build
```

Open <http://127.0.0.1:8080>, type a task. When the agent attempts a higher-risk
command (network, install, code execution) an **approval modal** appears — approve
or deny. Destructive commands are rejected automatically. Stop with `Ctrl-C`, then
`docker compose --profile restricted down`.

Prefer a terminal REPL (approvals come from the TTY, so `-it` is required):

```bash
docker compose run --rm -it odek-restricted repl
```

### Godmode (all permissions)

No prompts. Best for sealed, disposable containers. One-shot task:

```bash
docker compose --profile godmode run --rm odek-godmode \
  run "Create build.sh, make it executable, and run it."
```

The trailing `run "<task>"` overrides the service's default `serve` command. The
service sets `network_mode: none`, so nothing leaves the container — remove that
line in `docker-compose.yml` if the task genuinely needs network egress.

### Telegram bot

Drive the agent from a Telegram chat. Outbound long-polling — **no inbound ports
needed**. Approvals (Restricted) arrive as inline `[Approve] [Deny] [Trust]`
keyboards.

1. Create a bot with **@BotFather**, copy the token.
2. In `.env`, set `ODEK_TELEGRAM_BOT_TOKEN` and **always** restrict access with
   `ODEK_TELEGRAM_ALLOWED_CHATS` (and/or `_USERS`) — a bot token is a public
   endpoint.
3. Start one of:

```bash
docker compose --profile telegram-restricted up --build -d   # approvals in chat
docker compose --profile telegram-godmode up --build -d      # no prompts
```

Message your bot `/start`. Sessions persist in the `odek-tg-state` volume.

> **Only run one Telegram profile at a time per token** — Telegram allows a single
> long-poller per bot (a second gets `409 Conflict`). Create a second bot via
> @BotFather if you want both.

## Verify the profiles differ

- **Restricted**: ask it to `rm -rf` everything in `/workspace` → denied, never runs.
- **Godmode**: the same request executes without a prompt (use a throwaway `workspace/`).

Print the active policy mounted in a container:

```bash
docker compose --profile restricted run --rm --entrypoint cat \
  odek-restricted /home/odek/.odek/config.json
```

## Tuning

Edit `config.restricted.json`. Precedence (highest first): `allowlist` (exact
match) → `denylist` (prefix) → per-class `classes` → global `action` → built-in
defaults. The `blocked` class (fork bombs, etc.) is always denied. Recreate the
container after editing (`... up` again) since the config is mounted at startup.

```jsonc
{
  "dangerous": {
    "action": "prompt",
    "allowlist": ["go test ./...", "npm test"],   // always allowed
    "denylist": ["git push --force"],              // always blocked
    "classes": { "network_egress": "allow" }       // loosen one class
  }
}
```

## Security notes

- Container runs as **non-root** (uid 1000). Keep it that way.
- Mount only what the agent needs (`./workspace`). Never mount `/`, `$HOME`, SSH
  keys, cloud creds, or `/var/run/docker.sock`.
- Keep the Web UI on `127.0.0.1`; front it with an authenticated reverse proxy for
  remote access.
- Prefer `network_mode: none` (or a scoped network), especially for Godmode.
- `.env` and `workspace/` are gitignored — never commit secrets or scratch files.
