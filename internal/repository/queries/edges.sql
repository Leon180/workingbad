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
UPDATE edges SET is_current = 0 WHERE id = ? AND is_current = 1;

-- name: GetIncomingLiveEdges :many
SELECT id, from_id, relation, metadata, occurred_at FROM edges
 WHERE to_id = ? AND is_current = 1;

-- name: GetOutgoingLiveEdges :many
SELECT id, to_id, relation, metadata, occurred_at FROM edges
 WHERE from_id = ? AND is_current = 1;

-- name: SupersedeEdge :exec
UPDATE edges SET is_current = 0, superseded_by = ? WHERE id = ?;

-- name: GetGoalActivitiesByLogicalID :many
SELECT e.*
  FROM entries AS e
  JOIN edges   AS ed ON ed.from_id = e.id AND ed.relation = 'part_of' AND ed.is_current = 1
  JOIN entries AS g  ON ed.to_id = g.id
 WHERE g.logical_id = (SELECT seed.logical_id FROM entries AS seed WHERE seed.id = ?)
   AND e.type = 'activity'
   AND e.is_current = 1
 ORDER BY e.occurred_at ASC, e.ingested_at ASC;
