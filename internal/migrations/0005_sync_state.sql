-- +goose Up
-- Idempotency for sinks. Subject keys are stable across supersede:
--   git   -> segments.source_ref (encode(repo, branch, anchor_patch_id))
--   entry -> entries.logical_id
-- NOT entry_id, because supersede mints a new id and that would cause
-- double-posting on every re-Summarize.
-- +goose StatementBegin
CREATE TABLE sync_state (
    id                   TEXT PRIMARY KEY,                                       -- uuid v7
    sink                 TEXT NOT NULL,
    subject_kind         TEXT NOT NULL CHECK (subject_kind IN ('git','entry')),
    subject_ref          TEXT NOT NULL,
    external_ref         TEXT NOT NULL,                                          -- remote-side id (slack ts, clickup task_id, ...)
    last_synced_hash     TEXT NOT NULL,                                          -- hash of Render(push_fields projection)
    last_synced_entry_id TEXT,                                                   -- latest entry version we last synced
    synced_at            TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_sync_state_idem ON sync_state(sink, subject_kind, subject_ref);
-- +goose StatementEnd

-- +goose Down
SELECT 1;
