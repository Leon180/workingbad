-- name: GetChangeIDBySHA :one
SELECT change_id FROM raw_commits WHERE sha = ?;

-- name: GetChangeIDByPatch :one
SELECT change_id FROM raw_changes WHERE repo_id = ? AND patch_id = ?;

-- name: GetRawChange :one
SELECT change_id, repo_id, patch_id, created_at FROM raw_changes WHERE change_id = ?;

-- name: InsertRawChange :exec
INSERT INTO raw_changes (change_id, repo_id, patch_id, created_at) VALUES (?, ?, ?, ?);

-- name: InsertRawCommit :exec
INSERT INTO raw_commits
    (sha, repo_id, change_id, parent_shas, author, author_time, committer, commit_time, message, diff, branch_hint, is_current, superseded_by, created_at)
VALUES
    (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: FlipPriorSHAOnChange :exec
UPDATE raw_commits
   SET is_current = 0, superseded_by = ?
 WHERE change_id = ? AND is_current = 1 AND sha != ?;
