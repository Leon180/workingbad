-- +goose Up
-- Bitemporal time-travel: phase 4 of 4 (sync_state → append-only).
-- Design record: docs/grill/2026-06-08-time-travel-schema.md §4.6
--
-- Pre-bitemporal: sync_state was UPSERT-by-(sink, subject_kind, subject_ref).
-- One row per tuple, overwritten on each sync = no history.
--
-- Post-bitemporal: each sync inserts a fresh row + flips the previous live
-- row to is_current=0, superseded_by=<new>. Many rows share a logical_id
-- chain; idx_sync_state_logical_live (from 0010) ensures at most one live
-- row per chain. The legacy idempotency index needs to become partial so
-- it allows historical duplicates but still enforces "one live per tuple".
--
-- source_checkpoint is intentionally NOT converted to append-only here
-- (red team decision: its real value is fast resume, not reflog —
-- last_success_at / last_failure_at / failure_reason added in commit 12
-- carry the audit signal we actually need).

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sync_state_idem;
-- +goose StatementEnd

-- New partial unique: at most one live row per (sink, subject_kind, subject_ref).
-- Historical (is_current=0) rows for the same tuple are allowed and form the
-- audit trail / reflog for retroactive query.
-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_state_idem_live
    ON sync_state(sink, subject_kind, subject_ref)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_sync_state_logical_chain
    ON sync_state(logical_id, occurred_at DESC);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
