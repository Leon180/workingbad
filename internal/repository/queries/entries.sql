-- name: InsertEntryRow :exec
INSERT INTO entries
    (id, logical_id, type, title, body, source, source_ref, source_event_hash, origin,
     repo_id, author, actor, reason, status, is_current, superseded_by, metadata,
     version, quality_degraded, occurred_at, ingested_at, created_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEntryByID :one
SELECT * FROM entries WHERE id = ?;

-- name: GetEntryNodeRef :one
SELECT logical_id, type, is_current FROM entries WHERE id = ?;

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

-- name: GetEntryLogicalIDByID :one
-- Cheap targeted lookup for the Web UI's resolveLogical fallback path.
-- Pre-fix the handler scanned ListEntries(Limit=1000) and silently 404'd
-- past row 1000 (architect review #10 P1, hunter P1). This replaces the
-- scan with an indexed PK lookup.
SELECT logical_id FROM entries WHERE id = ?;
