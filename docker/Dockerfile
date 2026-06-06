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

# ---- whisper stage ----
# Build whisper.cpp's CLI and fetch the `tiny` model so the `transcribe` tool
# (and Telegram voice auto-transcription) work out of the box — no host
# install, no first-run model download. Same alpine base as the runtime stage
# so the musl ABI matches; OpenMP is disabled to keep the runtime dependency
# surface down to just libstdc++. To ship a different model, override the build
# arg: `--build-arg WHISPER_MODEL=base` (tiny | base | small | medium) — size
# and RAM grow accordingly.
FROM alpine:latest AS whisper
ARG WHISPER_MODEL=tiny
RUN apk add --no-cache git cmake make g++ musl-dev curl
RUN git clone --depth 1 https://github.com/ggerganov/whisper.cpp /whisper
WORKDIR /whisper
RUN cmake -B build \
      -DCMAKE_BUILD_TYPE=Release \
      -DBUILD_SHARED_LIBS=OFF \
      -DGGML_OPENMP=OFF \
      -DWHISPER_BUILD_TESTS=OFF \
      -DWHISPER_BUILD_EXAMPLES=ON \
 && cmake --build build -j "$(nproc)" --target whisper-cli
# Fetch the ggml model into a fixed image path (NOT under ~/.odek, which the
# Telegram compose profiles bind-mount over — that would hide a model baked
# there). The runtime config points transcription.models_dir at this path.
RUN mkdir -p /models \
 && curl -fsSL -o "/models/ggml-${WHISPER_MODEL}.bin" \
      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-${WHISPER_MODEL}.bin"

# ---- runtime stage ----
FROM alpine:latest
# Tooling the agent commonly needs inside the sandbox container.
# Trim or extend this list to taste. git + the GitHub CLI (`gh`, from the
# Alpine community repo) are included so the agent can clone/PR/release.
# ffmpeg converts Telegram's OGG/Opus voice notes to WAV for whisper.cpp;
# libstdc++ is the only shared lib the bundled whisper-cli needs at runtime.
RUN apk add --no-cache ca-certificates git github-cli bash coreutils curl jq ffmpeg libstdc++

# Bundle the whisper CLI + model from the whisper stage so `transcribe` works
# with zero setup. whisper-cli lands on PATH; the model goes to a stable image
# path that the runtime config (transcription.models_dir) points at.
COPY --from=whisper /whisper/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=whisper /models/ /usr/local/share/whisper/models/

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

# Scheduled tasks ("cron") are handled natively by odek itself — the bot hosts
# an in-process scheduler, and `odek schedule daemon` runs one headless. No
# external cron binary is bundled; see docs/SCHEDULES.md.

# Run as a non-root user — defense in depth even inside the container.
# Pre-create ~/.odek owned by the user so it's writable for config, sessions,
# the Telegram lock, and schedules.json (whether backed by an image dir or a
# mounted folder).
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
