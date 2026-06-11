package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Leon180/workingbad/internal/domain"
)

// SourceRefAlias is one (source, source_ref) tuple that resolves to an
// entry. After future merge operations a single entry may carry multiple
// aliases. Today every entry has exactly one alias matching its primary
// entries.source / entries.source_ref.
type SourceRefAlias struct {
	Source    domain.Source
	SourceRef string
}

// EntryBySourceRef walks: entry_source_refs → anchor entry id → its
// logical_id → the live version of that chain. The anchor pattern means
// the alias row is set once (at first insert) and never rewritten on
// supersede — the live version is found via the bitemporal chain, the
// same way it works everywhere else.
//
// Returns ErrNotFound when no alias row matches OR when the alias points
// at a logical_id that has no live version (entirely superseded away).
func (s *Service) EntryBySourceRef(ctx context.Context, source domain.Source, sourceRef string) (domain.Entry, error) {
	if source == "" || sourceRef == "" {
		return domain.Entry{}, fmt.Errorf("%w: source and source_ref required", ErrInvalidInput)
	}

	q := `
SELECT ` + labelEntryColumns + `
  FROM entry_source_refs sr
  JOIN entries anchor ON anchor.id = sr.entry_id
  JOIN entries e      ON e.logical_id = anchor.logical_id AND e.is_current = 1
 WHERE sr.source = ? AND sr.source_ref = ?
 LIMIT 1`

	rows, err := s.db.QueryContext(ctx, q, string(source), sourceRef)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: EntryBySourceRef: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries, err := scanEntryRows(rows)
	if err != nil {
		return domain.Entry{}, err
	}
	if len(entries) == 0 {
		return domain.Entry{}, fmt.Errorf("%w: source=%s ref=%s", ErrNotFound, source, sourceRef)
	}
	return entries[0], nil
}

// AllSourceRefs returns every alias tuple that resolves to entryID's
// logical chain. Order is (source, source_ref) ASC for stable rendering.
//
// Implemented as a single self-join over entries (entry → its logical_id
// chain → aliases) rather than the natural "fetch logical_id then look
// up aliases" pair of queries — one round-trip instead of two, which
// matters when the caller batches AllSourceRefs across many entries
// (UI render path may do this when showing chips per node).
//
// We distinguish "unknown entry" from "no aliases" by counting matched
// entry rows; sql.ErrNoRows on the alias select would conflate the two.
func (s *Service) AllSourceRefs(ctx context.Context, entryID string) ([]SourceRefAlias, error) {
	if entryID == "" {
		return nil, fmt.Errorf("%w: entry id required", ErrInvalidInput)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT sr.source, sr.source_ref
  FROM entries target
  JOIN entries e ON e.logical_id = target.logical_id
  JOIN entry_source_refs sr ON sr.entry_id = e.id
 WHERE target.id = ?
 ORDER BY sr.source, sr.source_ref`, entryID)
	if err != nil {
		return nil, fmt.Errorf("repository: AllSourceRefs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SourceRefAlias
	for rows.Next() {
		var a SourceRefAlias
		var src string
		if err := rows.Scan(&src, &a.SourceRef); err != nil {
			return nil, err
		}
		a.Source = domain.Source(src)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Empty result could be "entry doesn't exist" OR "entry exists with
	// no aliases". Resolve by checking entry existence — preserves the
	// ErrNotFound contract for the unknown-entry case.
	if len(out) == 0 {
		var exists int
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM entries WHERE id = ? LIMIT 1`, entryID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: entry %s", ErrNotFound, entryID)
		}
		if err != nil {
			return nil, fmt.Errorf("repository: verify entry %s: %w", entryID, err)
		}
	}
	return out, nil
}

// insertSourceRefAliasTx records the primary (source, source_ref) tuple
// for a freshly-inserted entry. Used inside InsertEntry's tx — exposed
// at package level rather than a method so the supersede path can reuse
// it later without duplication.
//
// INSERT OR IGNORE because supersede creates a fresh entries row with
// the same (source, source_ref) inherited from the live chain; we keep
// the original anchor row so lookups remain stable across version
// churn.
func insertSourceRefAliasTx(ctx context.Context, tx *sql.Tx, entryID string, source domain.Source, sourceRef string) error {
	if sourceRef == "" {
		// Manual entries without a hash, mocks, etc.: no identity to alias.
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO entry_source_refs (entry_id, source, source_ref) VALUES (?, ?, ?)`,
		entryID, string(source), sourceRef); err != nil {
		return fmt.Errorf("repository: insert source_ref alias: %w", err)
	}
	return nil
}
