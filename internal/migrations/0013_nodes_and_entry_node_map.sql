-- +goose Up
-- Slice D1 (issue #68): introduce the node layer + entry↔node mapping.
--
-- Today entries ARE the graph nodes — there is no separate node concept. The
-- v1.0.0 pipeline needs a node = graph-level work unit that an entry maps to
-- many-to-many (split = 1 entry→N nodes; aggregate = N entries→1 node). This
-- migration is PURELY ADDITIVE (new tables + backfill) and does NOT touch the
-- live read path: the graph/queries still read entries. D2 (issue #69) moves
-- supersede / FTS5 / bitemporal onto nodes and flips the read path over — that
-- is the dangerous, non-additive surgery and is deliberately NOT here.
--
-- Identity: a backfilled node's id EQUALS the originating logical_id. That is
-- not a hack — `logical_id` has always been "the stable identity across a
-- supersede chain", which is exactly what a node is at backfill time (1:1).
-- Post-aggregate/split the two diverge (a node may span multiple logical
-- entities, or a logical entity may split across nodes) and new nodes get
-- fresh uuid v7 ids minted in Go. So node.id is just a uuid whose provenance
-- happens to be a logical_id for the backfill generation.
--
-- v0.1.0 schema-freeze marker not yet pushed → additive structural change is
-- allowed (see scripts/check-migration-gates.sh gate-1; schema-frozen tag is
-- pushed by hand after D2).

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS nodes (
    id         TEXT PRIMARY KEY,                                                                  -- uuid v7 (= logical_id for backfilled rows)
    type       TEXT NOT NULL CHECK (type IN ('activity','research','discuss','decision','goal')), -- closed enum, mirrors entries.type
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    status     TEXT,                                                                              -- goal carries status; others NULL/'' or 'archived'
    created_at TEXT NOT NULL,                                                                     -- UTC ISO-8601
    updated_at TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_nodes_type ON nodes(type);
-- +goose StatementEnd

-- entry_node_map: many-to-many between entry versions and nodes. PK on the
-- pair allows one entry to map to multiple nodes (future split) and one node
-- to gather multiple entries (future aggregate). ON DELETE CASCADE keeps the
-- map clean if a node is ever hard-deleted (rare; supersede is the norm).
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS entry_node_map (
    entry_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    node_id  TEXT NOT NULL REFERENCES nodes(id)   ON DELETE CASCADE,
    PRIMARY KEY (entry_id, node_id)
);
-- +goose StatementEnd

-- "all entries for a node" without scanning the map.
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_entry_node_map_node ON entry_node_map(node_id);
-- +goose StatementEnd

-- Backfill, pure SQL, deterministic:
--   1. one node per logical_id, snapshotted from the LIVE (is_current=1)
--      version's graph fields; node.id := logical_id.
--   2. map EVERY entry version (current + superseded) to its logical_id node,
--      so historical versions resolve to the same node.
-- INSERT OR IGNORE guards re-runnability (idempotent on a partially-applied
-- DB, though goose's version table normally prevents re-run).
-- +goose StatementBegin
INSERT OR IGNORE INTO nodes (id, type, title, body, status, created_at, updated_at)
SELECT logical_id, type, title, COALESCE(body, ''), status, created_at, updated_at
FROM entries
WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
INSERT OR IGNORE INTO entry_node_map (entry_id, node_id)
SELECT id, logical_id
FROM entries;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
