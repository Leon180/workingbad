package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// ListFilter narrows ListEntries. Empty fields are wildcards.
type ListFilter struct {
	Type   domain.EntryType
	RepoID string
	Limit  int // <=0 → DefaultListLimit
}

// DefaultListLimit caps ListEntries when the caller did not specify.
const DefaultListLimit = 100

// ListEntries uses hand-written SQL because the optional Type/RepoID filters
// don't slot cleanly into sqlc. Returns is_current entries newest first.
func (s *Service) ListEntries(ctx context.Context, filter ListFilter) ([]domain.Entry, error) {
	if filter.Limit <= 0 {
		filter.Limit = DefaultListLimit
	}
	where := []string{"is_current = 1"}
	args := []any{}
	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(filter.Type))
	}
	if filter.RepoID != "" {
		where = append(where, "repo_id = ?")
		args = append(args, filter.RepoID)
	}
	q := `SELECT id, logical_id, type, title, body, source, COALESCE(source_ref, ''), origin, COALESCE(repo_id, ''), COALESCE(author, ''), COALESCE(status, ''), is_current, COALESCE(superseded_by, ''), metadata, created_at, updated_at
	        FROM entries WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY created_at DESC LIMIT ?`
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Entry
	for rows.Next() {
		var e domain.Entry
		var typ, src, origin, status, srcRef, repoID, author, supBy, created, updated string
		var isCur int
		if err := rows.Scan(&e.ID, &e.LogicalID, &typ, &e.Title, &e.Body, &src, &srcRef, &origin, &repoID, &author, &status, &isCur, &supBy, &e.Metadata, &created, &updated); err != nil {
			return nil, err
		}
		e.Type = domain.EntryType(typ)
		e.Source = domain.Source(src)
		e.SourceRef = srcRef
		e.Origin = domain.Origin(origin)
		e.RepoID = repoID
		e.Author = author
		e.Status = domain.Status(status)
		e.IsCurrent = isCur == 1
		e.SupersededBy = supBy
		if e.CreatedAt, err = parseRFC(created); err != nil {
			return nil, fmt.Errorf("repository: parse entry %s created_at: %w", e.ID, err)
		}
		if e.UpdatedAt, err = parseRFC(updated); err != nil {
			return nil, fmt.Errorf("repository: parse entry %s updated_at: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountPendingSegments uses hand-written SQL for the same reason as
// ListEntries — optional repo scope filter doesn't slot into sqlc.
func (s *Service) CountPendingSegments(ctx context.Context, scope MaterializeScope) (int, error) {
	args := []any{string(domain.SummaryStatePending), string(domain.SummaryStateStale)}
	q := `SELECT COUNT(*) FROM segments WHERE summary_state IN (?, ?)`
	if scope.RepoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, scope.RepoID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("repository: count pending: %w", err)
	}
	return n, nil
}

// GetGoalActivities walks the goal's LogicalID chain so status edits stay
// attached, returning live activities via live part_of edges. Backed by the
// sqlc-generated GetGoalActivitiesByLogicalID query.
//
// iteration_of traversal (strategic goal stacking with distinct LogicalIDs)
// is intentionally deferred — additive, can land when iterate UX exists.
func (s *Service) GetGoalActivities(ctx context.Context, goalID string) ([]domain.Entry, error) {
	rows, err := s.q.GetGoalActivitiesByLogicalID(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("repository: goal activities: %w", err)
	}
	out := make([]domain.Entry, len(rows))
	for i, r := range rows {
		converted, cerr := entryFromSqlc(r)
		if cerr != nil {
			return nil, cerr
		}
		out[i] = converted
	}
	return out, nil
}
