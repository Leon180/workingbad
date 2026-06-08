-- name: GetSegmentByKey :one
SELECT id, occurred_at_min, occurred_at_max, ingested_at FROM segments
 WHERE repo_id = ? AND source = ? AND source_ref = ?;

-- name: InsertSegment :exec
INSERT INTO segments
    (id, repo_id, source, source_ref, summary_state, anchor_patch_id, metadata,
     occurred_at_min, occurred_at_max, ingested_at, created_at, updated_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateSegment :exec
UPDATE segments
   SET summary_state = ?, anchor_patch_id = ?, metadata = ?,
       occurred_at_min = ?, occurred_at_max = ?, updated_at = ?
 WHERE id = ?;

-- name: MarkSegmentMaterialized :exec
UPDATE segments
   SET summary_state = ?, updated_at = ?
 WHERE id = ?;

-- name: LinkSegmentRaw :exec
INSERT OR IGNORE INTO segment_raw (segment_id, change_id) VALUES (?, ?);

-- name: GetCurrentChangesForSegment :many
-- Returns each live change in the segment plus the earliest author_time of
-- its current raw_commit(s). Bitemporal materialise reads this to anchor
-- the synthesised activity entry's occurred_at to the source event time.
SELECT rc.change_id, rc.repo_id, rc.patch_id, rc.ingested_at,
       (SELECT MIN(c.author_time)
          FROM raw_commits c
         WHERE c.change_id = rc.change_id AND c.is_current = 1) AS earliest_author_time
  FROM segment_raw sr
  JOIN raw_changes rc ON rc.change_id = sr.change_id
 WHERE sr.segment_id = ?
   AND EXISTS (SELECT 1 FROM raw_commits c WHERE c.change_id = rc.change_id AND c.is_current = 1)
 ORDER BY earliest_author_time ASC;
