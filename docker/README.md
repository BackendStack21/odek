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
├── Dockerfile.embeddings    # llama.cpp embeddings sidecar (bundled GGUF)
├── docker-compose.yml       # odek (4 profiles) + searxng + llama-embeddings
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
>
> **File downloads are capped.** Voice/photo/document downloads are limited to
> `ODEK_TELEGRAM_MAX_DOWNLOAD_SIZE` (default 5 MiB) and optionally to a total
> per-chat quota via `ODEK_TELEGRAM_MEDIA_QUOTA_PER_CHAT`. This prevents a
> malicious or accidental large upload from exhausting the container disk.

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
   to list and `/schedule rm|enable|disable|run|next` to manage them.

   > **Schedule management and `/restart` are restricted to operator chats/users.**
   > Mutating commands (`add`, `rm`, `enable`, `disable`, `run`) and `/restart`
   > are allowed only from the IDs listed in `ODEK_SCHEDULES_TELEGRAM_ADMIN_CHATS` /
   > `ODEK_SCHEDULES_TELEGRAM_ADMIN_USERS`. `/restart` is also rate-limited to
   > once per 60 seconds. If neither list nor `ODEK_TELEGRAM_DEFAULT_CHAT_ID` is
   > configured, mutating commands and `/restart` are rejected (read-only
   > `list`/`view`/`next` still work). To keep management host-only,
   > set `ODEK_SCHEDULES_ALLOW_TELEGRAM_MANAGEMENT=false`.

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

The image **bundles `llama-mtmd-cli` (llama.cpp b9549, built from source) and MiniCPM-V 4.6**
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

## Web search (out of the box)

The compose setup runs a **private [SearXNG](https://docs.searxng.org/) metasearch
sidecar** backing the `web_search` tool — no cloud search API, no keys.

- The `searxng` service co-starts with every profile and is reachable only by the
  odek containers at `http://searxng:8080` (**no host port is published** — the
  agent is the only consumer). Both bundled configs set `web_search.base_url` to it.
- `docker/searxng/settings.yml` enables the JSON API (`search.formats: [html, json]`)
  and disables the anti-bot limiter, so **no Redis/Valkey is required**.
- Set **`SEARXNG_SECRET`** in `.env` (e.g. `openssl rand -hex 32`).
- The agent searches, gets ranked results, then fetches the URLs it wants with the
  `browser` / `http_batch` tools. Results are wrapped as untrusted content.
- SearXNG needs outbound internet to reach upstream engines (Google, Bing,
  DuckDuckGo, …). If you front the stack with an allowlisting egress proxy, permit those.
- To run **without** web search: comment out the `searxng` service (and the
  `depends_on: [searxng]` lines), and remove the `web_search` block from the configs.

## Local semantic embeddings (out of the box)

The compose setup runs a **private [llama.cpp](https://github.com/ggerganov/llama.cpp)
embeddings sidecar** backing odek's semantic features — no cloud embeddings API, no keys.

Without it, similarity runs on local bag-of-words vectors: fast, but purely lexical —
*"fixed the auth bug"* and *"repaired login issue"* don't match. The sidecar swaps that
for a real embedding model, so everything matches by **meaning**. Both bundled configs
set the **top-level `embedding` block** to the sidecar, so one endpoint powers all three
consumers at once:

- **Memory** — episode recall, dedup, ranking, fact merge-on-write.
- **Sessions** — the `session_search` tool matches past sessions by meaning.
- **Skills** — lazy skill matching (inherits the shared default, with the per-turn query
  timeout bounded to 2s so the hot path stays fast).

See [`../docs/MEMORY.md`](../docs/MEMORY.md) → *Pluggable Embeddings*,
[`../docs/SESSIONS.md`](../docs/SESSIONS.md) → *Session Search*, and
[`../docs/CONFIG.md`](../docs/CONFIG.md) → *Shared embedding backend*.

- The `llama-embeddings` service co-starts with every profile and is reachable only by
  the odek containers at `http://llama-embeddings:8080` (**no host port** — the odek
  containers are the only consumers). Both bundled configs set the top-level `embedding`
  block to it; memory, sessions, and skills inherit it.
- The image **bundles `llama-server` (built from source, pinned to the same llama.cpp
  release as the main image) and `nomic-embed-text-v1.5`** (768-dim, ~84 MB at Q4_K_M)
  — so there's **no first-run model download** and no volume, mirroring the bundled
  whisper / MiniCPM-V models. The server runs `--embeddings --pooling mean` and exposes
  the OpenAI-compatible `/v1/embeddings` endpoint.
- **Graceful by design:** if the sidecar is still loading or unreachable, each consumer
  degrades safely — memory recall falls back to "no context", `session_search` to its
  keyword tier, skill matching to the keyword matcher — all with a 30s/short-timeout
  backoff, so the agent loop is never blocked and a wrong dedup never deletes an episode.
  Default behavior without the service is local RandomProjections everywhere.
- Want a higher-quality quantization? Rebuild with
  `--build-arg EMBED_QUANT=Q8_0` (available: `Q4_K_M` default, `Q5_K_M`, `Q6_K`, `Q8_0`,
  `f16`). To use a different model, override `EMBED_HF_REPO` / `EMBED_HF_REVISION` /
  `EMBED_FILE` and update `embedding.model` in the configs.
- To run **without** local embeddings: comment out the `llama-embeddings` service (and
  the matching `depends_on` entries), and remove the top-level `embedding` block from the
  configs — every subsystem falls back to RandomProjections automatically.
- **Point `base_url` only at a server you trust:** session transcripts, episode summaries,
  fact entries, and skill text are all sent there for embedding. Here it's the in-network
  sidecar, so nothing leaves the compose network; if you repoint it at a cloud API, that
  text egresses.

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
