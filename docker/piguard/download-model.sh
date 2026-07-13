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
docker run --rm \
  -v "${MODEL_DIR}:/out" \
  -e HF_HOME=/tmp \
  python:3.12-slim bash -c "
    set -euo pipefail
    apt-get update >/dev/null
    apt-get install -y --no-install-recommends git curl ca-certificates >/dev/null
    git clone --depth 1 --branch '${PIGUARD_REF}' \
      https://github.com/BackendStack21/go-prompt-injection-guard.git /src
    cd /src
    pip install --no-cache-dir -r scripts/requirements.txt >/dev/null
    python scripts/export_onnx.py
    cp ~/.cache/piguard/onnx/* /out/
  "

echo "Done. Model files written to ${MODEL_DIR}:"
ls -lh "${MODEL_DIR}"
