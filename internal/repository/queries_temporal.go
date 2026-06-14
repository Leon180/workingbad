package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Time-travel queries for the bitemporal model (grill:
// docs/grill/2026-06-08-time-travel-schema.md §4.7).
//
// All four use the same supersede-chain pattern: for each logical_id,
// pick the row that was current at the given asOf instant. A row
// "was current at asOf" iff:
//   - ingested_at <= asOf
//   - occurred_at <= asOf (event happened before we're asking about)
//   - it has no successor OR the successor was ingested AFTER asOf
//     (it would have been live at asOf because the supersede hadn't
//     happened yet)
//
// We intentionally do NOT build a snapshot table: red team #1 in the grill
// doc rejected it as N×N double-write with no payoff. The supersede chain
// plus idx_entries_logical_occurred (created in migration 0010) handles
// this efficiently up to the Phase 1 scale (~10k entries).

// ListEntriesAt returns the version of each logical entry that was current
// at the given asOf instant, filtered the same way as ListEntries.
func (s *Service) ListEntriesAt(ctx context.Context, asOf time.Time, filter ListFilter) ([]domain.Entry, error) {
	if asOf.IsZero() {
		return nil, fmt.Errorf("repository: ListEntriesAt requires non-zero asOf")
	}
	if filter.Limit <= 0 {
		filter.Limit = DefaultListLimit
	}
	asOfStr := asOf.UTC().Format(time.RFC3339Nano)

	where := []string{
		// Row must have existed by asOf.
		"COALESCE(e.ingested_at, e.created_at) <= ?",
		// Event must have happened by asOf.
		"COALESCE(e.occurred_at, e.ingested_at, e.created_at) <= ?",
		// And either no successor OR the successor came after asOf.
		`(e.superseded_by IS NULL OR EXISTS (
            SELECT 1 FROM entries s
            WHERE s.id = e.superseded_by
              AND COALESCE(s.ingested_at, s.created_at) > ?
        ))`,
	}
	args := []any{asOfStr, asOfStr, asOfStr}
	if filter.Type != "" {
		where = append(where, "e.type = ?")
		args = append(args, string(filter.Type))
	}
	if filter.RepoID != "" {
		where = append(where, "e.repo_id = ?")
		args = append(args, filter.RepoID)
	}
	if !filter.IncludeArchived {
		where = append(where, "COALESCE(e.status, '') != 'archived'")
	}
	args = append(args, filter.Limit)

	q := `SELECT e.id, e.logical_id, e.type, e.title, e.body, e.source,
                 COALESCE(e.source_ref, ''), COALESCE(e.source_event_hash, ''),
                 e.origin, COALESCE(e.repo_id, ''), COALESCE(e.author, ''),
                 COALESCE(e.actor, ''), COALESCE(e.reason, ''),
                 COALESCE(e.status, ''), e.is_current,
                 COALESCE(e.superseded_by, ''), e.metadata,
                 COALESCE(e.version, 1), COALESCE(e.quality_degraded, 0),
                 COALESCE(e.occurred_at, e.ingested_at, e.created_at),
                 COALESCE(e.ingested_at, e.created_at),
                 e.updated_at
            FROM entries e
           WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY COALESCE(e.occurred_at, e.ingested_at, e.created_at) DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: ListEntriesAt: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanEntryRows(rows)
}

// EntryHistory returns every version in the supersede chain for the given
// logical_id, newest first. Useful for "show me how this decision evolved".
func (s *Service) EntryHistory(ctx context.Context, logicalID string) ([]domain.Entry, error) {
	if logicalID == "" {
		return nil, fmt.Errorf("repository: EntryHistory requires logical_id")
	}
	q := `SELECT e.id, e.logical_id, e.type, e.title, e.body, e.source,
                 COALESCE(e.source_ref, ''), COALESCE(e.source_event_hash, ''),
                 e.origin, COALESCE(e.repo_id, ''), COALESCE(e.author, ''),
                 COALESCE(e.actor, ''), COALESCE(e.reason, ''),
                 COALESCE(e.status, ''), e.is_current,
                 COALESCE(e.superseded_by, ''), e.metadata,
                 COALESCE(e.version, 1), COALESCE(e.quality_degraded, 0),
                 COALESCE(e.occurred_at, e.ingested_at, e.created_at),
                 COALESCE(e.ingested_at, e.created_at),
                 e.updated_at
            FROM entries e
           WHERE e.logical_id = ?
           ORDER BY COALESCE(e.version, 1) DESC, COALESCE(e.ingested_at, e.created_at) DESC`

	rows, err := s.db.QueryContext(ctx, q, logicalID)
	if err != nil {
		return nil, fmt.Errorf("repository: EntryHistory: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanEntryRows(rows)
}

// GoalActivitiesAt returns the activities attached to a goal as of the
// given asOf instant. Sweeps the goal's full LogicalID chain (so status
// edits don't break the link) and shows only the activities + edges
// that were live at asOf.
func (s *Service) GoalActivitiesAt(ctx context.Context, goalID string, asOf time.Time) ([]domain.Entry, error) {
	if goalID == "" {
		return nil, fmt.Errorf("repository: GoalActivitiesAt requires goal id")
	}
	if asOf.IsZero() {
		return nil, fmt.Errorf("repository: GoalActivitiesAt requires non-zero asOf")
	}
	asOfStr := asOf.UTC().Format(time.RFC3339Nano)

	// Both the activity and the part_of edge must have been live at asOf.
	// The edge's "successor came after asOf" condition uses its own ingest
	// timestamp; the activity uses its supersede chain (just like
	// ListEntriesAt). Edges key on logical_id (decision (a), migration 0017),
	// so the activity joins by its logical_id and the goal is matched directly
	// by its logical_id — no goal-version chain needed.
	q := `SELECT e.id, e.logical_id, e.type, e.title, e.body, e.source,
                 COALESCE(e.source_ref, ''), COALESCE(e.source_event_hash, ''),
                 e.origin, COALESCE(e.repo_id, ''), COALESCE(e.author, ''),
                 COALESCE(e.actor, ''), COALESCE(e.reason, ''),
                 COALESCE(e.status, ''), e.is_current,
                 COALESCE(e.superseded_by, ''), e.metadata,
                 COALESCE(e.version, 1), COALESCE(e.quality_degraded, 0),
                 COALESCE(e.occurred_at, e.ingested_at, e.created_at),
                 COALESCE(e.ingested_at, e.created_at),
                 e.updated_at
            FROM entries e
            JOIN edges  ed ON ed.from_id = e.logical_id AND ed.relation = 'part_of'
           WHERE ed.to_id = (SELECT seed.logical_id FROM entries seed WHERE seed.id = ?)
             AND COALESCE(ed.ingested_at, ed.created_at) <= ?
             AND (ed.superseded_by IS NULL OR EXISTS (
                 SELECT 1 FROM edges se
                 WHERE se.id = ed.superseded_by
                   AND COALESCE(se.ingested_at, se.created_at) > ?
             ))
             AND e.type = 'activity'
             AND COALESCE(e.ingested_at, e.created_at) <= ?
             AND COALESCE(e.occurred_at, e.ingested_at, e.created_at) <= ?
             AND (e.superseded_by IS NULL OR EXISTS (
                 SELECT 1 FROM entries se
                 WHERE se.id = e.superseded_by
                   AND COALESCE(se.ingested_at, se.created_at) > ?
             ))
           ORDER BY COALESCE(e.occurred_at, e.ingested_at, e.created_at) ASC`

	rows, err := s.db.QueryContext(ctx, q, goalID, asOfStr, asOfStr, asOfStr, asOfStr, asOfStr)
	if err != nil {
		return nil, fmt.Errorf("repository: GoalActivitiesAt: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanEntryRows(rows)
}

// EdgeFilter narrows EdgesAt. Empty fields are wildcards.
type EdgeFilter struct {
	Relation domain.Relation
	FromID   string
	ToID     string
}

// EdgesAt returns the edges that were live at the given asOf instant. Edges
// key on the node's logical_id (decision (a)), so a link's identity is stable
// across entry supersede and EdgesAt(T) reflects the relationship that actually
// existed at T. from_id/to_id filters take node ids (logical_id).
func (s *Service) EdgesAt(ctx context.Context, asOf time.Time, filter EdgeFilter) ([]domain.Edge, error) {
	if asOf.IsZero() {
		return nil, fmt.Errorf("repository: EdgesAt requires non-zero asOf")
	}
	asOfStr := asOf.UTC().Format(time.RFC3339Nano)

	where := []string{
		"COALESCE(ed.ingested_at, ed.created_at) <= ?",
		"COALESCE(ed.occurred_at, ed.ingested_at, ed.created_at) <= ?",
		`(ed.superseded_by IS NULL OR EXISTS (
            SELECT 1 FROM edges se
            WHERE se.id = ed.superseded_by
              AND COALESCE(se.ingested_at, se.created_at) > ?
        ))`,
	}
	args := []any{asOfStr, asOfStr, asOfStr}
	if filter.Relation != "" {
		where = append(where, "ed.relation = ?")
		args = append(args, string(filter.Relation))
	}
	if filter.FromID != "" {
		where = append(where, "ed.from_id = ?")
		args = append(args, filter.FromID)
	}
	if filter.ToID != "" {
		where = append(where, "ed.to_id = ?")
		args = append(args, filter.ToID)
	}

	q := `SELECT ed.id, ed.from_id, ed.to_id, ed.relation, ed.is_current,
                 COALESCE(ed.superseded_by, ''),
                 COALESCE(ed.actor, ''), COALESCE(ed.reason, ''),
                 ed.metadata,
                 COALESCE(ed.occurred_at, ed.ingested_at, ed.created_at),
                 COALESCE(ed.ingested_at, ed.created_at)
            FROM edges ed
           WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY COALESCE(ed.occurred_at, ed.ingested_at, ed.created_at) DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: EdgesAt: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Edge
	for rows.Next() {
		var e domain.Edge
		var rel, supBy, actor, reason, occurred, ingested string
		var isCur int
		if err := rows.Scan(
			&e.ID, &e.FromID, &e.ToID, &rel, &isCur, &supBy,
			&actor, &reason, &e.Metadata, &occurred, &ingested,
		); err != nil {
			return nil, err
		}
		e.Relation = domain.Relation(rel)
		e.IsCurrent = isCur == 1
		e.SupersededBy = supBy
		e.Actor = actor
		e.Reason = reason
		if e.OccurredAt, err = parseRFC(occurred); err != nil {
			return nil, fmt.Errorf("repository: parse edge %s occurred_at: %w", e.ID, err)
		}
		if e.IngestedAt, err = parseRFC(ingested); err != nil {
			return nil, fmt.Errorf("repository: parse edge %s ingested_at: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanEntryRows is the shared row-scan used by ListEntriesAt /
// EntryHistory / GoalActivitiesAt. Column order is fixed across all three
// queries.
func scanEntryRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]domain.Entry, error) {
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
		var err error
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
