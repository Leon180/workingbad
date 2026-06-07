-- name: InsertEntryRow :exec
INSERT INTO entries
    (id, logical_id, type, title, body, source, source_ref, origin, repo_id, author, status, is_current, superseded_by, metadata, created_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEntryByID :one
SELECT id, logical_id, type, title, body, source, source_ref, origin, repo_id, author, status, is_current, superseded_by, metadata, created_at, updated_at
  FROM entries
 WHERE id = ?;

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
