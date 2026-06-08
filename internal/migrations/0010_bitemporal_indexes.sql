-- +goose Up
-- Bitemporal time-travel: phase 3 of 4 (indexes).
-- Design record: docs/grill/2026-06-08-time-travel-schema.md
--
-- Adds the indexes the bitemporal query layer needs. We intentionally keep
-- legacy created_at columns in place at the DB level for now — a final
-- pre-v0.1.0-tag squash migration will drop them once all sqlc queries are
-- migrated to ingested_at/occurred_at (see commit 6 in the grill doc).
--
-- NOT NULL enforcement on the new columns lives at the application validator
-- layer (commit 10), not in SQL — SQLite ALTER TABLE cannot add NOT NULL,
-- only a full table rebuild can, and that rebuild is deferred to the v0.1.0
-- squash.
--
-- Indexes added:
--   entries:
--     idx_entries_logical_occurred       — version-history walk (one logical_id)
--     idx_entries_type_occurred_current  — at-time list by type
--     idx_entries_source_event_hash      — fetched fingerprint idempotency
--   edges:
--     idx_edges_from_occurred_live       — at-time outgoing edges
--     idx_edges_to_occurred_live         — at-time incoming edges
--   segments:
--     idx_segments_repo_occurred_max     — repo work-window queries
--   sync_state:
--     idx_sync_state_logical_live        — at-most-one live row per chain

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_entries_logical_occurred
    ON entries(logical_id, occurred_at DESC, ingested_at DESC);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_entries_type_occurred_current
    ON entries(type, occurred_at DESC)
    WHERE is_current = 1;
-- +goose StatementEnd

-- Idempotency for fetched re-ingest: same (logical_id, source_event_hash)
-- pair already in flight ⇒ no-op. NULL hashes (manual/local) are ignored.
-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_entries_source_event_hash
    ON entries(logical_id, source_event_hash)
    WHERE source_event_hash IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_edges_from_occurred_live
    ON edges(from_id, occurred_at DESC)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_edges_to_occurred_live
    ON edges(to_id, occurred_at DESC)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_segments_repo_occurred_max
    ON segments(repo_id, occurred_at_max DESC);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_state_logical_live
    ON sync_state(logical_id)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
