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
[`../docs/DOCKER_COMPOSE_USER_GUIDE.md`](../docs/DOCKER_COMPOSE_USER_GUIDE.md).

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

No prompts. Best for disposable containers. One-shot task:

```bash
docker compose --profile godmode run --rm odek-godmode \
  run "Create build.sh, make it executable, and run it."
```

The trailing `run "<task>"` overrides the service's default `serve` command.

> **The container has outbound network** — Odek must reach the LLM provider API,
> so `network_mode: none` is *not* an option here (it would break the model call).
> Isolation comes from the container boundary, the non-root user, and mounting only
> `./workspace`. To also fence the agent's *own* egress while still letting Odek
> reach the model, run it on a network behind an allowlisting egress proxy — that's
> an advanced setup beyond these examples.

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

Message your bot `/start`. State (sessions, skills, `telegram.pid`) persists in the
local `./.odek` folder — an external host folder, just like `./workspace`.

> **Only run one Telegram profile at a time per token** — Telegram allows a single
> long-poller per bot (a second gets `409 Conflict`). Create a second bot via
> @BotFather if you want both.

### Scheduled reminders (cron)

The Telegram bot hosts odek's **native, in-process scheduler** — no extra
container, no external cron. Because it runs inside the bot, a scheduled task
sees the same resolved config (API key, bot token, default chat) the bot does.
Full guide: [../docs/SCHEDULES.md](../docs/SCHEDULES.md).

1. In `.env`, set **`ODEK_TELEGRAM_DEFAULT_CHAT_ID`** — the chat reminders are sent to
   (usually your own ID, the same as `ODEK_TELEGRAM_ALLOWED_CHATS`).
2. Add a job. The easiest way is **from the chat itself** — message the bot:

   ```text
   /schedule add 0 9 * * 1-5 Stand-up in 15 minutes
   ```

   Jobs added this way deliver back to that chat by default. Use `/schedules`
   to list and `/schedule rm|enable|disable|run|next` to manage them. To keep
   management host-only, set `ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT=false`
   (the chat can still list and preview).

   You can also run the CLI inside the container, or edit
   `./.odek/schedules.json` on the host directly — jobs persist in the `./.odek`
   volume and the running bot picks up changes automatically:

   ```bash
   docker compose --profile telegram-restricted exec odek-telegram-restricted \
     odek schedule add --cron "0 9 * * 1-5" --deliver telegram "Stand-up in 15 minutes"
   ```

Don't run a separate `odek schedule daemon` against the same `./.odek` while the
bot is up — a shared lock prevents double-firing, but the daemon will refuse to
start (non-zero exit, "another schedule daemon is already running") when the bot
holds it. In the reverse order (daemon up first), the bot's embedded scheduler
just defers silently.

## Voice transcription (out of the box)

The image **bundles whisper.cpp's CLI and the `tiny` ggml model**, plus `ffmpeg`
for OGG/Opus → WAV conversion — so the `transcribe` tool and Telegram voice
auto-transcription work with zero setup. No host install, no first-run download.

- The model ships at `/usr/local/share/whisper/models/ggml-tiny.bin`, and both
  `config.restricted.json` and `config.godmode.json` point
  `transcription.models_dir` there. (It lives outside `~/.odek` on purpose — the
  Telegram profiles bind-mount `./.odek`, which would otherwise shadow it.)
- Send the bot a voice note → it's transcribed locally and handed to the agent
  as text. `auto_transcribe` is on by default in the bundled configs.
- Want a more accurate (larger) model? Rebuild with
  `--build-arg WHISPER_MODEL=base` (or `small` / `medium`) and bump the
  `model` field in the config to match.

## Image & video understanding (out of the box)

The image **bundles `llama-mtmd-cli` (llama.cpp b9549) and MiniCPM-V 4.6**
(1.3B multimodal model) so the `vision` tool works with zero setup — no cloud
API, no host install, no first-run download.

- The model GGUF (`Q4_K_M`, ~529 MB) and vision projector (`mmproj`, ~1.1 GB)
  ship at `/usr/local/share/minicpm-v/models/`. They live outside `~/.odek` so
  Telegram bind-mounts cannot shadow them.
- Send the agent an image path → `vision` describes it locally using the
  bundled 1.3B model. Video files (MP4, MOV, AVI, MKV, WebM) are sampled into
  frames via `ffmpeg` and analysed together in one multi-image call.
- Want a higher-quality quantization? Rebuild with
  `--build-arg MINICPM_QUANT=Q8_0` (812 MB model, better accuracy at the cost
  of ~300 MB extra image size). Available quants: `Q4_0` (501 MB), `Q4_K_M`
  (529 MB, default), `Q8_0` (812 MB).
- To point at models installed on the host instead, set `vision.models_dir` in
  config to the directory containing `model.gguf` and `mmproj.gguf`.

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
- The container needs outbound network for the LLM API, so don't rely on
  `network_mode: none` for isolation. To restrict the agent's own egress, front it
  with an allowlisting proxy / firewalled network (advanced).
- `.env` and `workspace/` are gitignored — never commit secrets or scratch files.
