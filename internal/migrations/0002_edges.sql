-- +goose Up
-- +goose StatementBegin
CREATE TABLE edges (
    id            TEXT PRIMARY KEY,
    from_id       TEXT NOT NULL,
    to_id         TEXT NOT NULL,
    relation      TEXT NOT NULL CHECK (relation IN ('relates_to','derived_from','blocks','part_of','iteration_of')),
    is_current    INTEGER NOT NULL DEFAULT 1,
    superseded_by TEXT,
    metadata      TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL
);
-- +goose StatementEnd

-- partial unique: at most one live edge per (from, to, relation)
-- supersede flips is_current=0 first, then inserts new row (Summarizer same-tx)
-- +goose StatementBegin
CREATE UNIQUE INDEX idx_edges_live_triple
    ON edges(from_id, to_id, relation)
    WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_edges_from_live ON edges(from_id) WHERE is_current = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_edges_to_live ON edges(to_id) WHERE is_current = 1;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
