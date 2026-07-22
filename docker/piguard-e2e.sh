#!/usr/bin/env bash
# Local E2E runner for the PIGuard prompt-injection sidecar.
#
# Builds (if needed) and starts the daemon + gateway exactly like the
# docker-compose stack, then runs the env-gated E2E test in
# internal/guard/piguard_e2e_test.go against them, and tears everything
# down afterwards.
#
# Why a script and not CI: the stack is heavy (a ~735 MB ONNX model plus
# two image builds), which made the GitHub Actions job too slow to keep.
# Run this before merging changes that touch internal/guard or the
# docker piguard stack.
#
# Requirements: Docker, Go. First run needs the model exported once:
#   docker/piguard/download-model.sh
#
# Usage:
#   docker/piguard-e2e.sh            build images if missing, run E2E, tear down
#   docker/piguard-e2e.sh --build    force image rebuild
#   docker/piguard-e2e.sh --linux    also run the test binary inside a Linux
#                                    container (full socket-mode coverage;
#                                    on macOS the host socket subtest skips
#                                    because unix sockets do not cross the
#                                    Docker Desktop VM boundary)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER_DIR="${REPO_ROOT}/docker"
MODEL_DIR="${DOCKER_DIR}/piguard/models"
SOCK_DIR="/tmp/piguard-e2e"
NETWORK="piguard-e2e"
BUILD=0
LINUX=0
for arg in "$@"; do
  case "$arg" in
    --build) BUILD=1 ;;
    --linux) LINUX=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

cleanup() {
  docker rm -f piguard-e2e-gateway piguard-e2e-daemon >/dev/null 2>&1 || true
  docker network rm "${NETWORK}" >/dev/null 2>&1 || true
  rm -rf "${SOCK_DIR}"
}
trap cleanup EXIT

docker info >/dev/null 2>&1 || { echo "docker daemon is not running" >&2; exit 1; }

if [ ! -f "${MODEL_DIR}/model.onnx" ]; then
  echo "PIGuard model not found in ${MODEL_DIR}." >&2
  echo "Run ${DOCKER_DIR}/piguard/download-model.sh first (one-time, ~735 MB)." >&2
  exit 1
fi

if [ "${BUILD}" = 1 ] || ! docker image inspect piguard:local >/dev/null 2>&1; then
  (cd "${DOCKER_DIR}" && docker compose --profile restricted build piguard)
fi
if [ "${BUILD}" = 1 ] || ! docker image inspect piguard-gateway:local >/dev/null 2>&1; then
  (cd "${DOCKER_DIR}" && docker compose --profile restricted build piguard-gateway)
fi

cleanup # remove leftovers from a previous run
mkdir -p "${SOCK_DIR}"
docker network create "${NETWORK}" >/dev/null
docker run -d --name piguard-e2e-daemon --network "${NETWORK}" \
  -v "${MODEL_DIR}:/models:ro" \
  -v "${SOCK_DIR}:/run/piguard" \
  piguard:local \
  --socket=/run/piguard/piguard.sock --model-dir=/models \
  --max-batch=32 --batch-wait=5ms >/dev/null
docker run -d --name piguard-e2e-gateway --network "${NETWORK}" \
  -p 127.0.0.1:18080:8080 \
  -v "${SOCK_DIR}:/run/piguard" \
  piguard-gateway:local \
  --addr=:8080 --socket=/run/piguard/piguard.sock >/dev/null

echo "Waiting for gateway health..."
healthy=0
for _ in $(seq 1 90); do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "${healthy}" != 1 ]; then
  echo "=== daemon logs ===" >&2; docker logs piguard-e2e-daemon >&2 || true
  echo "=== gateway logs ===" >&2; docker logs piguard-e2e-gateway >&2 || true
  echo "gateway never became healthy" >&2
  exit 1
fi

export ODEK_E2E_GUARD=1
export PIGUARD_URL="http://127.0.0.1:18080/detect"
export PIGUARD_SOCKET="${SOCK_DIR}/piguard.sock"

echo "Running guard E2E (host)..."
(cd "${REPO_ROOT}" && go test ./internal/guard -run E2E -count=1 -v)

if [ "${LINUX}" = 1 ]; then
  echo "Running guard E2E inside a Linux container (socket mode covered)..."
  (cd "${REPO_ROOT}" && CGO_ENABLED=0 GOOS=linux go test -c -o /tmp/piguard-e2e/guard.test ./internal/guard)
  docker run --rm --network "${NETWORK}" \
    -e ODEK_E2E_GUARD=1 \
    -e PIGUARD_URL="http://piguard-e2e-gateway:8080/detect" \
    -e PIGUARD_SOCKET=/run/piguard/piguard.sock \
    -v "${SOCK_DIR}:/run/piguard" \
    -v /tmp/piguard-e2e:/out \
    debian:bookworm-slim /out/guard.test -test.run E2E -test.v
fi

echo "PIGuard E2E passed."
