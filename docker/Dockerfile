# syntax=docker/dockerfile:1
#
# Build context MUST be the repository root (see docker-compose.yml:
#   build: { context: .., dockerfile: docker/Dockerfile }).

# ---- build stage ----
# Stays on Alpine: the binary is built with CGO_ENABLED=0 (fully static), so it
# runs unchanged on the Debian runtime stage below.
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
# install, no first-run model download. Same Debian base as the runtime stage
# so the glibc ABI matches; OpenMP is disabled to keep the runtime dependency
# surface down to just libstdc++6. To ship a different model, override the build
# arg: `--build-arg WHISPER_MODEL=base` (tiny | base | small | medium) — size
# and RAM grow accordingly. WHISPER_VERSION pins the whisper.cpp release so the
# build is reproducible — bump it deliberately rather than tracking master.
FROM debian:bookworm-slim AS whisper
ARG WHISPER_MODEL=tiny
ARG WHISPER_VERSION=v1.8.6
RUN apt-get update && apt-get install -y --no-install-recommends \
      git cmake make g++ curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "${WHISPER_VERSION}" https://github.com/ggerganov/whisper.cpp /whisper
WORKDIR /whisper
# GGML_NATIVE=OFF: don't pass -mcpu=native. On Debian's GCC 12 the native
# arm64 path enables FP16 NEON intrinsics (vfmaq_f16) that fail to inline
# ("target specific option mismatch") when the host CPU's fp16 feature isn't
# resolved under Docker. A fixed baseline arch sidesteps that, builds on both
# amd64 and arm64, and keeps the image reproducible (no host-CPU dependence).
RUN cmake -B build \
      -DCMAKE_BUILD_TYPE=Release \
      -DBUILD_SHARED_LIBS=OFF \
      -DGGML_OPENMP=OFF \
      -DGGML_NATIVE=OFF \
      -DWHISPER_BUILD_TESTS=OFF \
      -DWHISPER_BUILD_EXAMPLES=ON \
 && cmake --build build -j "$(nproc)" --target whisper-cli
# Fetch the ggml model into a fixed image path (NOT under ~/.odek, which the
# Telegram compose profiles bind-mount over — that would hide a model baked
# there). The runtime config points transcription.models_dir at this path.
RUN mkdir -p /models \
 && curl -fsSL -o "/models/ggml-${WHISPER_MODEL}.bin" \
      "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-${WHISPER_MODEL}.bin"

# ---- minicpm-v stage ----
# Downloads a pre-built llama-mtmd-cli binary and fetches MiniCPM-V 4.6 model
# files (main GGUF + vision projector) so the `vision` tool works out of the
# box with zero host setup. Pre-built binaries from the official llama.cpp
# release avoid a multi-minute C++ compile inside Docker.
#
# Size added to the runtime image: ~530 MB model + ~1.1 GB mmproj ≈ 1.6 GB.
# To use a different quantization level: --build-arg MINICPM_QUANT=Q8_0
# Available quants: Q4_0 (501 MB) | Q4_K_M (529 MB, default) | Q8_0 (812 MB)
# LLAMA_VERSION pins the llama.cpp release — bump it deliberately.
FROM debian:bookworm-slim AS minicpm
ARG MINICPM_QUANT=Q4_K_M
ARG LLAMA_VERSION=b9549
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
# Map dpkg architecture to the llama.cpp release asset name suffix
# (amd64 → x64, arm64 → arm64). Extract the CLI binary and all bundled
# shared libraries (.so*) — the pre-built Ubuntu binaries are dynamically
# linked against libllama.so, libmtmd.so, etc. shipped in the same tarball.
RUN set -ex \
 && LLAMA_ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
 && curl -fsSL \
      "https://github.com/ggerganov/llama.cpp/releases/download/${LLAMA_VERSION}/llama-${LLAMA_VERSION}-bin-ubuntu-${LLAMA_ARCH}.tar.gz" \
      -o /tmp/llama.tar.gz \
 && mkdir -p /tmp/llama-bin \
 && tar -xzf /tmp/llama.tar.gz -C /tmp/llama-bin \
 && find /tmp/llama-bin -name "llama-mtmd-cli" -exec install -m 755 {} /usr/local/bin/llama-mtmd-cli \; \
 && mkdir -p /usr/local/lib/llama \
 && find /tmp/llama-bin -name "*.so*" -exec cp -P {} /usr/local/lib/llama/ \; \
 && rm -rf /tmp/llama.tar.gz /tmp/llama-bin
# Fetch the model GGUF and vision projector into a fixed image path (NOT under
# ~/.odek — bind-mount profiles would hide files baked there). The runtime
# config points vision.models_dir at this path, or the tool auto-detects it.
RUN mkdir -p /usr/local/share/minicpm-v/models \
 && curl -fsSL \
      "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/MiniCPM-V-4_6-${MINICPM_QUANT}.gguf" \
      -o /usr/local/share/minicpm-v/models/model.gguf \
 && curl -fsSL \
      "https://huggingface.co/openbmb/MiniCPM-V-4_6-gguf/resolve/78e02f066e9819a60573b78a4275df8a0c27f698/mmproj-model-f16.gguf" \
      -o /usr/local/share/minicpm-v/models/mmproj.gguf

# ---- runtime stage ----
FROM debian:bookworm-slim
# Tooling the agent commonly needs inside the sandbox container.
# Trim or extend this list to taste. git + the GitHub CLI (`gh`) are included so
# the agent can clone/PR/release. ffmpeg converts Telegram's OGG/Opus voice
# notes to WAV for whisper.cpp; libstdc++6 is the only shared lib the bundled
# whisper-cli needs at runtime.
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git bash coreutils curl jq ffmpeg libstdc++6 \
 && rm -rf /var/lib/apt/lists/*

# GitHub CLI (`gh`) is not in Debian's default repos — add the official apt
# repository and install it.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

# Bundle the whisper CLI + model from the whisper stage so `transcribe` works
# with zero setup. whisper-cli lands on PATH; the model goes to a stable image
# path that the runtime config (transcription.models_dir) points at.
COPY --from=whisper /whisper/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=whisper /models/ /usr/local/share/whisper/models/

# Bundle llama-mtmd-cli + MiniCPM-V 4.6 from the minicpm stage so `vision`
# works with zero setup. The tool auto-detects /usr/local/share/minicpm-v/models.
# The shared libraries (libllama.so, libmtmd.so, …) are registered via ldconfig
# so the dynamically-linked binary resolves them at runtime.
COPY --from=minicpm /usr/local/bin/llama-mtmd-cli /usr/local/bin/llama-mtmd-cli
COPY --from=minicpm /usr/local/lib/llama/ /usr/local/lib/llama/
COPY --from=minicpm /usr/local/share/minicpm-v/models/ /usr/local/share/minicpm-v/models/
RUN echo "/usr/local/lib/llama" > /etc/ld.so.conf.d/llama.conf && ldconfig

# ── Adding extra dependencies the agent can use ──────────────────────────
# The agent runs shell commands INSIDE this image, so any runtime or CLI it
# should call must be installed here. Trim or extend to taste, then rebuild
# with `docker compose --profile <name> up --build`.
#
# IMPORTANT: keep all installs ABOVE the `USER odek` line below — apt and the
# Bun installer need root.

# Extra agent toolchains: Go, Python 3, and Bun.
#   - Python 3 comes from Debian's apt (3.11 on bookworm); python3-venv lets the
#     agent create virtualenvs since system pip is externally managed (PEP 668).
#   - Go is installed from the official tarball so the agent gets a current
#     toolchain (Debian's golang-go lags badly). Override with --build-arg
#     GO_VERSION=x.y.z. dpkg --print-architecture yields amd64/arm64, which
#     match Go's release naming.
#   - Bun uses its official installer (glibc build); it needs unzip. BUN_INSTALL
#     puts bun on PATH at /usr/local/bin/bun.
ARG GO_VERSION=1.25.4
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 python3-pip python3-venv unzip \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
      | tar -C /usr/local -xz \
 && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash
ENV PATH=/usr/local/go/bin:$PATH

# Scheduled tasks ("cron") are handled natively by odek itself — the bot hosts
# an in-process scheduler, and `odek schedule daemon` runs one headless. No
# external cron binary is bundled; see docs/SCHEDULES.md.

# Run as a non-root user — defense in depth even inside the container.
# Pre-create ~/.odek owned by the user so it's writable for config, sessions,
# the Telegram lock, and schedules.json (whether backed by an image dir or a
# mounted folder).
RUN useradd -m -u 1000 odek \
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
