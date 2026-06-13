package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Node-layer time-travel — the bitemporal analogue of the entry queries in
// queries_temporal.go (grill: docs/grill/2026-06-08-time-travel-schema.md §4.7).
// Nodes carry the same supersede-chain shape as entries (migration 0014), so
// the "version current at asOf" predicate is identical: for each logical_id,
// pick the row that
//   - was ingested by asOf,
//   - whose event occurred by asOf, and
//   - has no successor, OR whose successor was ingested AFTER asOf
//     (the supersede hadn't happened yet at asOf).
// No snapshot table — the chain plus idx_nodes_logical_occurred (0016) handles
// this up to Phase 1 scale, exactly as for entries.

// NodeListFilter narrows ListNodesAt. The zero value lists every node that was
// live at asOf (archived excluded) up to DefaultListLimit.
type NodeListFilter struct {
	Type            domain.EntryType // "" = any type
	IncludeArchived bool             // include status = 'archived' nodes
	Limit           int              // <= 0 → DefaultListLimit
}

// ListNodesAt returns the version of each logical node that was current at the
// given asOf instant — the node-layer analogue of ListEntriesAt.
func (s *Service) ListNodesAt(ctx context.Context, asOf time.Time, filter NodeListFilter) ([]domain.Node, error) {
	if asOf.IsZero() {
		return nil, fmt.Errorf("repository: ListNodesAt requires non-zero asOf")
	}
	if filter.Limit <= 0 {
		filter.Limit = DefaultListLimit
	}
	asOfStr := asOf.UTC().Format(time.RFC3339Nano)

	where := []string{
		// Row must have existed by asOf.
		"COALESCE(n.ingested_at, n.created_at) <= ?",
		// Event must have happened by asOf.
		"COALESCE(n.occurred_at, n.ingested_at, n.created_at) <= ?",
		// And either no successor OR the successor came after asOf.
		`(n.superseded_by IS NULL OR EXISTS (
            SELECT 1 FROM nodes s
            WHERE s.id = n.superseded_by
              AND COALESCE(s.ingested_at, s.created_at) > ?
        ))`,
	}
	args := []any{asOfStr, asOfStr, asOfStr}
	if filter.Type != "" {
		where = append(where, "n.type = ?")
		args = append(args, string(filter.Type))
	}
	if !filter.IncludeArchived {
		// COALESCE folds NULL → '' so non-goal nodes (status NULL/'') are kept;
		// only an explicit 'archived' status is excluded.
		where = append(where, "COALESCE(n.status, '') != 'archived'")
	}
	args = append(args, filter.Limit)

	// occurred_at is inherited across a supersede, so a chain's versions share
	// it and distinct chains can tie; the ingested_at + id tiebreakers make the
	// order total and the result deterministic.
	q := `SELECT ` + nodeColumns + `
            FROM nodes n
           WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY COALESCE(n.occurred_at, n.ingested_at, n.created_at) DESC,
                   COALESCE(n.ingested_at, n.created_at) DESC, n.id ASC
           LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("repository: ListNodesAt: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanNodeRows(rows)
}

// NodeHistory returns every version in a node's supersede chain, newest first —
// the node-layer analogue of EntryHistory ("how did this work unit evolve").
func (s *Service) NodeHistory(ctx context.Context, logicalID string) ([]domain.Node, error) {
	if logicalID == "" {
		return nil, fmt.Errorf("repository: NodeHistory requires logical_id")
	}
	q := `SELECT ` + nodeColumns + `
            FROM nodes n
           WHERE n.logical_id = ?
           ORDER BY COALESCE(n.version, 1) DESC, COALESCE(n.ingested_at, n.created_at) DESC`

	rows, err := s.db.QueryContext(ctx, q, logicalID)
	if err != nil {
		return nil, fmt.Errorf("repository: NodeHistory: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanNodeRows(rows)
}
