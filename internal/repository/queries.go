package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// entryColumns is the canonical SELECT list for an Entry row. Centralised so
// every read path matches scanEntry's expectations.
const entryColumns = `id, logical_id, type, title, body, source, COALESCE(source_ref, ''), origin, COALESCE(repo_id, ''), COALESCE(author, ''), COALESCE(status, ''), is_current, COALESCE(superseded_by, ''), metadata, created_at, updated_at`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanEntry decodes one entries row into a domain.Entry. Caller chose the
// SELECT columns to match entryColumns.
func scanEntry(r rowScanner) (domain.Entry, error) {
	var e domain.Entry
	var typ, src, origin, status, srcRef, repoID, author, supBy, created, updated string
	var isCur int
	if err := r.Scan(
		&e.ID, &e.LogicalID, &typ, &e.Title, &e.Body,
		&src, &srcRef, &origin, &repoID, &author, &status,
		&isCur, &supBy, &e.Metadata, &created, &updated,
	); err != nil {
		return domain.Entry{}, err
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
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return e, nil
}

// ListFilter narrows ListEntries. Empty fields are wildcards.
type ListFilter struct {
	Type   domain.EntryType
	RepoID string
	Limit  int // <=0 → DefaultListLimit
}

// DefaultListLimit caps ListEntries when the caller did not specify.
const DefaultListLimit = 100

// ListEntries returns is_current entries matching the filter, newest first.
// Caps unbounded queries at DefaultListLimit.
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
	q := `SELECT ` + entryColumns + ` FROM entries WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY created_at DESC LIMIT ?`
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountPendingSegments counts segments still needing materialize (pending or
// stale). Used by the UI counter row so callers see how much LLM work is
// queued without paying for it.
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

// entryColumnsE is the e-aliased form of entryColumns for JOIN-bearing
// queries. Kept in sync by hand — split by ", " breaks inside COALESCE
// arguments, so a hand-written constant is simpler than a tokeniser.
const entryColumnsE = `e.id, e.logical_id, e.type, e.title, e.body, e.source, COALESCE(e.source_ref, ''), e.origin, COALESCE(e.repo_id, ''), COALESCE(e.author, ''), COALESCE(e.status, ''), e.is_current, COALESCE(e.superseded_by, ''), e.metadata, e.created_at, e.updated_at`

// GetGoalActivities returns live activity entries linked via `part_of` to
// any goal sharing the supplied goal's LogicalID. This naturally follows the
// supersede chain (status edits + title edits stay attached to the same
// logical goal) thanks to rePointIncomingEdges in SetGoalStatus.
//
// iteration_of traversal (strategic goal stacking with distinct LogicalIDs)
// is intentionally deferred — additive, can land when iterate UX exists.
func (s *Service) GetGoalActivities(ctx context.Context, goalID string) ([]domain.Entry, error) {
	q := `SELECT ` + entryColumnsE + `
	        FROM entries AS e
	        JOIN edges   AS ed ON ed.from_id = e.id AND ed.relation = 'part_of' AND ed.is_current = 1
	        JOIN entries AS g  ON ed.to_id = g.id
	       WHERE g.logical_id = (SELECT logical_id FROM entries WHERE id = ?)
	         AND e.type = 'activity'
	         AND e.is_current = 1
	       ORDER BY e.created_at ASC`

	rows, err := s.db.QueryContext(ctx, q, goalID)
	if err != nil {
		return nil, fmt.Errorf("repository: goal activities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
