-- +goose Up
-- Slice F2a: per-edge confidence for the relate pipeline (Step 3) and the
-- dashed/solid + slider confidence UI.
--
-- NOT NULL DEFAULT 1.0: every edge today is a human-asserted AttachToGoal link
-- (certain), and manual edges stay 1.0; the LLM relate step (Slice F8) writes
-- its own scores. SQLite permits ADD COLUMN NOT NULL with a constant DEFAULT,
-- so existing rows backfill to 1.0 automatically — no separate UPDATE needed.
-- Pre-`schema-frozen` marker, so the additive ALTER is allowed.

-- +goose StatementBegin
ALTER TABLE edges ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
-- +goose StatementEnd

-- +goose Down
-- forward-only migration discipline: see docs/ROADMAP.md "Migration 紀律".
SELECT 1;
