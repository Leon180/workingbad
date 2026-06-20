-- +goose Up
-- Slice F1: record edge detach time so EdgesAt can time-travel detaches.
--
-- Post-D2e an edge has only two lifecycle events: create (AttachToGoal) and
-- detach (DetachFromGoal). Edge supersede/re-point is gone, so superseded_by is
-- vestigial (read but never written). DetachEdge previously set is_current=0
-- with no timestamp, so EdgesAt's superseded_by-based predicate treated a
-- detached edge as live at every asOf. detached_at fixes the bitemporal
-- contract: an edge is live at asOf iff it was created by asOf AND not detached
-- by asOf.
--
-- Pre-`schema-frozen` marker, so adding the column is allowed.

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN detached_at TEXT;
-- +goose StatementEnd

-- Backfill: any already-detached row (is_current=0 from a prior detach or the
-- 0017 dedup) gets a best-effort detach time so EdgesAt(now) excludes it. The
-- true detach instant is unrecorded for old rows; ingested_at is the safest
-- value (treats them as detached since they were written) and is RFC3339Nano,
-- matching the format EdgesAt compares against.
-- +goose StatementBegin
UPDATE edges SET detached_at = COALESCE(ingested_at, created_at)
 WHERE is_current = 0 AND detached_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
