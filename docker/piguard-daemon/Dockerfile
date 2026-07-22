# syntax=docker/dockerfile:1
#
# Vendored, architecture-safe build of the PIGuard prompt-injection daemon.
#
# Why vendored: the upstream Dockerfile
# (https://github.com/BackendStack21/go-prompt-injection-guard/blob/v1.0.0/Dockerfile)
# pins its base images by amd64-only sha256 digests, so on Apple Silicon Macs
# the build produces x86-64 binaries (piguard-server, libonnxruntime.so) inside
# an arm64-labeled image, and the container crash-loops with:
#   rosetta error: failed to open elf at /lib64/ld-linux-x86-64.so.2
# This Dockerfile builds the same pinned upstream source (v1.0.0) but uses
# unpinned base image tags, which resolve to the host architecture. The upstream
# Makefile is arch-aware (it selects the aarch64 ONNX Runtime when
# GOARCH=arm64), so the resulting image contains native binaries. Note: the
# upstream Makefile has no checksum entries for linux-arm64, so it prints
# WARNINGs during the build — that is expected and harmless.

# ---- build stage ----
# cgo is required: the tokenizer bindings link a static library and ONNX Runtime
# is loaded at runtime. A glibc base is required (the prebuilt libtokenizers.a is
# a GNU build), so this uses debian-based images rather than Alpine.
FROM golang:1.26-bookworm AS builder

# Fetch the pinned upstream release.
RUN git clone --depth 1 --branch v1.0.0 \
    https://github.com/BackendStack21/go-prompt-injection-guard.git /src
WORKDIR /src

# Fetch native libs (tokenizers static + ONNX Runtime shared) and build both
# binaries.
RUN make libtokenizers libonnxruntime build

# ---- runtime stage ----
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /src/bin/piguard            /usr/local/bin/piguard
COPY --from=builder /src/bin/piguard-server     /usr/local/bin/piguard-server
COPY --from=builder /src/lib/libonnxruntime.so  /usr/local/lib/libonnxruntime.so

# The tokenizer code is linked statically; only ONNX Runtime is loaded at
# runtime, located via ORT_DYLIB_PATH.
ENV ORT_DYLIB_PATH=/usr/local/lib/libonnxruntime.so

# Run as a non-root user. /run/piguard holds the Unix socket (share it with
# clients via a volume); /models holds the exported model (mount it read-only).
RUN useradd --system --uid 10001 piguard \
    && mkdir -p /run/piguard /models \
    && chown -R piguard /run/piguard
USER piguard

ENTRYPOINT ["piguard-server"]
CMD ["--socket", "/run/piguard/piguard.sock", "--model-dir", "/models"]
