package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository/sqlcdb"
)

// Service is the single write gateway into the truth source
// (truth-source-schema invariant 10). CLI, HTTP, and sync adapters all call
// these methods; nothing else may write to `entries` / `edges` / FTS5.
//
// Persistence is split between sqlc-generated single-statement queries
// (the q field) and hand-written dynamic SQL for the few methods sqlc can't
// express cleanly (ListEntries / CountPendingSegments with optional WHERE).
type Service struct {
	db *sql.DB
	q  *sqlcdb.Queries
}

// NewService wraps an already-open *sql.DB (from Open). The service does not
// take ownership; callers manage Close.
func NewService(db *sql.DB) *Service {
	return &Service{
		db: db,
		q:  sqlcdb.New(db),
	}
}

// InsertEntry validates, assigns IDs and timestamps, then writes the entry
// plus its FTS5 mirror in one transaction.
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

	qtx := s.q.WithTx(tx)
	params, err := entryToInsertParams(e)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: marshal entry: %w", err)
	}
	if err := qtx.InsertEntryRow(ctx, params); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: insert entry: %w", err)
	}
	if err := qtx.InsertEntryFTS(ctx, sqlcdb.InsertEntryFTSParams{
		EntryID: e.ID, Title: e.Title, Body: e.Body,
	}); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: fts insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: commit: %w", err)
	}
	return e, nil
}

// Supersede appends a new entry version that replaces oldID. The replacement
// inherits LogicalID from the old entry, the old entry is marked superseded,
// FTS5 is updated, and every live edge touching oldID is re-pointed at the
// new id (both incoming and outgoing) — all in one transaction.
//
// Delegates the bulk of the work to supersedeEntryInTx so service.Supersede /
// materializeOne / SetGoalStatus all share the same supersede behaviour.
func (s *Service) Supersede(ctx context.Context, oldID string, replacement domain.Entry) (domain.Entry, error) {
	if err := validateEntry(replacement); err != nil {
		return domain.Entry{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	if err := s.supersedeEntryInTx(ctx, qtx, oldID, &replacement); err != nil {
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
