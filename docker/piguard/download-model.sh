#!/usr/bin/env bash
# Download the PIGuard ONNX model and tokenizer into docker/piguard/models/.
# This is a one-time step required before the PIGuard sidecar can start in
# Docker Compose. The exported model is ~735 MB.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODEL_DIR="${SCRIPT_DIR}/models"
mkdir -p "${MODEL_DIR}"

if [ -f "${MODEL_DIR}/model.onnx" ] && [ -f "${MODEL_DIR}/tokenizer.json" ]; then
  echo "PIGuard model already present in ${MODEL_DIR}; nothing to do."
  exit 0
fi

PIGUARD_REF="${PIGUARD_REF:-v1.0.0}"

echo "Downloading and exporting PIGuard model (${PIGUARD_REF})..."
echo "Output directory: ${MODEL_DIR}"

# Run the export in a disposable Python container so no host Python/toolchain is
# required. The guard repo's export_onnx.py downloads the model from HuggingFace
# and converts it to ONNX.
#
# DEBIAN_FRONTEND=noninteractive keeps apt-get quiet on headless hosts.
# GIT_CONFIG_GLOBAL turns off the detached-HEAD advice and clone progress spam.
# HF_TRUST_REMOTE_CODE=1 allows the PIGuard model's custom modeling code to run
# without an interactive prompt. Pass through HF_TOKEN if set on the host.
DOCKER_ENVS=(
  -e HF_HOME=/tmp
  -e DEBIAN_FRONTEND=noninteractive
  -e GIT_CONFIG_GLOBAL=/tmp/.gitconfig
  -e HF_TRUST_REMOTE_CODE=1
)
if [ -n "${HF_TOKEN:-}" ]; then
  DOCKER_ENVS+=(-e "HF_TOKEN=${HF_TOKEN}")
fi

docker run --rm \
  -v "${MODEL_DIR}:/out" \
  "${DOCKER_ENVS[@]}" \
  python:3.12-slim bash -c "
    set -euo pipefail

    # Silence git advice / progress noise inside the container.
    printf '[advice]\ndetachedHead = false\n' > /tmp/.gitconfig

    echo '==> Installing git and curl inside container...'
    apt-get -qq update
    apt-get -qq install -y --no-install-recommends git curl ca-certificates >/dev/null

    echo '==> Cloning go-prompt-injection-guard (${PIGUARD_REF})...'
    git clone --quiet --depth 1 --branch '${PIGUARD_REF}' \
      https://github.com/BackendStack21/go-prompt-injection-guard.git /src

    cd /src

    # The PIGuard model uses custom HF modeling code. The upstream export script
    # does not pass trust_remote_code=True, so we patch it to avoid the interactive
    # y/N prompt that would hang in a non-TTY container.
    echo '==> Patching export script to allow custom HF modeling code...'
    sed -i 's/revision=MODEL_REVISION)/revision=MODEL_REVISION, trust_remote_code=True)/g' scripts/export_onnx.py

    echo '==> Installing Python export requirements (this may take a minute)...'
    pip install --root-user-action=ignore --disable-pip-version-check --no-cache-dir --quiet -r scripts/requirements.txt

    echo '==> Exporting PIGuard model from HuggingFace to ONNX (~735 MB, be patient)...'
    python -u scripts/export_onnx.py

    echo '==> Copying exported model to host volume...'
    cp ~/.cache/piguard/onnx/* /out/
    chmod 644 /out/*
  "

echo "Done. Model files written to ${MODEL_DIR}:"
ls -lh "${MODEL_DIR}"
