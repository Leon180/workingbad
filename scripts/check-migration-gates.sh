#!/usr/bin/env bash
# scripts/check-migration-gates.sh
#
# Three CI gates that enforce the migration discipline locked in ROADMAP
# "Migration 紀律":
#   gate-1 immutable-tagged:  post v0.1.0, files at the tag must not change
#   gate-2 sequential:        numbering is 0001, 0002, ... with no gaps
#   gate-3 count-matches-max: number of files == highest version
#
# Pre-v0.1.0 gate-1 is a no-op (we may still edit/squash unreleased files).
# Exits non-zero on any failure so CI fails loudly.

set -euo pipefail

MIG_DIR="internal/migrations"
FROZEN_TAG="v0.1.0"

cd "$(git rev-parse --show-toplevel)"

if [ ! -d "$MIG_DIR" ]; then
  echo "FAIL: migrations directory $MIG_DIR not found"
  exit 1
fi

# Use a sorted list of sql files only.
list_migrations() {
  find "$MIG_DIR" -maxdepth 1 -type f -name '[0-9]*_*.sql' -exec basename {} \; | sort
}

gate_sequential() {
  local expected=1
  local saw_count=0
  local files
  files=$(list_migrations)
  if [ -z "$files" ]; then
    echo "FAIL gate-2 (sequential): no migration files found"
    return 1
  fi
  while IFS= read -r f; do
    local n
    n=$(echo "$f" | sed -E 's/^0*([0-9]+)_.*/\1/')
    if [ "$n" -ne "$expected" ]; then
      echo "FAIL gate-2 (sequential): expected migration ${expected}, saw ${f}"
      return 1
    fi
    expected=$((expected + 1))
    saw_count=$((saw_count + 1))
  done <<< "$files"
  echo "OK   gate-2 (sequential):   ${saw_count} files, no gaps"
  return 0
}

gate_count_matches() {
  local files
  files=$(list_migrations)
  local count
  count=$(printf '%s\n' "$files" | grep -c '.')
  local max
  max=$(printf '%s\n' "$files" | sed -E 's/^0*([0-9]+)_.*/\1/' | sort -n | tail -1)
  if [ "${count:-0}" -ne "${max:-0}" ]; then
    echo "FAIL gate-3 (count):       ${count} files but highest version is ${max}"
    return 1
  fi
  echo "OK   gate-3 (count):        ${count} files == highest version ${max}"
  return 0
}

gate_immutable_tagged() {
  if ! git rev-parse --verify --quiet "$FROZEN_TAG" >/dev/null 2>&1; then
    echo "SKIP gate-1 (immutable):   tag ${FROZEN_TAG} does not exist yet (pre-1.0)"
    return 0
  fi
  local changed
  changed=$(git diff --name-only "$FROZEN_TAG" HEAD -- "$MIG_DIR" | grep -E '\.sql$' || true)
  if [ -z "$changed" ]; then
    echo "OK   gate-1 (immutable):   no migration files changed since ${FROZEN_TAG}"
    return 0
  fi
  local violations=""
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    # A file is allowed to be NEW after the tag — only ones that existed at the
    # tag and were subsequently changed are violations.
    if git cat-file -e "${FROZEN_TAG}:${f}" 2>/dev/null; then
      violations+="${f}"$'\n'
    fi
  done <<< "$changed"
  if [ -n "$violations" ]; then
    echo "FAIL gate-1 (immutable):   the following migration files were modified after ${FROZEN_TAG}:"
    printf '  - %s\n' $violations
    return 1
  fi
  echo "OK   gate-1 (immutable):   only NEW migration files appended since ${FROZEN_TAG}"
  return 0
}

ok=0
gate_immutable_tagged || ok=1
gate_sequential       || ok=1
gate_count_matches    || ok=1

if [ $ok -ne 0 ]; then
  echo
  echo "Migration gate(s) failed."
  exit 1
fi
echo
echo "All migration gates passed."
