-- +goose Up
-- Slice D2 step 1 (issue #69), model A: give the node its own supersede chain.
--
-- entries become immutable raw facts; the NODE is the editable/versioned
-- graph unit. This is ADDITIVE (ALTER ADD COLUMN + backfill) — the live read
-- path still reads entries; FTS5, the read-path flip, and the edges re-key to
-- node-id (decision (a), issue #69) land in subsequent D2 sub-PRs. This sub-PR
-- only equips nodes with the versioning + bitemporal columns and the
-- SupersedeNode mechanism so the rest of D2 can build on it.

-- logical_id is added nullable: SQLite ALTER ADD COLUMN cannot attach NOT NULL
-- without a DEFAULT (which would be wrong here — the value is backfilled below)
-- and cannot ADD a CHECK constraint to an existing table at all. Every writer
-- populates it (CreateNode sets logical_id=id; SupersedeNode inherits it; the
-- backfill below sets it), and idx_nodes_logical_live only enforces uniqueness
-- on non-NULL live rows. The NOT NULL DDL enforcement is deferred to the D2
-- read-path-flip sub-PR, which rebuilds the nodes table anyway (#69).
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN logical_id TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN is_current INTEGER NOT NULL DEFAULT 1;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN superseded_by TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN occurred_at TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE nodes ADD COLUMN ingested_at TEXT;
-- +goose StatementEnd

-- Backfill logical_id := id. Each existing node is its own logical root; from
-- the D1 backfill node.id == the entry's logical_id, so this keeps the node
-- logical identity aligned with the entry world it came from.
-- +goose StatementBegin
UPDATE nodes SET logical_id = id WHERE logical_id IS NULL;
-- +goose StatementEnd

-- Backfill the bitemporal pair from the live entry mapped to each node, so
-- node-level time-travel (a later D2 sub-PR) has real timestamps to walk.
-- +goose StatementBegin
UPDATE nodes SET
  occurred_at = (
    SELECT COALESCE(e.occurred_at, e.ingested_at, e.created_at)
    FROM entries e
    JOIN entry_node_map m ON m.entry_id = e.id
    WHERE m.node_id = nodes.id AND e.is_current = 1
    LIMIT 1
  ),
  ingested_at = (
    SELECT COALESCE(e.ingested_at, e.created_at)
    FROM entries e
    JOIN entry_node_map m ON m.entry_id = e.id
    WHERE m.node_id = nodes.id AND e.is_current = 1
    LIMIT 1
  )
WHERE occurred_at IS NULL;
-- +goose StatementEnd

-- Chain-walk by logical_id + live lookup by type. (idx_nodes_type from 0013
-- stays; this superset index serves the is_current-filtered live queries.)
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_nodes_logical ON nodes(logical_id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_nodes_type_current ON nodes(type, is_current);
-- +goose StatementEnd

-- At most one live version per logical chain — the node-layer analogue of
-- idx_entries / sync_state live-uniqueness. Backfilled rows are all version 1
-- / is_current 1 with distinct logical_ids, so this holds at creation.
-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_logical_live ON nodes(logical_id) WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
