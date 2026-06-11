package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// labelEntryColumns matches the 22-column order that scanEntryRows
// expects. Kept in one place to make drift visible if the bitemporal
// scanner ever changes.
const labelEntryColumns = `e.id, e.logical_id, e.type, e.title, e.body, e.source,
       COALESCE(e.source_ref, ''), COALESCE(e.source_event_hash, ''),
       e.origin, COALESCE(e.repo_id, ''), COALESCE(e.author, ''),
       COALESCE(e.actor, ''), COALESCE(e.reason, ''),
       COALESCE(e.status, ''), e.is_current,
       COALESCE(e.superseded_by, ''), e.metadata,
       COALESCE(e.version, 1), COALESCE(e.quality_degraded, 0),
       COALESCE(e.occurred_at, e.ingested_at, e.created_at),
       COALESCE(e.ingested_at, e.created_at),
       e.updated_at`

// validSecondaryLabels is the closed enum for the entry_labels table.
// `goal` is intentionally NOT in the set: a goal is an aggregation-root
// identity, not an aspect a node carries alongside other aspects.
var validSecondaryLabels = map[domain.EntryType]struct{}{
	domain.EntryTypeActivity: {},
	domain.EntryTypeResearch: {},
	domain.EntryTypeDiscuss:  {},
	domain.EntryTypeDecision: {},
}

// SetLabels replaces the full secondary-label set for an entry. Pass an
// empty slice (or nil) to clear all labels. The validator rejects:
//   - labels not in {activity, research, discuss, decision}
//   - labels equal to the entry's primary type (would be a redundant
//     no-op; surface as an error so the caller noticed)
//
// Idempotent: calling with the same set twice ends in the same state.
// Single tx so partial-write is impossible.
func (s *Service) SetLabels(ctx context.Context, entryID string, labels []domain.EntryType) error {
	if entryID == "" {
		return fmt.Errorf("%w: entry id required", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Need the live primary type for the "label != primary" check, plus
	// an early-out if the entry doesn't exist.
	var primaryType string
	var isCurrent int
	err = tx.QueryRowContext(ctx,
		`SELECT type, is_current FROM entries WHERE id = ?`, entryID).
		Scan(&primaryType, &isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: entry %s", ErrNotFound, entryID)
	}
	if err != nil {
		return fmt.Errorf("repository: load entry type: %w", err)
	}
	if isCurrent != 1 {
		return fmt.Errorf("%w: entry %s is not live", ErrNotFound, entryID)
	}

	// Validate every requested label before touching the table.
	seen := make(map[domain.EntryType]bool, len(labels))
	for _, l := range labels {
		if _, ok := validSecondaryLabels[l]; !ok {
			return fmt.Errorf("%w: label %q not in {activity, research, discuss, decision}",
				ErrInvalidInput, l)
		}
		if string(l) == primaryType {
			return fmt.Errorf("%w: label %q equals entry primary type — drop the duplicate",
				ErrInvalidInput, l)
		}
		if seen[l] {
			return fmt.Errorf("%w: duplicate label %q in input", ErrInvalidInput, l)
		}
		seen[l] = true
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entry_labels WHERE entry_id = ?`, entryID); err != nil {
		return fmt.Errorf("repository: clear labels: %w", err)
	}
	// One multi-row INSERT instead of N per-row inserts — the label set
	// is bounded (≤3) but a caller that loops SetLabels across many
	// entries (e.g. the seeder) compounds the round-trips fast. Build
	// the placeholders + args up front.
	if len(seen) > 0 {
		placeholders := make([]string, 0, len(seen))
		args := make([]any, 0, len(seen)*2)
		for l := range seen {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, entryID, string(l))
		}
		q := `INSERT INTO entry_labels (entry_id, label) VALUES ` +
			strings.Join(placeholders, ", ")
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("repository: insert labels: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repository: commit: %w", err)
	}
	return nil
}

// GetLabels returns the secondary-label set for an entry. Order is
// alphabetic-stable for deterministic rendering. Empty slice for an
// entry that exists but carries no secondaries; ErrNotFound when the
// id doesn't resolve to any entry row — without that distinction,
// callers (HTTP, CLI) couldn't tell "no labels yet" from "fabricated
// id", which leaks the endpoint as an existence probe and breaks REST
// semantics for the HTTP wrapper.
func (s *Service) GetLabels(ctx context.Context, entryID string) ([]domain.EntryType, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM entries WHERE id = ? LIMIT 1`, entryID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: entry %s", ErrNotFound, entryID)
	}
	if err != nil {
		return nil, fmt.Errorf("repository: lookup entry %s: %w", entryID, err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT label FROM entry_labels WHERE entry_id = ? ORDER BY label`,
		entryID)
	if err != nil {
		return nil, fmt.Errorf("repository: get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.EntryType
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, fmt.Errorf("repository: scan label: %w", err)
		}
		out = append(out, domain.EntryType(l))
	}
	return out, rows.Err()
}

// EntriesWithLabel returns the live entries that carry the given label
// either as their primary type OR in their secondary labels. The union
// is computed in one query so callers don't have to dedupe.
func (s *Service) EntriesWithLabel(ctx context.Context, label domain.EntryType, limit int) ([]domain.Entry, error) {
	if _, ok := validSecondaryLabels[label]; !ok {
		return nil, fmt.Errorf("%w: label %q not in {activity, research, discuss, decision}",
			ErrInvalidInput, label)
	}
	if limit <= 0 {
		limit = 100
	}

	// UNION primary-type match + secondary-label match, dedupe by id.
	// Column order is locked to scanEntryRows (kept in labelEntryColumns
	// up top so drift is visible).
	q := `
SELECT DISTINCT ` + labelEntryColumns + `
  FROM entries e
  LEFT JOIN entry_labels el ON el.entry_id = e.id
 WHERE e.is_current = 1
   AND (e.type = ? OR el.label = ?)
 ORDER BY e.created_at DESC
 LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, string(label), string(label), limit)
	if err != nil {
		return nil, fmt.Errorf("repository: entries with label: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanEntryRows(rows)
}
