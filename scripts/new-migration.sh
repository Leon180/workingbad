#!/usr/bin/env bash
# scripts/new-migration.sh — create the next forward-only migration file.
#
# Usage: make migrate-new NAME=add_segments_status_idx
# Naming: forward-only, never edit a tagged file (see ROADMAP "Migration 紀律").

set -euo pipefail

NAME="${1:-}"
if [ -z "$NAME" ]; then
  echo "usage: $0 <descriptive_name>"
  exit 1
fi

SAFE_NAME=$(echo "$NAME" | tr ' /' '__' | tr -cd '[:alnum:]_-')

ROOT="$(git rev-parse --show-toplevel)"
MIG_DIR="${ROOT}/internal/migrations"

NEXT_RAW=$(
  find "$MIG_DIR" -maxdepth 1 -type f -name '[0-9]*_*.sql' -exec basename {} \; \
    | sed -E 's/^0*([0-9]+)_.*/\1/' \
    | sort -n \
    | tail -1
)
NEXT=$((${NEXT_RAW:-0} + 1))
PADDED=$(printf "%04d" "$NEXT")
NEW="${MIG_DIR}/${PADDED}_${SAFE_NAME}.sql"

cat > "$NEW" <<'EOF'
-- +goose Up
-- +goose StatementBegin
-- TODO: DDL here.
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline (see docs/ROADMAP.md "Migration 紀律")
SELECT 1;
EOF

echo "created: ${NEW}"
