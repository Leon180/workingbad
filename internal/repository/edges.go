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

// AttachToGoal links an existing activity entry to a goal via a `part_of`
// edge. Idempotent: if a live edge already exists for (activity, goal,
// part_of), returns it instead of inserting a duplicate.
//
// Validates that:
//   - both entries exist and are is_current = 1
//   - the to entry is type=goal
func (s *Service) AttachToGoal(ctx context.Context, activityID, goalID string) (domain.Edge, error) {
	if activityID == "" || goalID == "" {
		return domain.Edge{}, errors.New("repository: AttachToGoal requires activity_id and goal_id")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Edge{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := assertLive(ctx, tx, activityID, ""); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: activity: %w", err)
	}
	if err := assertLive(ctx, tx, goalID, "goal"); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: goal: %w", err)
	}

	// Reuse existing live edge if any.
	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'part_of' AND is_current = 1`,
		activityID, goalID,
	).Scan(&existingID)
	switch {
	case err == nil:
		edge, lerr := loadEdge(ctx, tx, existingID)
		if lerr != nil {
			return domain.Edge{}, lerr
		}
		if cerr := tx.Commit(); cerr != nil {
			return domain.Edge{}, cerr
		}
		return edge, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to insert
	default:
		return domain.Edge{}, fmt.Errorf("repository: lookup attach: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: gen edge id: %w", err)
	}
	now := time.Now().UTC()
	edge := domain.Edge{
		ID:        id.String(),
		FromID:    activityID,
		ToID:      goalID,
		Relation:  domain.RelationPartOf,
		IsCurrent: true,
		Metadata:  "{}",
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO edges (id, from_id, to_id, relation, is_current, metadata, created_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
		edge.ID, edge.FromID, edge.ToID, string(edge.Relation), edge.Metadata, now.Format(time.RFC3339Nano),
	); err != nil {
		return domain.Edge{}, fmt.Errorf("repository: insert edge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.Edge{}, err
	}
	return edge, nil
}

// DetachFromGoal marks an existing live edge as superseded. Returns an error
// if the edge does not exist or is already detached. Append-only: the edge
// row stays in the table for history.
func (s *Service) DetachFromGoal(ctx context.Context, edgeID string) error {
	if edgeID == "" {
		return errors.New("repository: DetachFromGoal requires edge_id")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE edges SET is_current = 0 WHERE id = ? AND is_current = 1`, edgeID,
	)
	if err != nil {
		return fmt.Errorf("repository: detach: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("repository: edge %q not found or already detached", edgeID)
	}
	return nil
}

// SetGoalStatus changes a goal entry's status by superseding it. LogicalID
// is preserved (same logical entity, new physical row) and all live edges
// pointing at the prior version are re-pointed to the new one in the same
// transaction, so part_of queries continue to land on a live to_id.
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

	current, err := loadEntryInTx(ctx, tx, goalID)
	if err != nil {
		return domain.Entry{}, err
	}
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
	if err := s.supersedeEntryInTx(ctx, tx, goalID, &replacement); err != nil {
		return domain.Entry{}, err
	}
	if err := rePointIncomingEdges(ctx, tx, goalID, replacement.ID); err != nil {
		return domain.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Entry{}, err
	}
	return replacement, nil
}

// assertLive returns an error if the entry doesn't exist, isn't current, or
// (when wantType is non-empty) doesn't match the expected type.
func assertLive(ctx context.Context, tx *sql.Tx, id, wantType string) error {
	var typ string
	var isCurrent int
	err := tx.QueryRowContext(ctx,
		`SELECT type, is_current FROM entries WHERE id = ?`, id,
	).Scan(&typ, &isCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("entry %q not found", id)
	}
	if err != nil {
		return err
	}
	if isCurrent != 1 {
		return fmt.Errorf("entry %q is not current", id)
	}
	if wantType != "" && typ != wantType {
		return fmt.Errorf("entry %q has type %q, want %q", id, typ, wantType)
	}
	return nil
}

// rePointIncomingEdges copies every live incoming edge to the old entry into
// a fresh edge pointing at the new entry, marking the old edges superseded.
// The new edges inherit from_id, relation, and metadata.
func rePointIncomingEdges(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, from_id, relation, metadata FROM edges WHERE to_id = ? AND is_current = 1`,
		oldID,
	)
	if err != nil {
		return fmt.Errorf("repository: load incoming edges: %w", err)
	}
	type pending struct{ id, fromID, relation, metadata string }
	var edges []pending
	for rows.Next() {
		var e pending
		if err := rows.Scan(&e.id, &e.fromID, &e.relation, &e.metadata); err != nil {
			_ = rows.Close()
			return err
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range edges {
		newEdgeID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("repository: gen repoint edge id: %w", err)
		}
		next := newEdgeID.String()
		if _, err := tx.ExecContext(ctx,
			`UPDATE edges SET is_current = 0, superseded_by = ? WHERE id = ?`, next, e.id,
		); err != nil {
			return fmt.Errorf("repository: flip old edge: %w", err)
		}
		metaArg := e.metadata
		if metaArg == "" {
			metaArg = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (id, from_id, to_id, relation, is_current, metadata, created_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
			next, e.fromID, newID, e.relation, metaArg, now,
		); err != nil {
			return fmt.Errorf("repository: insert repointed edge: %w", err)
		}
	}
	return nil
}

// loadEntryInTx loads a full Entry by id inside an existing tx.
func loadEntryInTx(ctx context.Context, tx *sql.Tx, id string) (domain.Entry, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE id = ?`, id)
	return scanEntry(row)
}

// loadEdge loads a single Edge by id inside the given tx.
func loadEdge(ctx context.Context, tx *sql.Tx, id string) (domain.Edge, error) {
	var e domain.Edge
	var relation string
	var isCurrent int
	var supBy sql.NullString
	var meta string
	var created string
	err := tx.QueryRowContext(ctx,
		`SELECT id, from_id, to_id, relation, is_current, superseded_by, metadata, created_at FROM edges WHERE id = ?`, id,
	).Scan(&e.ID, &e.FromID, &e.ToID, &relation, &isCurrent, &supBy, &meta, &created)
	if err != nil {
		return domain.Edge{}, fmt.Errorf("repository: load edge: %w", err)
	}
	e.Relation = domain.Relation(relation)
	e.IsCurrent = isCurrent == 1
	if supBy.Valid {
		e.SupersededBy = supBy.String
	}
	e.Metadata = meta
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return e, nil
}
