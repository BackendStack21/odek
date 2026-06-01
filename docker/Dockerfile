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
# Trim or extend this list to taste.
RUN apk add --no-cache ca-certificates git bash coreutils curl jq

# Run as a non-root user — defense in depth even inside the container.
# Pre-create ~/.odek owned by the user so a mounted named volume (used for
# Telegram session state) inherits uid 1000 ownership and is writable.
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
