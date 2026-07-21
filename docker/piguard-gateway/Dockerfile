# syntax=docker/dockerfile:1

# Vendored HTTP gateway for the PIGuard daemon. Replaces the upstream
# examples/http-gateway (a raw-text demo) with a bridge that forwards odek's
# JSON protocol verbatim to the daemon socket. Base images are intentionally
# NOT pinned by digest so builds resolve to the host architecture (the
# upstream amd64-pinned digests produce x86-64 binaries inside arm64-labeled
# images on Apple Silicon).

# ---- build stage ----
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/piguard-gateway .

# ---- runtime stage ----
# Runs as root (distroless default) on purpose: the daemon creates its socket
# owned by its own non-root user, and the gateway must be able to connect.
# The upstream example gateway does the same. The container is unprivileged
# otherwise and only reachable on the private compose network.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/piguard-gateway /usr/local/bin/piguard-gateway
ENTRYPOINT ["piguard-gateway"]
CMD ["--addr=:8080", "--socket=/run/piguard/piguard.sock"]
