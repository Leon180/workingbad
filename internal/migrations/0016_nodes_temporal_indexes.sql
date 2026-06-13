-- +goose Up
-- Slice D2 step 3 (issue #69): indexes backing node-layer time-travel.
--
-- Mirrors the entries bitemporal indexes (0010) now that nodes carry
-- occurred_at / ingested_at / version (0014). NodeHistory walks a single
-- logical_id by (version / occurred_at); ListNodesAt filters by type and
-- orders by occurred_at. Additive, idempotent (IF NOT EXISTS).
--
-- Pre-`schema-frozen` marker → adding migrations is still allowed
-- (scripts/check-migration-gates.sh gate-1 SKIPs until the tag is pushed).

-- NodeHistory: WHERE logical_id = ? ORDER BY version DESC, ingested_at DESC.
-- The key columns match the ORDER BY exactly, so SQLite serves both the lookup
-- AND the ordering from the index with no in-memory sort. (This deliberately
-- improves on the entries analogue idx_entries_logical_occurred, whose
-- occurred_at second column does NOT match EntryHistory's version ordering and
-- so forces a post-sort.)
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_nodes_logical_version
    ON nodes(logical_id, version DESC, ingested_at DESC);
-- +goose StatementEnd

-- Live list by type, occurred-ordered (partial → only live rows indexed).
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_nodes_type_occurred_current
    ON nodes(type, occurred_at DESC)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
