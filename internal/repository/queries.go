package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// ListFilter narrows ListEntries. Empty fields are wildcards.
//
// IncludeArchived defaults to false — soft-deleted (status=archived)
// entries are hidden from the default list so an engineer who archived
// a junk entry doesn't keep seeing it. The CLI / Web UI can flip it
// on via --include-archived / ?include_archived=true when the user
// wants to review the archive.
type ListFilter struct {
	Type            domain.EntryType
	RepoID          string
	Limit           int // <=0 → DefaultListLimit
	IncludeArchived bool
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
	if !filter.IncludeArchived {
		// status defaults to NULL/"" for non-goal entries (issue #51
		// extended that to allow status=archived too). Hide either NULL
		// or '' is irrelevant — we only want to filter OUT explicit
		// 'archived'. COALESCE keeps NULL-status rows visible.
		where = append(where, "COALESCE(status, '') != 'archived'")
	}
	q := `SELECT id, logical_id, type, title, body, source, COALESCE(source_ref, ''),
                 COALESCE(source_event_hash, ''), origin, COALESCE(repo_id, ''),
                 COALESCE(author, ''), COALESCE(actor, ''), COALESCE(reason, ''),
                 COALESCE(status, ''), is_current, COALESCE(superseded_by, ''),
                 metadata, COALESCE(version, 1), COALESCE(quality_degraded, 0),
                 COALESCE(occurred_at, ingested_at, created_at),
                 COALESCE(ingested_at, created_at),
                 updated_at
            FROM entries WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY occurred_at DESC, ingested_at DESC LIMIT ?`
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Entry
	for rows.Next() {
		var e domain.Entry
		var typ, src, origin, status, srcRef, hash, repoID, author, actor, reason, supBy, occurred, ingested, updated string
		var isCur, qDeg, version int
		if err := rows.Scan(
			&e.ID, &e.LogicalID, &typ, &e.Title, &e.Body, &src, &srcRef,
			&hash, &origin, &repoID, &author, &actor, &reason, &status,
			&isCur, &supBy, &e.Metadata, &version, &qDeg,
			&occurred, &ingested, &updated,
		); err != nil {
			return nil, err
		}
		e.Type = domain.EntryType(typ)
		e.Source = domain.Source(src)
		e.SourceRef = srcRef
		e.SourceEventHash = hash
		e.Origin = domain.Origin(origin)
		e.RepoID = repoID
		e.Author = author
		e.Actor = actor
		e.Reason = reason
		e.Status = domain.Status(status)
		e.IsCurrent = isCur == 1
		e.SupersededBy = supBy
		e.Version = version
		e.QualityDegraded = qDeg == 1
		if e.OccurredAt, err = parseRFC(occurred); err != nil {
			return nil, fmt.Errorf("repository: parse entry %s occurred_at: %w", e.ID, err)
		}
		if e.IngestedAt, err = parseRFC(ingested); err != nil {
			return nil, fmt.Errorf("repository: parse entry %s ingested_at: %w", e.ID, err)
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

// GetEntryLogicalIDByID returns the logical_id for a given entry id (any
// version in the supersede chain). Uses the entries PK lookup; bounded
// regardless of total entry count.
//
// Returns ErrNotFound when the id doesn't exist — adapters can map cleanly
// to 404 without scanning or substring-matching errors. Replaces the
// pre-fix ListEntries(Limit=1000) scan-fallback in the Web layer's
// resolveLogical helper, which silently 404'd at row 1001.
func (s *Service) GetEntryLogicalIDByID(ctx context.Context, id string) (string, error) {
	logical, err := s.q.GetEntryLogicalIDByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("repository: entry %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("repository: lookup logical_id for %q: %w", id, err)
	}
	return logical, nil
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
