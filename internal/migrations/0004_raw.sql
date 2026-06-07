-- +goose Up
-- raw_changes is the rewrite-transparent logical change layer.
-- change_id is uuid v7 (surrogate); patch_id is the content fingerprint and
-- can collide intentionally (cherry-pick same patch onto two branches both
-- present in repo) — partial-unique only forbids collisions WITHIN a repo
-- where patch_id is NOT NULL. Merge commits get patch_id=NULL and live as
-- standalone change_id rows (never used as segment anchor).
-- +goose StatementBegin
CREATE TABLE raw_changes (
    change_id  TEXT PRIMARY KEY,                  -- uuid v7
    repo_id    TEXT NOT NULL,
    patch_id   TEXT,                              -- NULL for merges / when computation failed
    created_at TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_raw_changes_repo_patch
    ON raw_changes(repo_id, patch_id)
    WHERE patch_id IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_raw_changes_repo ON raw_changes(repo_id);
-- +goose StatementEnd

-- raw_commits stores the full self-contained commit content. Identity is the
-- sha (idempotent re-ingest = no-op). amend/rebase flips is_current and
-- superseded_by points to the new sha that supersedes this one.
-- +goose StatementBegin
CREATE TABLE raw_commits (
    sha            TEXT PRIMARY KEY,
    repo_id        TEXT NOT NULL,
    change_id      TEXT NOT NULL,
    parent_shas    TEXT NOT NULL DEFAULT '[]',     -- json array
    author         TEXT NOT NULL,
    author_time    TEXT NOT NULL,                  -- UTC
    committer      TEXT NOT NULL,
    commit_time    TEXT NOT NULL,                  -- UTC
    message        TEXT NOT NULL,
    diff           TEXT,                            -- nullable; large; fetched on demand
    branch_hint    TEXT,                            -- branch the commit was first discovered on
    is_current     INTEGER NOT NULL DEFAULT 1,
    superseded_by  TEXT,                            -- another sha (rewrite target)
    created_at     TEXT NOT NULL,
    FOREIGN KEY (change_id) REFERENCES raw_changes(change_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_raw_commits_repo ON raw_commits(repo_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_raw_commits_change ON raw_commits(change_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_raw_commits_repo_current
    ON raw_commits(repo_id, is_current);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
