-- name: GetEdgeByID :one
SELECT * FROM edges WHERE id = ?;

-- name: GetLiveEdgeByTriple :one
SELECT id FROM edges
 WHERE from_id = ? AND to_id = ? AND relation = ? AND is_current = 1;

-- name: InsertEdge :exec
INSERT INTO edges
    (id, from_id, to_id, relation, is_current, metadata,
     actor, reason, occurred_at, ingested_at, created_at)
VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?);

-- name: DetachEdge :execrows
UPDATE edges SET is_current = 0, detached_at = ? WHERE id = ? AND is_current = 1;

-- name: GetGoalActivitiesByLogicalID :many
-- Edges key on the node stable id (logical_id == node.id) since migration 0017,
-- so the activity joins by its logical_id and the goal is matched directly by
-- its logical_id (no goal-version chain join needed).
SELECT e.*
  FROM entries AS e
  JOIN edges   AS ed ON ed.from_id = e.logical_id AND ed.relation = 'part_of' AND ed.is_current = 1
 WHERE ed.to_id = (SELECT seed.logical_id FROM entries AS seed WHERE seed.id = ?)
   AND e.type = 'activity'
   AND e.is_current = 1
 ORDER BY e.occurred_at ASC, e.ingested_at ASC;
