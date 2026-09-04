# Getting Started

Install **odek** (the agent engine), configure it with a **GLM 5.3 Flash** subscription from **z.ai**, and — optionally — add **bodek**, a polished terminal UI on top.

odek is a single static Go binary (~11 MB, instant startup). No Python, no Node, no framework — one binary and an API key is the whole install.

---

## What you need

| Requirement | Notes |
|---|---|
| macOS or Linux | Prebuilt binaries, amd64 & arm64. Windows: build from source with Go |
| A z.ai API key | From the [GLM Coding Plan](https://z.ai/subscribe) subscription or pay-as-you-go. Create/manage keys at [z.ai Open Platform](https://z.ai/manage-apikey/apikey-list) |
| Go ≥ 1.25.12 | *Only* if installing from source. Not needed for prebuilt binaries |
| Docker | *Optional* — enables the default-on sandbox. Skip it with `ODEK_NO_SANDBOX=1` (see [Step 4](#4-sandbox-docker-optional)) |

---

## 1. Install odek

**From source** (recommended if you have Go):

```bash
go install github.com/BackendStack21/odek/cmd/odek@latest
```

**Or grab a prebuilt binary** from the
[releases page](https://github.com/BackendStack21/odek/releases) — binaries for
Linux and macOS (amd64 & arm64) plus a `checksums.txt` for verification.
One-liner for Linux / macOS (installs to `~/.local/bin`):

```bash
mkdir -p ~/.local/bin && curl -fLo ~/.local/bin/odek \
  "https://github.com/BackendStack21/odek/releases/latest/download/odek-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" \
  && chmod +x ~/.local/bin/odek
```

Verify:

```bash
odek version
```

Later: `odek upgrade` self-updates from GitHub Releases (SHA-256 verified) —
see [Keeping things up to date](#keeping-things-up-to-date).

---

## 2. Create the config

Scaffold the global config — this is where provider settings live:

```bash
odek init --global
```

This writes `~/.odek/config.json` (permissions `0600`; refuses to overwrite an
existing file unless you pass `--force`). Open it and set **provider + model**
(not a free-floating `base_url`):

```json
{
  "provider": "zai",
  "model": "glm-5.3-flash",
  "providers": {
    "zai": {
      "api_key": "${ZAI_API_KEY}",
      "base_url": "https://api.z.ai/api/coding/paas/v4"
    }
  },
  "llm": { "request_timeout_seconds": 300 },
  ...
}
```

- **`provider`** — `zai` (see [docs/PROVIDERS.md](docs/PROVIDERS.md)).
- **`model`** — lowercase id `glm-5.3-flash` (1M last-resort context window).
- **`providers.zai.base_url`** — GLM Coding Plan (subscription) uses
  `https://api.z.ai/api/coding/paas/v4`. Pay-as-you-go: `https://api.z.ai/api/paas/v4`.
- **`providers.zai.api_key`** — `${ZAI_API_KEY}` from `secrets.env` (next step).
- **`llm.request_timeout_seconds`** — default is 300s; raise further if GLM
  reasoning is still slow to first byte.

v1 top-level `base_url` / `api_key` still work as loud aliases — see
[docs/MIGRATION.md](docs/MIGRATION.md).

> The exact model ids available to your plan are listed in your
> [z.ai dashboard](https://z.ai/model-api).

---

## 3. Store the API key

Create `~/.odek/secrets.env` — it is auto-loaded into the process environment
on startup, so the key never has to appear in your shell history:

```bash
cat > ~/.odek/secrets.env <<'EOF'
ZAI_API_KEY=sk-your-zai-key-here
EOF
chmod 600 ~/.odek/secrets.env
```

Alternative: `export ZAI_API_KEY=sk-...` per shell session. Other providers
use their own env key (`DEEPSEEK_API_KEY`, `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, …). odek resolves config through a five-layer chain:

```
~/.odek/secrets.env → ~/.odek/config.json → ./odek.json → ODEK_* env vars → CLI flags
```

Note: a project-level `./odek.json` is treated as **untrusted** — it cannot
set `provider`, `providers`, `llm`, or other sensitive fields. Provider
settings belong in the global config.

---

## 4. Sandbox (Docker, optional)

Tool execution runs inside an isolated Docker container **by default** for
`odek run`, `odek continue`, `odek repl`, and `odek serve`. If you don't have
Docker installed, opt out explicitly:

```bash
export ODEK_NO_SANDBOX=1     # add to ~/.zshrc / ~/.bashrc
# or per-run:
odek run --no-sandbox "..."
```

Unsandboxed runs warn loudly; set `ODEK_REQUIRE_SANDBOX=1` to make
unsandboxed execution fatal instead. Full model: [docs/SANDBOXING.md](docs/SANDBOXING.md).

---

## 5. First run

```bash
odek run "List the Go files in this directory and count their total lines"
```

You should see `glm-5.3-flash` in the run header and a short ReAct
trace: think → act → answer.

Interactive session:

```bash
odek repl
```

Web UI (streams tokens, tools, approvals in the browser):

```bash
odek serve
# → prints a token URL like http://127.0.0.1:8080/?token=…
```

`odek serve` binds to `127.0.0.1:8080` by default (`--addr` to change) and
never exposes itself beyond loopback unless explicitly configured.

---

## 6. Optional: bodek — the terminal UI

[bodek](https://github.com/BackendStack21/bodek) is a Bubble Tea TUI that
launches (or attaches to) an `odek serve` instance and renders the agent's
live stream — reasoning, tokens, tool calls, approval prompts — with themes
and desktop notifications. It is a **pure front-end**: every agent behaviour
still comes from odek itself.

```bash
# Install the TUI (odek is already installed — bodek needs it on PATH)
go install github.com/BackendStack21/bodek/cmd/bodek@latest
```

Prebuilt binaries are on the
[bodek releases page](https://github.com/BackendStack21/bodek/releases)
(Linux/macOS/Windows, amd64 & arm64, with `checksums.txt`):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m); [ "$ARCH" = "x86_64" ] && ARCH=amd64
URL=$(curl -fsSL https://api.github.com/repos/BackendStack21/bodek/releases/latest \
  | grep browser_download_url | grep "${OS}_${ARCH}" | cut -d '"' -f 4)
curl -fsSL "$URL" | tar -xz bodek && install -m 755 bodek ~/.local/bin/
```

Start chatting:

```bash
bodek                # spawns odek serve itself and connects
```

Useful flags:

```bash
bodek --sandbox                        # force tool calls into odek's Docker sandbox
bodek --url 'http://127.0.0.1:8080/?token=…'   # attach to an already-running odek serve
bodek --theme ember-light              # start with a theme (/theme switches live)
bodek --notify                         # desktop notifications on turn/approval events
bodek --mouse                          # mouse-wheel scrolling (blocks text selection)
```

---

## Keeping things up to date

Both tools self-upgrade from their GitHub Releases. Each run downloads the
latest release, verifies it against the release's `checksums.txt`
(SHA-256), and replaces the running binary atomically — no manual download,
no package manager.

```bash
odek upgrade          # update odek to the latest release
bodek upgrade         # update bodek to the latest release
```

Both commands detect the current version and report that it is already up to
date when nothing newer exists. To preview without touching the binary:

```bash
odek upgrade --check  # report the latest odek version, install nothing
```

Notes:

- The upgrade replaces the binary **wherever it lives** — `~/.local/bin`
  from the one-liner install or `~/go/bin` from `go install`. If you prefer
  the source route instead, `go install …@latest` is equivalent.
- Upgrade **odek first, then bodek**: bodek is only a front-end for odek's
  serve protocol, so it always benefits from the engine's new behaviour.
- `--check` is odek-only; bodek's upgrade has no flags.

---

## GLM 5.3 Flash on z.ai — quick reference

| Setting | Value |
|---|---|
| Model id | `glm-5.3-flash` (lowercase) |
| Context / max output | 1M tokens / 128K tokens |
| Coding Plan endpoint (OpenAI Chat Completions) | `https://api.z.ai/api/coding/paas/v4` |
| Pay-as-you-go endpoint | `https://api.z.ai/api/paas/v4` |
| Reasoning | Always on. Set depth with `--thinking low\|medium\|high` (odek maps these to `reasoning_effort`; GLM has no *medium* level, so odek maps it to *high*). Note: GLM-5.3 rejects `thinking: disabled` — odek translates that request to enabled + low effort automatically |
| Request timeout | Default is **300s** (`llm.request_timeout_seconds`). Raise further if reasoning is still slow to first byte. |

---

## Tuning for sub-agents

`delegate_tasks` runs sub-agents in parallel; the parent's own stream runs
alongside them. With the default `max_concurrency: 3` that's **4 concurrent
provider streams** — z.ai throttles around 5 concurrent streams and
429-saturates above that. If you see 429 errors on sub-agent-heavy runs:

```json
{
  "max_concurrency": 2
}
```

(Top-level key in `~/.odek/config.json`, or env `ODEK_MAX_CONCURRENCY`.)

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `failed to create sandbox container … hint: make sure Docker is running` | No Docker. Use `--no-sandbox` or `export ODEK_NO_SANDBOX=1` |
| `429` / rate-limit errors during sub-agent runs | Lower `max_concurrency` to `2` (or `1`) — z.ai throttles ~5 concurrent streams |
| Auth errors / empty key | Is `ZAI_API_KEY` in `~/.odek/secrets.env` (mode 0600) or the environment, and is `provider` `zai`? A project `./odek.json` can't carry keys |
| Slow first response / provider timeout | Expected: GLM-5.3 reasoning is always on. Defaults are 300s; raise `llm.request_timeout_seconds` and `llm.stream_idle_timeout_seconds` further if the model is still silent past that. |

---

## Where to go next

- [docs/CHEATSHEET.md](docs/CHEATSHEET.md) — every command and flag, one page
- [docs/CONFIG.md](docs/CONFIG.md) — full configuration reference
- [docs/CLI.md](docs/CLI.md) — command reference
- [docs/SUBAGENTS.md](docs/SUBAGENTS.md) — parallel sub-agents (`delegate_tasks`)
- [docs/SECURITY.md](docs/SECURITY.md) — approval system, sandbox, and the prompt-injection defenses you're benefiting from
- [bodek README](https://github.com/BackendStack21/bodek) — full TUI feature list
