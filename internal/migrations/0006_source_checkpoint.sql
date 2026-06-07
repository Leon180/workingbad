-- +goose Up
-- The ONLY mutable table in the system (invariant 1 exemption).
-- cursor is opaque per-Source blob; correctness anchors live in raw sha unique
-- + patch_id chain, not here. advance is in-place UPDATE; raw/segment writes
-- and checkpoint advance are separate (at-least-once + idempotent convergence).
-- +goose StatementBegin
CREATE TABLE source_checkpoint (
    repo_id    TEXT NOT NULL,
    source     TEXT NOT NULL,
    cursor     BLOB NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, source)
);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
