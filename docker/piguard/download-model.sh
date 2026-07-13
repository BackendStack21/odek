#!/usr/bin/env bash
# Download the PIGuard ONNX model and tokenizer into docker/piguard/models/.
# This is a one-time step required before the PIGuard sidecar can start in
# Docker Compose. The exported model is ~735 MB.
#
# The export script in this directory is derived from the go-prompt-injection-guard
# export script with trust_remote_code=True added so it can run non-interactively.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODEL_DIR="${SCRIPT_DIR}/models"
EXPORT_SCRIPT="${SCRIPT_DIR}/export_onnx.py"
mkdir -p "${MODEL_DIR}"

if [ -f "${MODEL_DIR}/model.onnx" ] && [ -f "${MODEL_DIR}/tokenizer.json" ]; then
  echo "PIGuard model already present in ${MODEL_DIR}; nothing to do."
  exit 0
fi

echo "Downloading and exporting PIGuard model into ${MODEL_DIR}..."

# Run the export in a disposable Python container so no host Python/toolchain is
# required. The export script downloads the model from HuggingFace and converts
# it to ONNX.
#
# DEBIAN_FRONTEND=noninteractive keeps apt-get quiet on headless hosts.
# HF_TRUST_REMOTE_CODE=1 is belt-and-suspenders. Pass HF_TOKEN through if set
# on the host for higher HF rate limits.
DOCKER_ENVS=(
  -e HF_HOME=/tmp
  -e DEBIAN_FRONTEND=noninteractive
  -e HF_TRUST_REMOTE_CODE=1
)
if [ -n "${HF_TOKEN:-}" ]; then
  DOCKER_ENVS+=(-e "HF_TOKEN=${HF_TOKEN}")
fi

# Pinned versions from go-prompt-injection-guard/scripts/requirements.txt.
PYTHON_REQS="torch==2.12.0 transformers==5.10.2 onnxscript==0.7.0"

docker run --rm \
  -v "${MODEL_DIR}:/out" \
  -v "${EXPORT_SCRIPT}:/src/export_onnx.py:ro" \
  "${DOCKER_ENVS[@]}" \
  python:3.12-slim bash -c "
    set -euo pipefail

    echo '==> Installing Python export requirements (~2 GB, this may take several minutes)...'
    pip install --root-user-action=ignore --disable-pip-version-check --no-cache-dir --quiet ${PYTHON_REQS}

    echo '==> Exporting PIGuard model from HuggingFace to ONNX (~735 MB, be patient)...'
    python -u /src/export_onnx.py

    echo '==> Copying exported model to host volume...'
    cp ~/.cache/piguard/onnx/* /out/
    chmod 644 /out/*
  "

echo "Done. Model files written to ${MODEL_DIR}:"
ls -lh "${MODEL_DIR}"
