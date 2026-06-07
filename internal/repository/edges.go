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

	if err := assertLive(ctx, qtx, activityID, ""); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: activity: %w", err)
	}
	if err := assertLive(ctx, qtx, goalID, "goal"); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: goal: %w", err)
	}

	// Reuse existing live edge if any.
	existingID, err := qtx.GetLiveEdgeByTriple(ctx, sqlcdb.GetLiveEdgeByTripleParams{
		FromID: activityID, ToID: goalID, Relation: string(domain.RelationPartOf),
	})
	switch {
	case err == nil:
		edge, lerr := qtx.GetEdgeByID(ctx, existingID)
		if lerr != nil {
			return domain.Edge{}, lerr
		}
		if cerr := tx.Commit(); cerr != nil {
			return domain.Edge{}, cerr
		}
		return edgeFromSqlc(edge), nil
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
	if err := qtx.InsertEdge(ctx, sqlcdb.InsertEdgeParams{
		ID:        id.String(),
		FromID:    activityID,
		ToID:      goalID,
		Relation:  string(domain.RelationPartOf),
		Metadata:  "{}",
		CreatedAt: formatRFC(now),
	}); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: insert edge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.Edge{}, err
	}
	return domain.Edge{
		ID:        id.String(),
		FromID:    activityID,
		ToID:      goalID,
		Relation:  domain.RelationPartOf,
		IsCurrent: true,
		Metadata:  "{}",
		CreatedAt: now,
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
		return fmt.Errorf("repository: edge %q not found or already detached", edgeID)
	}
	return nil
}

// SetGoalStatus changes a goal entry's status by superseding it and
// re-points every live incoming edge to the new id in the same tx.
func (s *Service) SetGoalStatus(ctx context.Context, goalID string, newStatus domain.Status) (domain.Entry, error) {
	switch newStatus {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusDone, domain.StatusArchived:
	default:
		return domain.Entry{}, fmt.Errorf("repository: invalid goal status %q", newStatus)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	old, err := qtx.GetEntryByID(ctx, goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Entry{}, fmt.Errorf("repository: goal %q not found", goalID)
	}
	if err != nil {
		return domain.Entry{}, err
	}
	current := entryFromSqlc(old)
	if current.Type != domain.EntryTypeGoal {
		return domain.Entry{}, fmt.Errorf("repository: entry %q is not a goal (type=%s)", goalID, current.Type)
	}
	if !current.IsCurrent {
		return domain.Entry{}, fmt.Errorf("repository: goal %q is not current", goalID)
	}

	replacement := current
	replacement.ID = ""
	replacement.SupersededBy = ""
	replacement.Status = newStatus

	if err := validateEntry(replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := s.supersedeEntryInTx(ctx, qtx, goalID, &replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := rePointIncomingEdges(ctx, qtx, goalID, replacement.ID); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, err
	}
	return replacement, nil
}

// assertLive returns an error if the entry doesn't exist, isn't current, or
// (when wantType is non-empty) doesn't match the expected type.
func assertLive(ctx context.Context, qtx *sqlcdb.Queries, id, wantType string) error {
	row, err := qtx.GetEntryTypeAndCurrent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("entry %q not found", id)
	}
	if err != nil {
		return err
	}
	if row.IsCurrent != 1 {
		return fmt.Errorf("entry %q is not current", id)
	}
	if wantType != "" && row.Type != wantType {
		return fmt.Errorf("entry %q has type %q, want %q", id, row.Type, wantType)
	}
	return nil
}

// rePointIncomingEdges copies every live incoming edge to the old entry into
// a fresh edge pointing at the new entry, marking the old edges superseded.
func rePointIncomingEdges(ctx context.Context, qtx *sqlcdb.Queries, oldID, newID string) error {
	incoming, err := qtx.GetIncomingLiveEdges(ctx, oldID)
	if err != nil {
		return fmt.Errorf("repository: load incoming edges: %w", err)
	}
	now := formatRFC(time.Now().UTC())
	for _, e := range incoming {
		nextEdgeID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("repository: gen repoint edge id: %w", err)
		}
		next := nextEdgeID.String()
		if err := qtx.SupersedeEdge(ctx, sqlcdb.SupersedeEdgeParams{
			SupersededBy: stringToNS(next), ID: e.ID,
		}); err != nil {
			return fmt.Errorf("repository: flip old edge: %w", err)
		}
		metaArg := e.Metadata
		if metaArg == "" {
			metaArg = "{}"
		}
		if err := qtx.InsertEdge(ctx, sqlcdb.InsertEdgeParams{
			ID:        next,
			FromID:    e.FromID,
			ToID:      newID,
			Relation:  e.Relation,
			Metadata:  metaArg,
			CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("repository: insert repointed edge: %w", err)
		}
	}
	return nil
}
