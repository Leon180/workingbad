-- +goose Up
-- Bitemporal time-travel: phase 2 of 4 (backfill).
-- Design record: docs/grill/2026-06-08-time-travel-schema.md
--
-- For existing rows (pre-bitemporal):
--   - ingested_at = created_at (semantic identity)
--   - occurred_at = created_at (best signal we have for legacy data —
--     deliberately matches ingested_at so legacy rows are visibly
--     "system-time only"; bitemporal queries treat them as a single
--     point in time)
--   - version = 1 (start the chain)
--   - quality_degraded = 0 (assume good; new fetched rows that genuinely
--     lack occurred_at will set this to 1 going forward)
--
-- Segments: occurred_at_min/max derived from joined raw_commits.author_time
-- across the live changes. NULL if no joined raw exists (orphan segment).

-- +goose StatementBegin
UPDATE entries
   SET ingested_at = created_at,
       occurred_at = created_at,
       version = 1,
       quality_degraded = 0;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE edges
   SET ingested_at = created_at,
       occurred_at = created_at;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE raw_commits
   SET ingested_at = created_at;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE raw_changes
   SET ingested_at = created_at;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE segments
   SET ingested_at = created_at;
-- +goose StatementEnd

-- Segments.occurred_at_min/max from live raw_commits across linked changes.
-- Coalesce in case the join is empty (orphan segment) — leaves the column NULL.
-- +goose StatementBegin
UPDATE segments
   SET occurred_at_min = (
       SELECT MIN(rc.author_time)
         FROM segment_raw sr
         JOIN raw_commits rc ON rc.change_id = sr.change_id AND rc.is_current = 1
        WHERE sr.segment_id = segments.id
   ),
       occurred_at_max = (
       SELECT MAX(rc.author_time)
         FROM segment_raw sr
         JOIN raw_commits rc ON rc.change_id = sr.change_id AND rc.is_current = 1
        WHERE sr.segment_id = segments.id
   );
-- +goose StatementEnd

-- sync_state: pre-bitemporal rows treated as one logical chain per id.
-- +goose StatementBegin
UPDATE sync_state
   SET ingested_at = synced_at,
       occurred_at = synced_at,
       logical_id = id,
       is_current = 1
 WHERE logical_id IS NULL;
-- +goose StatementEnd

-- source_checkpoint: ingested_at mirrors updated_at; success/failure timestamps
-- left NULL (no historical signal).
-- +goose StatementBegin
UPDATE source_checkpoint
   SET ingested_at = updated_at
 WHERE ingested_at IS NULL;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
