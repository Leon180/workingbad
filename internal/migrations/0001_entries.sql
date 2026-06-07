-- +goose Up
-- +goose StatementBegin
CREATE TABLE entries (
    id            TEXT PRIMARY KEY,                                                                  -- uuid v7
    logical_id    TEXT NOT NULL,                                                                     -- uuid v7; stable identity across edits/supersede
    type          TEXT NOT NULL CHECK (type IN ('activity','research','discuss','decision','goal')), -- closed enum (invariant 5)
    title         TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL,                                                                     -- git | github | slack | clickup | claude | manual
    source_ref    TEXT,                                                                              -- create-dedupe only; identity is logical_id (invariant 9 + manual lifecycle)
    origin        TEXT NOT NULL CHECK (origin IN ('fetched','pushed','local')),
    repo_id       TEXT,                                                                              -- isolation key; NULL for non-git manual/fetched
    author        TEXT,
    status        TEXT,                                                                              -- per-type validator enforces non-NULL only for goal
    is_current    INTEGER NOT NULL DEFAULT 1,                                                        -- 0/1; append-only + supersede (invariant 1)
    superseded_by TEXT,                                                                              -- entry id pointing forward; NULL when is_current=1
    metadata      TEXT NOT NULL DEFAULT '{}',                                                        -- json blob; must not carry core semantics (invariant 6)
    created_at    TEXT NOT NULL,                                                                     -- UTC ISO-8601 (invariant 7)
    updated_at    TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_entries_logical_id ON entries(logical_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_entries_type_current ON entries(type, is_current);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_entries_repo_source ON entries(repo_id, source);
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
-- Down kept as no-op so goose tooling does not complain; never run.
SELECT 1;
