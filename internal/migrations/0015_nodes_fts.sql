-- +goose Up
-- Slice D2 step 2 (issue #69): FTS5 full-text search over the live node layer.
--
-- Own-content virtual table mirroring is_current=1 nodes — the node-layer
-- analogue of entries_fts (0007). node_id UNINDEXED so it can be retrieved
-- without spending FTS tokens indexing it; title/body are the indexed content.
-- CreateNode / SupersedeNode maintain this in the SAME tx as the node write
-- (no triggers — manual maintenance, per truth-source-schema FTS5). nodes.id
-- stays uuid v7 and does not align with the FTS rowid by design.
--
-- Pre-`schema-frozen` marker → editing/adding migrations is still allowed
-- (scripts/check-migration-gates.sh gate-1 SKIPs until the tag is pushed).

-- +goose StatementBegin
CREATE VIRTUAL TABLE nodes_fts USING fts5(
    node_id UNINDEXED,
    title,
    body
);
-- +goose StatementEnd

-- Backfill from the live nodes the D1/D2 backfills produced, so search is
-- populated the moment this migration applies. Only is_current=1 rows enter
-- the index (the maintenance contract keeps it that way thereafter).
-- +goose StatementBegin
INSERT INTO nodes_fts (node_id, title, body)
SELECT id, title, body FROM nodes WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
