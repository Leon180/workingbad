-- name: InsertEntryFTS :exec
INSERT INTO entries_fts (entry_id, title, body) VALUES (?, ?, ?);

-- name: DeleteEntryFTS :exec
DELETE FROM entries_fts WHERE entry_id = ?;
