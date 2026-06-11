-- +goose Up
-- Two additive tables to support:
--
--   1. Multi-label per entry — a node can carry zero or more secondary
--      type labels from the closed set {activity, research, discuss,
--      decision} in addition to its entries.type "primary label". The
--      effective label set for rendering is {entries.type} ∪
--      {entry_labels.label}. `goal` is intentionally excluded from the
--      secondary set: goal is an aggregation-root identity, not an
--      aspect.
--
--   2. Source-ref alias — each entry can be addressed by multiple
--      (source, source_ref) tuples. Today each entry has exactly one
--      tuple equal to its primary entries.source/source_ref; after a
--      future merge action, multiple tuples may resolve to the same
--      entry so subsequent source updates still find the merged target.
--
-- Both tables are additive; no existing column changes. v0.1.0 pre-tag,
-- so we can ship structural additions.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS entry_labels (
    entry_id TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    label    TEXT NOT NULL CHECK (label IN ('activity', 'research', 'discuss', 'decision')),
    PRIMARY KEY (entry_id, label)
);
-- +goose StatementEnd

-- "find all entries with label X" without scanning entry_labels.
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_entry_labels_label ON entry_labels(label);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS entry_source_refs (
    entry_id    TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    source_ref  TEXT NOT NULL,
    PRIMARY KEY (source, source_ref)
);
-- +goose StatementEnd

-- "find the entry that source's ref currently resolves to".
-- The PK on (source, source_ref) already enforces lookup uniqueness;
-- this secondary index covers the reverse "all aliases for entry_id".
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_entry_source_refs_entry_id ON entry_source_refs(entry_id);
-- +goose StatementEnd

-- Backfill: every existing entry that carries a non-empty source_ref
-- gets one row matching its primary identity. After this migration
-- runs, lookup-by-source-ref via entry_source_refs is authoritative
-- regardless of which version of the entry the source_ref originally
-- belonged to.
--
-- INSERT OR IGNORE protects against duplicate (source, source_ref)
-- pairs from prior versions of the same logical_id — only one row
-- per tuple lives in the alias table, pointing at whichever id was
-- inserted first. The Service layer's source-ref lookup will then
-- resolve to the live version via the entries.is_current check.
-- +goose StatementBegin
INSERT OR IGNORE INTO entry_source_refs (entry_id, source, source_ref)
SELECT id, source, source_ref
FROM entries
WHERE source_ref != '';
-- +goose StatementEnd

-- +goose Down
SELECT 1;