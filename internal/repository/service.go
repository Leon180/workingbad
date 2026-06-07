package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/domain"
)

// Service is the single write gateway into the truth source
// (truth-source-schema invariant 10). CLI, HTTP, and sync adapters all call
// these methods; nothing else may write to `entries` / `edges` / FTS5.
type Service struct {
	db *sql.DB
}

// NewService wraps an already-open *sql.DB (from Open). The service does not
// take ownership; callers manage Close.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// InsertEntry validates, assigns IDs and timestamps, then writes the entry
// plus its FTS5 mirror in one transaction.
//
// New entries:
//   - ID assigned (uuid v7) if empty.
//   - LogicalID = ID for a freshly-created entry; preserves invariant that
//     LogicalID is the stable identity across future supersede chains.
//   - IsCurrent is forced to true regardless of input.
func (s *Service) InsertEntry(ctx context.Context, e domain.Entry) (domain.Entry, error) {
	if err := validateEntry(e); err != nil {
		return domain.Entry{}, err
	}
	if err := assignNewIDs(&e); err != nil {
		return domain.Entry{}, err
	}
	stampTimes(&e, time.Now().UTC())
	e.IsCurrent = true

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertEntryRow(ctx, tx, e); err != nil {
		return domain.Entry{}, err
	}
	if err := insertFTS(ctx, tx, e); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: commit: %w", err)
	}
	return e, nil
}

// Supersede appends a new entry version that replaces oldID. The replacement
// inherits LogicalID from the old entry, the old entry is marked superseded,
// and FTS5 is updated to point at the new content — all in one transaction.
//
// Errors if oldID does not exist or is not currently is_current=1.
func (s *Service) Supersede(ctx context.Context, oldID string, replacement domain.Entry) (domain.Entry, error) {
	if err := validateEntry(replacement); err != nil {
		return domain.Entry{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		oldLogicalID string
		oldIsCurrent int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT logical_id, is_current FROM entries WHERE id = ?`, oldID,
	).Scan(&oldLogicalID, &oldIsCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Entry{}, fmt.Errorf("repository: old entry %q not found", oldID)
	}
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: load old: %w", err)
	}
	if oldIsCurrent != 1 {
		return domain.Entry{}, fmt.Errorf("repository: old entry %q is not current", oldID)
	}

	if err := assignNewIDs(&replacement); err != nil {
		return domain.Entry{}, err
	}
	replacement.LogicalID = oldLogicalID
	now := time.Now().UTC()
	stampTimes(&replacement, now)
	replacement.IsCurrent = true

	// Mark old superseded, drop its FTS row, write replacement.
	if _, err := tx.ExecContext(ctx,
		`UPDATE entries SET is_current = 0, superseded_by = ?, updated_at = ? WHERE id = ?`,
		replacement.ID, now.Format(time.RFC3339Nano), oldID,
	); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: mark superseded: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entries_fts WHERE entry_id = ?`, oldID,
	); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: drop old fts: %w", err)
	}
	if err := insertEntryRow(ctx, tx, replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := insertFTS(ctx, tx, replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: commit: %w", err)
	}
	return replacement, nil
}

// validateEntry enforces the per-type / per-source contracts the schema can't
// express in pure DDL.
func validateEntry(e domain.Entry) error {
	if e.Title == "" {
		return errors.New("entry: title required")
	}
	if e.Origin == "" {
		return errors.New("entry: origin required")
	}
	switch e.Origin {
	case domain.OriginFetched, domain.OriginPushed, domain.OriginLocal:
	default:
		return fmt.Errorf("entry: invalid origin %q", e.Origin)
	}
	switch e.Type {
	case domain.EntryTypeGoal:
		if e.Status == "" {
			return errors.New("entry: goal requires status")
		}
		switch e.Status {
		case domain.StatusOpen, domain.StatusInProgress, domain.StatusDone, domain.StatusArchived:
		default:
			return fmt.Errorf("entry: invalid goal status %q", e.Status)
		}
	case domain.EntryTypeActivity,
		domain.EntryTypeResearch,
		domain.EntryTypeDiscuss,
		domain.EntryTypeDecision:
		if e.Status != "" {
			return fmt.Errorf("entry: %s entries must have empty status", e.Type)
		}
	case "":
		return errors.New("entry: type required")
	default:
		return fmt.Errorf("entry: unknown type %q", e.Type)
	}
	if e.Source == "" {
		return errors.New("entry: source required")
	}
	if e.Source == domain.SourceManual && e.SourceRef == "" {
		return errors.New("entry: manual source requires source_ref (content hash)")
	}
	return nil
}

func assignNewIDs(e *domain.Entry) error {
	if e.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("entry: generate id: %w", err)
		}
		e.ID = id.String()
	}
	if e.LogicalID == "" {
		e.LogicalID = e.ID
	}
	return nil
}

func stampTimes(e *domain.Entry, now time.Time) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
}

func insertEntryRow(ctx context.Context, tx *sql.Tx, e domain.Entry) error {
	metadata := e.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO entries
		   (id, logical_id, type, title, body, source, source_ref, origin, repo_id, author, status, is_current, superseded_by, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.LogicalID, string(e.Type), e.Title, e.Body,
		string(e.Source), nullableString(e.SourceRef),
		string(e.Origin), nullableString(e.RepoID), nullableString(e.Author),
		nullableString(string(e.Status)),
		boolToInt(e.IsCurrent), nullableString(e.SupersededBy),
		metadata,
		e.CreatedAt.Format(time.RFC3339Nano),
		e.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("repository: insert entry: %w", err)
	}
	return nil
}

func insertFTS(ctx context.Context, tx *sql.Tx, e domain.Entry) error {
	if !e.IsCurrent {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO entries_fts (entry_id, title, body) VALUES (?, ?, ?)`,
		e.ID, e.Title, e.Body,
	)
	if err != nil {
		return fmt.Errorf("repository: fts insert: %w", err)
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
