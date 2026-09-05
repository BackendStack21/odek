#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
EXPECTED_VERSION=${1:-}
EXPECTED_BODEK_VERSION=${2:-}

echo "==> source install (latest release tag)"
docker run --rm golang:1.25-alpine sh -euc '
  apk add --no-cache git >/dev/null
  TAG=$(git ls-remote --tags --sort=-v:refname \
    https://github.com/BackendStack21/odek.git \
    | awk '"'"'!/\^\{\}$/ {sub("refs/tags/", "", $2); print $2; exit}'"'"')
  TMP=$(mktemp -d)
  git clone --depth 1 --branch "${TAG}" \
    https://github.com/BackendStack21/odek.git "${TMP}/odek"
  (cd "${TMP}/odek" && GOBIN=/tmp/bin go install \
    -ldflags "-X main.version=${TAG}" ./cmd/odek)
  /tmp/bin/odek version | tee /tmp/version.txt
  if [ -n "$1" ]; then
    grep -F "$1" /tmp/version.txt >/dev/null
  fi
' sh "${EXPECTED_VERSION}"

echo "==> checksummed release install"
docker run --rm curlimages/curl:8.16.0 sh -euc '
  OS=$(uname -s | tr "[:upper:]" "[:lower:]")
  ARCH=$(uname -m | sed "s/x86_64/amd64/;s/aarch64/arm64/")
  ASSET="odek-${OS}-${ARCH}"
  TMP=$(mktemp -d)
  curl -fsSL -o "${TMP}/${ASSET}" \
    "https://github.com/BackendStack21/odek/releases/latest/download/${ASSET}"
  curl -fsSL -o "${TMP}/checksums.txt" \
    "https://github.com/BackendStack21/odek/releases/latest/download/checksums.txt"
  (cd "${TMP}" && grep "  ${ASSET}$" checksums.txt | sha256sum -c -)
  mkdir -p "${HOME}/.local/bin"
  install -m 755 "${TMP}/${ASSET}" "${HOME}/.local/bin/odek"
  "${HOME}/.local/bin/odek" version | tee /tmp/version.txt
  if [ -n "$1" ]; then
    grep -F "$1" /tmp/version.txt >/dev/null
  fi
' sh "${EXPECTED_VERSION}"

echo "==> checksummed bodek release install"
docker run --rm curlimages/curl:8.16.0 sh -euc '
  OS=$(uname -s | tr "[:upper:]" "[:lower:]")
  ARCH=$(uname -m | sed "s/x86_64/amd64/;s/aarch64/arm64/")
  LATEST=$(curl -fsSL -o /dev/null -w "%{url_effective}" \
    https://github.com/BackendStack21/bodek/releases/latest)
  TAG=${LATEST##*/}
  VERSION=${TAG#v}
  ASSET="bodek_${VERSION}_${OS}_${ARCH}.tar.gz"
  TMP=$(mktemp -d)
  curl -fsSL -o "${TMP}/${ASSET}" \
    "https://github.com/BackendStack21/bodek/releases/download/${TAG}/${ASSET}"
  curl -fsSL -o "${TMP}/checksums.txt" \
    "https://github.com/BackendStack21/bodek/releases/download/${TAG}/checksums.txt"
  (cd "${TMP}" && grep "  ${ASSET}$" checksums.txt | sha256sum -c -)
  tar -xzf "${TMP}/${ASSET}" -C "${TMP}" bodek
  mkdir -p "${HOME}/.local/bin"
  install -m 755 "${TMP}/bodek" "${HOME}/.local/bin/bodek"
  "${HOME}/.local/bin/bodek" version | tee /tmp/bodek-version.txt
  if [ -n "$1" ]; then
    grep -F "$1" /tmp/bodek-version.txt >/dev/null
  fi
' sh "${EXPECTED_BODEK_VERSION}"

echo "==> current checkout config initialization"
docker run --rm \
  -v "${ROOT}:/src:ro" \
  -w /src \
  golang:1.25-alpine sh -euc '
    go build -ldflags "-X main.version=e2e" -o /tmp/odek ./cmd/odek
    export HOME=/tmp/home

    /tmp/odek init --global
    GLOBAL="${HOME}/.odek/config.json"
    test -f "${GLOBAL}"
    test "$(stat -c %a "${GLOBAL}")" = "600"
    grep -F "\"provider\": \"deepseek\"" "${GLOBAL}" >/dev/null
    BEFORE=$(sha256sum "${GLOBAL}")
    /tmp/odek init --global 2>&1 | grep -F "already exists"
    test "$(sha256sum "${GLOBAL}")" = "${BEFORE}"

    mkdir -p /tmp/project
    cd /tmp/project
    /tmp/odek init --local
    test -f odek.json
    test "$(stat -c %a odek.json)" = "600"
    grep -F "\"model\": \"\"" odek.json >/dev/null
    ! grep -F "\"provider\":" odek.json >/dev/null

    /tmp/odek version | grep -F "e2e"
  '

echo "getting-started Docker E2E: PASS"
