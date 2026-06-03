# syntax=docker/dockerfile:1
#
# Build context MUST be the repository root (see docker-compose.yml:
#   build: { context: .., dockerfile: docker/Dockerfile }).

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
# Trim or extend this list to taste. git + the GitHub CLI (`gh`, from the
# Alpine community repo) are included so the agent can clone/PR/release.
RUN apk add --no-cache ca-certificates git github-cli bash coreutils curl jq

# ── Adding extra dependencies the agent can use ──────────────────────────
# The agent runs shell commands INSIDE this image, so any runtime or CLI it
# should call must be installed here. Uncomment what you need, then rebuild
# with `docker compose --profile <name> up --build`.
#
# IMPORTANT: keep all installs ABOVE the `USER odek` line below — apk, npm,
# and the Bun installer need root.
#
# Alpine packages (musl libc — small and fast):
#   RUN apk add --no-cache nodejs npm            # Node.js + npm
#   RUN apk add --no-cache python3 py3-pip        # Python 3 + pip
#   RUN apk add --no-cache go                      # Go toolchain
#   RUN apk add --no-cache make gcc g++ musl-dev   # build-essential equivalents
#   RUN apk add --no-cache ripgrep fd jq yq        # search / JSON-YAML tools
#
# JS package managers (once npm is present):
#   RUN npm install -g pnpm yarn
#
# Bun (official installer; needs bash + unzip; BUN_INSTALL puts it on PATH).
# Uses Bun's musl build on Alpine; libstdc++ is required at runtime:
#   RUN apk add --no-cache bash unzip libstdc++ \
#    && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash
#   # → installs /usr/local/bin/bun
#
# ── Heavy / glibc-only toolchains: switch to a Debian base ───────────────
# If a tool won't run on Alpine's musl, replace `FROM alpine:latest` above
# with a Debian-based image and use apt-get instead:
#
#   FROM debian:bookworm-slim
#   RUN apt-get update && apt-get install -y --no-install-recommends \
#         ca-certificates git bash curl jq python3 python3-pip nodejs npm \
#    && rm -rf /var/lib/apt/lists/*
#
# …or start from an official language image that already bundles the runtime:
#   FROM node:20-bookworm-slim     # Node.js + npm preinstalled
#   FROM python:3.12-slim          # Python preinstalled
#
# On a Debian base, the user-creation line below differs — replace
#   `adduser -D -u 1000 odek`  with  `useradd -m -u 1000 odek`
# (the mkdir/chown, ENV HOME, USER, and WORKDIR lines all work unchanged).

# ── supercronic — container-friendly cron for scheduled `odek run` jobs ──
# Why not busybox crond: crond launches jobs with a SCRUBBED environment, so
# env_file vars (ODEK_API_KEY, ODEK_TELEGRAM_BOT_TOKEN, …) never reach a cron
# tick — and it wants root to setuid, clashing with the non-root user below.
# supercronic runs as a normal user and passes ITS OWN environment through to
# each job, so `odek run --deliver` from cron sees the same vars as the bot.
#
# Pinned to a release + SHA-256 computed from the official GitHub assets, so a
# tampered or substituted binary fails the build. TARGETARCH is supplied by
# BuildKit (this Dockerfile already opts in via the syntax= directive above).
ARG SUPERCRONIC_VERSION=v0.2.46
ARG TARGETARCH
RUN set -eu; \
    arch="${TARGETARCH:?TARGETARCH is empty — build with BuildKit (docker buildx build) or pass --build-arg TARGETARCH=amd64|arm64}"; \
    case "$arch" in \
      amd64) sha=5adff01c5a797663948e656d2b61d10932369ee437eb5cb54fa872b2960f222b ;; \
      arm64) sha=c0576a8eb092e3f79108ed0a2155a25c7766af78456e5a6070e54757ef513bfe ;; \
      *) echo "supercronic: unsupported TARGETARCH=$arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/aptible/supercronic/releases/download/${SUPERCRONIC_VERSION}/supercronic-linux-${arch}" \
      -o /usr/local/bin/supercronic; \
    echo "${sha}  /usr/local/bin/supercronic" | sha256sum -c -; \
    chmod +x /usr/local/bin/supercronic

# Run as a non-root user — defense in depth even inside the container.
# Pre-create ~/.odek owned by the user so it's writable for config, sessions,
# and the Telegram lock (whether backed by an image dir or a mounted folder).
# ~/.crontabs holds the (optional) bind-mounted crontab read by supercronic.
RUN adduser -D -u 1000 odek \
 && mkdir -p /home/odek/.odek /home/odek/.crontabs /workspace \
 && chown -R odek:odek /home/odek/.odek /home/odek/.crontabs /workspace

COPY --from=build /out/odek /usr/local/bin/odek
COPY --chmod=0755 docker/cron-entrypoint.sh /usr/local/bin/cron-entrypoint.sh

# Docker does NOT set $HOME from USER, but Odek resolves ~/.odek via $HOME.
# Set it explicitly so config.json, sessions, and the Telegram lock land in
# /home/odek/.odek (where the volume and config bind mounts are).
ENV HOME=/home/odek
USER odek
WORKDIR /workspace

# The wrapper starts supercronic in the background IFF a crontab is mounted,
# then `exec`s odek — so services without a crontab behave exactly as before
# (odek stays the container's main process; signals and the singleton lock are
# unchanged). The compose `command:` (serve/telegram/run …) flows through as $@.
ENTRYPOINT ["/usr/local/bin/cron-entrypoint.sh"]
