#!/usr/bin/env bash
# ── generate-changelog.sh ──────────────────────────────────────────
# Generate CHANGELOG.md entries from conventional git commits.
# Deprecates manual editing of docs/CHANGELOG.md.
#
# Usage:
#   # Generate for unreleased changes (--bump patch/minor/major)
#   ./generate-changelog.sh --bump patch                  # last tag +1 → stdout
#   ./generate-changelog.sh --bump patch --prepend         # ...and prepend to CHANGELOG
#
#   # Generate for a specific range
#   ./generate-changelog.sh --from v0.57.0 --to v0.58.0
#
#   # Release notes for gh release create
#   ./generate-changelog.sh --bump patch --notes > /tmp/notes.md
#   gh release create v0.58.7 --notes-file /tmp/notes.md
#
# Commit convention and section mapping:
#   feat|feature:  → ### Features
#   fix|bugfix:    → ### Bug Fixes
#   perf:          → ### Performance
#   refactor:      → ### Refactoring
#   docs:          → ### Documentation
#   test|tests:    → ### Testing
#   chore|build|ci:→ ### Infrastructure
#   (unmatched)    → ### Other Changes
# ──────────────────────────────────────────────────────────────────────
set -euo pipefail

CHANGELOG_FILE="docs/CHANGELOG.md"
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── Parse args ────────────────────────────────────────────────────
FROM_TAG=""
TO_TAG=""
BUMP=""
MODE="stdout"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from|--since) FROM_TAG="$2"; shift 2 ;;
    --to)           TO_TAG="$2";   shift 2 ;;
    --bump)         BUMP="$2";     shift 2 ;;
    --prepend)      MODE="prepend"; shift ;;
    --notes)        MODE="notes";  shift ;;
    *) echo "Unknown: $1"; exit 1 ;;
  esac
done

cd "$REPO_DIR"

# ── Resolve tags ──────────────────────────────────────────────────
if [[ -z "$FROM_TAG" ]]; then
  FROM_TAG=$(git tag --sort=-v:refname | head -1)
  if [[ -z "$FROM_TAG" ]]; then
    echo "Error: no tags found. Use --from to specify a starting tag." >&2
    exit 1
  fi
fi

if [[ -n "$BUMP" ]]; then
  # Auto-bump FROM_TAG by patch/minor/major
  # Strip leading 'v' if present, bump, then re-add
  BASE="${FROM_TAG#v}"
  IFS='.' read -r MAJ MIN PATCH <<< "$BASE"
  case "$BUMP" in
    patch) TO_TAG="v${MAJ}.${MIN}.$((PATCH + 1))" ;;
    minor) TO_TAG="v${MAJ}.$((MIN + 1)).0" ;;
    major) TO_TAG="v$((MAJ + 1)).0.0" ;;
    *) echo "Error: --bump must be patch, minor, or major (got: $BUMP)" >&2; exit 1 ;;
  esac
fi

RANGE="${FROM_TAG}..${TO_TAG:-HEAD}"

# ── Parse commits ─────────────────────────────────────────────────
FEATURES=""; BUGFIXES=""; PERFORMANCE=""; REFACTORING=""
DOCUMENTATION=""; TESTING=""; INFRASTRUCTURE=""; OTHER=""

while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  [[ "$line" == "Merge "* ]] && continue

  if echo "$line" | grep -qE '^[a-zA-Z]+(\\([^)]*\\))?: '; then
    local_part="${line%%(*}"
    TYPE="${local_part%%:*}"
    DESC="${line#*: }"
  else
    TYPE="other"
    DESC="$line"
  fi

  case "$TYPE" in
    feat|feature)  FEATURES="${FEATURES}- ${DESC}"$'\n' ;;
    fix|bugfix)    BUGFIXES="${BUGFIXES}- ${DESC}"$'\n' ;;
    perf)          PERFORMANCE="${PERFORMANCE}- ${DESC}"$'\n' ;;
    refactor)      REFACTORING="${REFACTORING}- ${DESC}"$'\n' ;;
    docs)          DOCUMENTATION="${DOCUMENTATION}- ${DESC}"$'\n' ;;
    test|tests)    TESTING="${TESTING}- ${DESC}"$'\n' ;;
    chore|build|ci) INFRASTRUCTURE="${INFRASTRUCTURE}- ${DESC}"$'\n' ;;
    *)             OTHER="${OTHER}- ${DESC}"$'\n' ;;
  esac
done < <(git log --oneline --no-merges --format="%s" "$RANGE" 2>/dev/null || true)

# ── Build release title ───────────────────────────────────────────
pick_title() {
  local content="$1"
  if [[ -n "$content" ]]; then
    echo "$content" | head -1 | sed 's/^- //; s/ —.*//; s/\.$//'
  fi
}
TITLE=$(pick_title "$FEATURES")
[[ -z "$TITLE" ]] && TITLE=$(pick_title "$BUGFIXES")
[[ -z "$TITLE" ]] && TITLE=$(pick_title "$INFRASTRUCTURE")
[[ -z "$TITLE" ]] && TITLE="Release"
TITLE="$(echo "$TITLE" | sed 's/./\u&/')"

# ── Version label ─────────────────────────────────────────────────
VERSION="${TO_TAG:-$FROM_TAG}"
DATE=$(date +%Y-%m-%d)

# ── Build the entry ───────────────────────────────────────────────
build_entry() {
  echo "## ${VERSION} (${DATE}) — ${TITLE}"
  echo ""

  emit_section() {
    local label="$1" content="$2"
    content=$(echo "$content" | sed '/^$/d')
    [[ -z "$content" ]] && return
    echo "### ${label}"
    echo "$content"
    echo ""
  }

  emit_section "Features"       "$FEATURES"
  emit_section "Bug Fixes"      "$BUGFIXES"
  emit_section "Performance"    "$PERFORMANCE"
  emit_section "Refactoring"    "$REFACTORING"
  emit_section "Documentation"  "$DOCUMENTATION"
  emit_section "Testing"        "$TESTING"
  emit_section "Infrastructure" "$INFRASTRUCTURE"
  emit_section "Other Changes"  "$OTHER"
}

# ── Execute ────────────────────────────────────────────────────────
case "$MODE" in
  stdout)
    build_entry
    ;;
  notes)
    build_entry | tail -n +3 | sed '/^$/d'
    ;;
  prepend)
    ENTRY=$(build_entry)
    if [[ ! -f "$CHANGELOG_FILE" ]]; then
      echo "# Changelog" > "$CHANGELOG_FILE"
    fi
    HEADER="$(head -2 "$CHANGELOG_FILE")"
    REST="$(tail -n +2 "$CHANGELOG_FILE")"
    {
      echo "$HEADER"
      echo ""
      echo "$ENTRY"
      echo "$REST"
    } > /tmp/changelog-new.md
    mv /tmp/changelog-new.md "$CHANGELOG_FILE"
    echo "✅ Prepended to $CHANGELOG_FILE"
    ;;
esac
