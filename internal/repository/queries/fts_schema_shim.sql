-- sqlc's SQLite parser does not understand CREATE VIRTUAL TABLE ... USING fts5(...).
-- This shim declares entries_fts as an ordinary table with the same column
-- shape so sqlc can typecheck INSERT/DELETE/SELECT statements against it. At
-- runtime modernc creates the real FTS5 virtual table via the goose
-- migration; only the codegen path sees this shim.

CREATE TABLE entries_fts (
    entry_id TEXT NOT NULL,
    title    TEXT NOT NULL,
    body     TEXT NOT NULL
);
