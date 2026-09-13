#!/bin/sh
# odek installer — downloads prebuilt release binaries from GitHub.
#
# Usage:
#   curl -fsSL https://odek.21no.de/install.sh | sh
#   sh install.sh [--dry-run]
#
# macOS and Linux. Windows users: grab a binary from the releases page.
#
# Installs odek for the current platform into /usr/local/bin (or
# ~/.local/bin when /usr/local/bin is not writable). Verifies the download
# against the release checksums.txt and refuses to install anything it
# cannot verify (fail closed).
set -eu

ODEK_REPO="BackendStack21/odek"
GH_DL="https://github.com"

DRY_RUN=""
say()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- platform
OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) die "unsupported OS '$OS' — this installer covers macOS and Linux." ;;
esac
case "$ARCH" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture '$ARCH'." ;;
esac

command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 \
  || die "need curl or wget to download."

fetch() { # fetch <url> -> stdout
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"
  else wget -qO- "$1"; fi
}
download() { # download <url> <dest>
  if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$2" "$1"
  else wget -qO "$2" "$1"; fi
}

sha256_hex() { # sha256_hex <file> -> hex digest (empty when no tool)
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    printf ''
  fi
}

checksum() { # checksum <file> <expected-hex>
  actual=$(sha256_hex "$1")
  if [ -z "$actual" ]; then
    # Fail closed: without a sha256 tool we cannot verify the download, and
    # unverified code does not go on the PATH.
    die "no sha256 tool found (sha256sum/shasum) — refusing to install unverified."
  fi
  [ "$actual" = "$2" ] || die "checksum mismatch for $(basename "$1") — download corrupted or tampered. Aborting."
}

# ---------------------------------------------------------------- install dir
install_dir=""
pick_install_dir() {
  for d in /usr/local/bin "$HOME/.local/bin"; do
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then install_dir=$d; return; fi
  done
  die "no writable install directory found (tried /usr/local/bin, ~/.local/bin)."
}

path_has_dir() { case ":$PATH:" in *":$1:"*) return 0 ;; *) return 1 ;; esac; }

# ---------------------------------------------------------------- releases
latest_tag() { # latest_tag <repo> -> "vX.Y.Z" (empty on failure)
  # Resolve via the redirect of /releases/latest — no API rate limits.
  url="$GH_DL/$1/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    loc=$(curl -fsSI -o /dev/null -w '%{redirect_url}' "$url" 2>/dev/null || true)
  else
    loc=$(wget -qS --spider "$url" 2>&1 | sed -n 's/^ *Location: *//p' | tail -1)
  fi
  case "$loc" in
    */tag/*) printf '%s\n' "${loc##*/}" ;;
    *) printf '' ;;
  esac
}

checksum_line() { # checksum_line <repo> <tag> <asset> -> hex digest or ""
  fetch "$GH_DL/$1/releases/download/$2/checksums.txt" 2>/dev/null \
    | awk -v a="$3" '$2 == a {print $1; exit}'
}

TMP=$(mktemp -d) || die "cannot create temp dir."
trap 'rm -rf "$TMP"' EXIT INT TERM

install_asset() { # install_asset <repo> <tag> <asset-file> <binary-name>
  repo=$1 tag=$2 asset=$3 bin=$4
  say "Downloading $asset ($tag)"
  if [ -n "$DRY_RUN" ]; then
    say "dry run: would download $GH_DL/$repo/releases/download/$tag/$asset to $install_dir/$bin"
    return
  fi
  download "$GH_DL/$repo/releases/download/$tag/$asset" "$TMP/$asset"
  digest=$(checksum_line "$repo" "$tag" "$asset")
  if [ -n "$digest" ]; then
    checksum "$TMP/$asset" "$digest"
    say "Checksum verified."
  else
    # Fail closed: a missing/unfetchable checksum line is suspicious, not a
    # reason to install unverified code.
    die "no checksum entry for $asset — refusing to install unverified."
  fi
  cp "$TMP/$asset" "$TMP/$bin" && chmod +x "$TMP/$bin"
  [ -f "$TMP/$bin" ] || die "binary '$bin' not found."
  mv -f "$TMP/$bin" "$install_dir/$bin"
  chmod +x "$install_dir/$bin"
  say "Installed $install_dir/$bin"
}

# ---------------------------------------------------------------- main
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) if [ -f "$0" ]; then sed -n '2,9p' "$0"; else
                 echo 'usage: sh install.sh [--dry-run]'; fi
               exit 0 ;;
    *) die "unknown option '$arg' (supported: --dry-run)" ;;
  esac
done

if command -v odek >/dev/null 2>&1; then
  current=$(odek version 2>/dev/null || echo unknown)
  warn "odek already installed: $(command -v odek) ($current) — not overwriting."
  say "To update, run: odek upgrade"
  exit 0
fi

pick_install_dir

tag=$(latest_tag "$ODEK_REPO")
[ -n "$tag" ] || die "cannot resolve the latest odek release (network or GitHub problem — install curl if it is missing)."
install_asset "$ODEK_REPO" "$tag" "odek-$os-$arch" odek

if [ -n "$DRY_RUN" ]; then
  say "dry run: no files were downloaded or installed."
  exit 0
fi

if ! path_has_dir "$install_dir"; then
  cat <<EOF

NOTE: $install_dir is not on your PATH. Add it:

  echo 'export PATH="$install_dir:\$PATH"' >> ~/.profile && source ~/.profile
EOF
fi

cat <<EOF

Odek is installed. Next, set up your provider:

  1. odek init --global             # creates ~/.odek/config.json
  2. Add your provider API key, e.g.:
       "providers": { "openai": { "apiKey": "sk-..." } }
  3. odek run "your first task"
     Full guide: https://github.com/BackendStack21/odek/blob/main/GETTING_STARTED.md
EOF
