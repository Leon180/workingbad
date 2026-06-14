-- +goose Up
-- Slice D2 step 4 (issue #69, decision (a)): re-key edges to the node layer.
--
-- Until now edges.from_id/to_id stored an ENTRY id — a per-VERSION id. Because
-- that id changes every time the entry supersedes, the codebase carried a
-- rePointAllLiveEdges pass that, on every supersede, rewrote each touching edge
-- to the new version's id. Decision (a): edges instead reference the node's
-- STABLE identity (logical_id == node.id at this stage — node.id and logical_id
-- coincide until split/aggregate land). Since logical_id is invariant across an
-- entry's supersede chain, edges never need re-pointing again, and the entire
-- re-point machinery is deleted in the same change.
--
-- This is a DESTRUCTIVE, forward-only data remap. A verbatim, recoverable copy
-- of the pre-remap edges is kept in edges_pre_node_rekey — drop it by hand once
-- you've confirmed the graph still renders correctly. Pre-`schema-frozen`
-- marker, so editing/adding migrations is still allowed.

-- 1. Recoverable backup of the current edges (verbatim).
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS edges_pre_node_rekey AS SELECT * FROM edges;
-- +goose StatementEnd

-- 2. The live-triple uniqueness is keyed on the OLD entry-ids. Drop it before
--    the in-place remap so the UPDATEs can't transiently violate it (two
--    versions briefly mapping to the same logical triple).
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_edges_live_triple;
-- +goose StatementEnd

-- 3. Remap from_id: entry-id → that entry's logical_id (= node.id). Only rows
--    whose endpoint resolves to a known entry are touched. Entries are
--    append-only + supersede (never hard-deleted), so every edge endpoint
--    written by AttachToGoal resolves to a live entry — there are no orphans in
--    practice. The WHERE EXISTS guard is purely defensive; were a stale orphan
--    to exist it would be left as-is AND preserved verbatim in
--    edges_pre_node_rekey for inspection (inspect with:
--    SELECT * FROM edges WHERE is_current=1
--      AND from_id NOT IN (SELECT logical_id FROM entries)).
-- +goose StatementBegin
UPDATE edges
   SET from_id = (SELECT e.logical_id FROM entries e WHERE e.id = edges.from_id)
 WHERE EXISTS (SELECT 1 FROM entries e WHERE e.id = edges.from_id);
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE edges
   SET to_id = (SELECT e.logical_id FROM entries e WHERE e.id = edges.to_id)
 WHERE EXISTS (SELECT 1 FROM entries e WHERE e.id = edges.to_id);
-- +goose StatementEnd

-- 4. Collapsing per-version ids onto logical_ids could make two live rows share
--    (from_id, to_id, relation). In correctly re-pointed data this can't happen
--    (re-point retired the old before inserting the new, leaving one live row
--    per logical relationship), but guard anyway: keep the most-recently-
--    inserted live row per triple, retire the rest (is_current=0 — NOT deleted,
--    so the row + its superseded_by audit trail survive and the pre-image is
--    also in edges_pre_node_rekey), so the unique index rebuilds cleanly.
-- +goose StatementBegin
UPDATE edges
   SET is_current = 0
 WHERE is_current = 1
   AND rowid NOT IN (
       SELECT MAX(rowid) FROM edges
        WHERE is_current = 1
        GROUP BY from_id, to_id, relation
   );
-- +goose StatementEnd

-- 5. Rebuild live-triple uniqueness on the now node-keyed columns.
-- +goose StatementBegin
CREATE UNIQUE INDEX idx_edges_live_triple
    ON edges(from_id, to_id, relation)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
