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

// AttachToGoal links an existing activity entry to a goal via a `part_of`
// edge. Idempotent: if a live edge already exists for (activity, goal,
// part_of), returns it instead of inserting a duplicate.
func (s *Service) AttachToGoal(ctx context.Context, activityID, goalID string) (domain.Edge, error) {
	if activityID == "" || goalID == "" {
		return domain.Edge{}, errors.New("repository: AttachToGoal requires activity_id and goal_id")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Edge{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	// Edges key on the node's stable identity (logical_id == node.id), not the
	// per-version entry id, so the link survives entry supersede with no
	// re-pointing (decision (a), migration 0017). Callers still pass entry ids;
	// we resolve each to its node id here.
	activityNodeID, err := resolveLiveNodeID(ctx, qtx, activityID, "")
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: activity: %w", err)
	}
	goalNodeID, err := resolveLiveNodeID(ctx, qtx, goalID, "goal")
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: goal: %w", err)
	}

	// Reuse existing live edge if any.
	existingID, err := qtx.GetLiveEdgeByTriple(ctx, sqlcdb.GetLiveEdgeByTripleParams{
		FromID: activityNodeID, ToID: goalNodeID, Relation: string(domain.RelationPartOf),
	})
	switch {
	case err == nil:
		edge, lerr := qtx.GetEdgeByID(ctx, existingID)
		if lerr != nil {
			return domain.Edge{}, lerr
		}
		if cerr := tx.Commit(); cerr != nil {
			return domain.Edge{}, fmt.Errorf("repository: commit: %w", cerr)
		}
		converted, cerr := edgeFromSqlc(edge)
		if cerr != nil {
			return domain.Edge{}, cerr
		}
		return converted, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through
	default:
		return domain.Edge{}, fmt.Errorf("repository: lookup attach: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: gen edge id: %w", err)
	}
	now := time.Now().UTC()
	nowRFC, err := formatRFC(now)
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: format now: %w", err)
	}
	if err := qtx.InsertEdge(ctx, sqlcdb.InsertEdgeParams{
		ID:         id.String(),
		FromID:     activityNodeID,
		ToID:       goalNodeID,
		Relation:   string(domain.RelationPartOf),
		Metadata:   "{}",
		OccurredAt: stringToNS(nowRFC),
		IngestedAt: stringToNS(nowRFC),
		CreatedAt:  nowRFC,
	}); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: insert edge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.Edge{}, err
	}
	return domain.Edge{
		ID:         id.String(),
		FromID:     activityNodeID,
		ToID:       goalNodeID,
		Relation:   domain.RelationPartOf,
		IsCurrent:  true,
		Metadata:   "{}",
		OccurredAt: now,
		IngestedAt: now,
	}, nil
}

// DetachFromGoal marks an existing live edge as superseded.
func (s *Service) DetachFromGoal(ctx context.Context, edgeID string) error {
	if edgeID == "" {
		return errors.New("repository: DetachFromGoal requires edge_id")
	}
	n, err := s.q.DetachEdge(ctx, edgeID)
	if err != nil {
		return fmt.Errorf("repository: detach: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("repository: edge %q not found or already detached: %w", edgeID, ErrNotFound)
	}
	return nil
}

// SetGoalStatus changes a goal entry's status by superseding it in the same tx.
// Attached activities stay linked with no edge rewriting: edges key on the
// goal's logical_id (decision (a)), which the supersede preserves.
func (s *Service) SetGoalStatus(ctx context.Context, goalID string, newStatus domain.Status) (domain.Entry, error) {
	switch newStatus {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusDone, domain.StatusArchived:
	default:
		return domain.Entry{}, fmt.Errorf("repository: invalid goal status %q: %w", newStatus, ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	old, err := qtx.GetEntryByID(ctx, goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Entry{}, fmt.Errorf("repository: goal %q not found: %w", goalID, ErrNotFound)
	}
	if err != nil {
		return domain.Entry{}, err
	}
	current, err := entryFromSqlc(old)
	if err != nil {
		return domain.Entry{}, err
	}
	if current.Type != domain.EntryTypeGoal {
		return domain.Entry{}, fmt.Errorf("repository: entry %q is not a goal (type=%s): %w", goalID, current.Type, ErrInvalidInput)
	}
	if !current.IsCurrent {
		return domain.Entry{}, fmt.Errorf("repository: goal %q is not current: %w", goalID, ErrNotFound)
	}

	replacement := current
	replacement.ID = ""
	replacement.SupersededBy = ""
	replacement.Status = newStatus

	if err := validateEntry(replacement); err != nil {
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	// Edges key on the goal's logical_id (stable across supersede, decision
	// (a)), so attached activities stay linked through this status change with
	// no edge rewriting — the supersede keeps the same logical_id.
	if err := s.supersedeEntryInTx(ctx, qtx, goalID, &replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, err
	}
	return replacement, nil
}

// resolveLiveNodeID validates that id is a live entry (optionally of wantType)
// and returns the node identity edges key on — the entry's logical_id
// (== node.id at this stage). Because logical_id is invariant across an entry's
// supersede chain, edges referencing it never need re-pointing (decision (a)).
func resolveLiveNodeID(ctx context.Context, qtx *sqlcdb.Queries, id, wantType string) (string, error) {
	row, err := qtx.GetEntryNodeRef(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("entry %q not found: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if row.IsCurrent != 1 {
		return "", fmt.Errorf("entry %q is not current: %w", id, ErrNotFound)
	}
	if wantType != "" && row.Type != wantType {
		return "", fmt.Errorf("entry %q has type %q, want %q", id, row.Type, wantType)
	}
	// logical_id is TEXT NOT NULL and backfilled (migration 0009), so this is a
	// data-integrity guard, not an expected path: never write a blank-keyed edge.
	if row.LogicalID == "" {
		return "", fmt.Errorf("repository: entry %q has empty logical_id (data integrity)", id)
	}
	return row.LogicalID, nil
}
