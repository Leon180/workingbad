-- +goose Up
-- Idempotency-key authority for git ingest (truth-source-schema invariant 2).
-- segments are the lifecycle carrier for work-session boundaries; entries
-- only hold materialized output.
-- +goose StatementBegin
CREATE TABLE segments (
    id              TEXT PRIMARY KEY,
    repo_id         TEXT NOT NULL,
    source          TEXT NOT NULL,
    source_ref      TEXT NOT NULL,                                                       -- encode(repo_id, branch_name, anchor_patch_id)
    summary_state   TEXT NOT NULL CHECK (summary_state IN ('pending','materialized','stale')),
    anchor_patch_id TEXT,                                                                -- author-time-earliest non-NULL patch-id of segment
    metadata        TEXT NOT NULL DEFAULT '{}',                                          -- threshold snapshot lives here, not source_ref
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_segments_idem ON segments(repo_id, source, source_ref);
-- +goose StatementEnd

-- partial index speeds up batchMaterialize's "find work" scan
-- +goose StatementBegin
CREATE INDEX idx_segments_needs_summary
    ON segments(summary_state)
    WHERE summary_state IN ('pending','stale');
-- +goose StatementEnd

-- segment_raw join: pointing to change_id (not sha) so amend/rebase keeps the
-- join stable while raw_commits.is_current flips underneath
-- +goose StatementBegin
CREATE TABLE segment_raw (
    segment_id TEXT NOT NULL,
    change_id  TEXT NOT NULL,
    PRIMARY KEY (segment_id, change_id),
    FOREIGN KEY (segment_id) REFERENCES segments(id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_segment_raw_change ON segment_raw(change_id);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
