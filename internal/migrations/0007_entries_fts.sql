-- +goose Up
-- FTS5 own-content virtual table mirroring is_current=1 entries.
-- entry_id UNINDEXED so we can retrieve it but not waste tokens indexing it.
-- repository.insertEntry / supersede paths maintain this in the same tx;
-- NO triggers (own-content + manual maintenance, per truth-source-schema FTS5).
-- entries.id PK stays uuid v7 (does not align with FTS rowid by design).
-- +goose StatementBegin
CREATE VIRTUAL TABLE entries_fts USING fts5(
    entry_id UNINDEXED,
    title,
    body
);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
