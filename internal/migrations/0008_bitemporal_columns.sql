-- +goose Up
-- Bitemporal time-travel: phase 1 of 4 (additive nullable columns).
-- Design record: docs/grill/2026-06-08-time-travel-schema.md
--
-- This migration only ADDs nullable columns. 0009 backfills them from
-- created_at (where applicable). 0010 drops legacy created_at, enforces
-- NOT NULL, and creates new indexes via table-rebuild. 0011 converts
-- sync_state to append-only + supersede.
--
-- Splitting these gives goose forward-only safety: each migration is
-- a small, independently green CI step. dogfooding DB never half-broken.

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN occurred_at       TEXT;    -- event time / valid_time
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN ingested_at       TEXT;    -- system time / will replace created_at
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN actor             TEXT;    -- optional; supersede audit
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN reason            TEXT;    -- optional; supersede audit
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN source_event_hash TEXT;    -- fetched fingerprint for idempotency; NULL for manual/local
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN version           INTEGER; -- optimistic lock + linear history
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE entries ADD COLUMN quality_degraded  INTEGER; -- 0/1; set when occurred_at fell back to ingested_at
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN occurred_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN actor       TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN reason      TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE segments ADD COLUMN occurred_at_min TEXT;  -- MIN(raw_commits.author_time) across change set
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE segments ADD COLUMN occurred_at_max TEXT;  -- MAX(raw_commits.author_time)
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE segments ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE segment_raw ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE raw_commits ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE raw_changes ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sync_state ADD COLUMN occurred_at   TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sync_state ADD COLUMN ingested_at   TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sync_state ADD COLUMN logical_id    TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sync_state ADD COLUMN is_current    INTEGER DEFAULT 1;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sync_state ADD COLUMN superseded_by TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE source_checkpoint ADD COLUMN last_success_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE source_checkpoint ADD COLUMN last_failure_at TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE source_checkpoint ADD COLUMN failure_reason  TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE source_checkpoint ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
