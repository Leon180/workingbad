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
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := assignNewIDs(&e); err != nil {
		return domain.Entry{}, err
	}
	stampTimes(&e, time.Now().UTC())
	e.IsCurrent = true
	if e.Version <= 0 {
		e.Version = 1
	}

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
	if err := insertSourceRefAliasTx(ctx, tx, e.ID, e.Source, e.SourceRef); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: commit: %w", err)
	}
	return e, nil
}

// ErrVersionConflict is returned by Supersede when expectedVersion does not
// match the live row's Version. This is the optimistic-lock contract: two
// concurrent supersede attempts based on the same expected_version cannot
// both succeed; the loser must re-read and retry. Phase 1 single-writer
// SQLite has no real race today, but the structure is in place for Phase 2
// concurrent sync workers (red team #1 in grill doc).
var ErrVersionConflict = errors.New("repository: supersede version conflict")

// ErrNotFound is the sentinel for "the row you asked for doesn't exist,
// or isn't the current version anymore". Adapter layers (CLI, HTTP)
// use errors.Is to map this to the right surface response (404 / exit code).
// Avoids fragile string-matching on error messages.
var ErrNotFound = errors.New("repository: not found")

// ErrInvalidInput is the sentinel for caller-supplied data that fails
// validation (bad status, bad type, etc.). Adapter layers map this to 400.
var ErrInvalidInput = errors.New("repository: invalid input")

// Supersede appends a new entry version that replaces oldID. The replacement
// inherits LogicalID + OccurredAt from the old entry, the old entry is
// marked superseded, FTS5 is updated, and every live edge touching oldID is
// re-pointed at the new id (both incoming and outgoing) — all in one tx.
//
// expectedVersion is the optimistic lock: pass 0 to skip the check (legacy
// path / single-writer), pass the value the caller observed when reading
// the live row to detect concurrent writes.
//
// Delegates to supersedeEntryInTx so materializeOne / SetGoalStatus all
// share the same supersede behaviour.
func (s *Service) Supersede(ctx context.Context, oldID string, expectedVersion int, replacement domain.Entry) (domain.Entry, error) {
	if err := validateEntry(replacement); err != nil {
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("repository: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	if err := s.supersedeEntryInTxWithExpected(ctx, qtx, oldID, expectedVersion, &replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, fmt.Errorf("repository: commit: %w", err)
	}
	return replacement, nil
}

// FutureOccurredAtTolerance is the cap beyond which a future OccurredAt
// triggers a warning log. Below this it's accepted silently — ClickUp
// due_dates, scheduled events, and clock skew across distributed sources
// all routinely produce timestamps slightly in the future. Capping at 0
// (red team #5: "no 5min cap") would falsely reject legitimate events.
const FutureOccurredAtTolerance = 24 * time.Hour

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
		// Non-goal entries default to no status. The single exception is
		// archived — issue #51 — so a user can soft-delete a duplicate /
		// junk entry without supersede + empty content gymnastics. Any
		// other goal-only status (open/in_progress/done) is still wrong
		// on these types and surfaces as the same validator error.
		if e.Status != "" && e.Status != domain.StatusArchived {
			return fmt.Errorf("entry: %s entries can only carry status=archived (or empty)", e.Type)
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
	// Bitemporal validator additions (red team #5 + #4 in grill doc):
	//   - Future occurred_at within FutureOccurredAtTolerance is OK
	//     (ClickUp due_date, scheduled events, NTP skew).
	//   - Beyond the tolerance, log a warning event but DO NOT reject —
	//     rejecting would block legitimate forward-dated entries.
	//   - Fetched origin missing occurred_at falls back to ingestion time
	//     with QualityDegraded=true (assignment happens at the write site,
	//     not here — validateEntry is pure).
	if !e.OccurredAt.IsZero() {
		if delta := time.Until(e.OccurredAt); delta > FutureOccurredAtTolerance {
			// Don't reject — just surface that this entry's occurred_at is
			// suspiciously far in the future. Production code paths can
			// observe this through structured logging once we wire it up.
			_ = delta // explicit no-op; future-dated entries are allowed
		}
	}
	if e.Version < 0 {
		return fmt.Errorf("entry: version must be >= 0, got %d", e.Version)
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

// stampTimes assigns the bitemporal write-time fields on a fresh-or-replacement
// entry just before persistence. IngestedAt is always the supplied wall-clock
// now (system time of this write); OccurredAt is preserved if the caller
// already set it, otherwise defaults to now. UpdatedAt mirrors IngestedAt
// under append-only.
func stampTimes(e *domain.Entry, now time.Time) {
	e.IngestedAt = now
	if e.OccurredAt.IsZero() {
		e.OccurredAt = now
	}
	e.UpdatedAt = now
}
