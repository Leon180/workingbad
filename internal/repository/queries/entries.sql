-- name: InsertEntryRow :exec
INSERT INTO entries
    (id, logical_id, type, title, body, source, source_ref, source_event_hash, origin,
     repo_id, author, actor, reason, status, is_current, superseded_by, metadata,
     version, quality_degraded, occurred_at, ingested_at, created_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEntryByID :one
SELECT * FROM entries WHERE id = ?;

-- name: GetEntryTypeAndCurrent :one
SELECT type, is_current FROM entries WHERE id = ?;

-- name: GetLiveActivityForSegment :one
SELECT id FROM entries
 WHERE type = 'activity' AND source = 'git' AND source_ref = ? AND is_current = 1
 LIMIT 1;

-- name: FlipEntrySuperseded :exec
UPDATE entries
   SET is_current = 0, superseded_by = ?, updated_at = ?
 WHERE id = ?;

-- name: GetEntryByLogicalIDAndHash :one
-- Idempotency lookup: if a fetched event with this (logical_id, hash) is
-- already current, the caller should noop. Returns the live entry id.
SELECT id FROM entries
 WHERE logical_id = ?
   AND source_event_hash = ?
   AND is_current = 1
 LIMIT 1;
