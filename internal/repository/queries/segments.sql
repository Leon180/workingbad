-- name: GetSegmentByKey :one
SELECT id, created_at FROM segments
 WHERE repo_id = ? AND source = ? AND source_ref = ?;

-- name: InsertSegment :exec
INSERT INTO segments
    (id, repo_id, source, source_ref, summary_state, anchor_patch_id, metadata, created_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateSegment :exec
UPDATE segments
   SET summary_state = ?, anchor_patch_id = ?, metadata = ?, updated_at = ?
 WHERE id = ?;

-- name: MarkSegmentMaterialized :exec
UPDATE segments
   SET summary_state = ?, updated_at = ?
 WHERE id = ?;

-- name: LinkSegmentRaw :exec
INSERT OR IGNORE INTO segment_raw (segment_id, change_id) VALUES (?, ?);

-- name: GetCurrentChangesForSegment :many
SELECT rc.change_id, rc.repo_id, rc.patch_id, rc.created_at
  FROM segment_raw sr
  JOIN raw_changes rc ON rc.change_id = sr.change_id
 WHERE sr.segment_id = ?
   AND EXISTS (SELECT 1 FROM raw_commits c WHERE c.change_id = rc.change_id AND c.is_current = 1)
 ORDER BY rc.created_at ASC;
