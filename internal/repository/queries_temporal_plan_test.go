package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestQueryPlan_BitemporalReadsUseIndexes guards against silent index
// regressions on the at-time query layer (red team #6 in grill doc).
//
// The bitemporal queries are sensitive to SQLite's query planner — a
// missing index or a small change in WHERE order can swing them from
// index-driven to full-table-scan, which is a perfectly correct but
// catastrophically slow regression. EXPLAIN QUERY PLAN at CI surfaces
// the swing the moment it happens.
//
// We don't pin exact plans (they're SQLite-version-sensitive); we assert
// that for each query, at least one expected index appears in the plan
// text. Failing tests should never be "the planner output changed"
// alone — they should be a real plan regression.
func TestQueryPlan_BitemporalReadsUseIndexes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	asOf := time.Now().UTC().Format(time.RFC3339Nano)

	cases := []struct {
		name        string
		query       string
		args        []any
		mustContain []string // any one of these in plan text
	}{
		{
			name: "ListEntriesAt — supersede chain walk",
			query: `EXPLAIN QUERY PLAN
                SELECT e.id FROM entries e
                 WHERE COALESCE(e.ingested_at, e.created_at) <= ?
                   AND COALESCE(e.occurred_at, e.ingested_at, e.created_at) <= ?
                   AND e.type = 'goal'`,
			args:        []any{asOf, asOf},
			mustContain: []string{"idx_entries_type_occurred_current", "idx_entries_type_current", "idx_entries_logical_occurred"},
		},
		{
			name: "EntryHistory — chain walk by logical_id",
			query: `EXPLAIN QUERY PLAN
                SELECT id, version FROM entries
                 WHERE logical_id = ?`,
			args:        []any{"some-logical-id"},
			mustContain: []string{"idx_entries_logical_occurred", "idx_entries_logical_id"},
		},
		{
			name: "EdgesAt — from-side scan",
			query: `EXPLAIN QUERY PLAN
                SELECT id FROM edges
                 WHERE from_id = ?
                   AND COALESCE(ingested_at, created_at) <= ?
                   AND is_current = 1`,
			args:        []any{"some-id", asOf},
			mustContain: []string{"idx_edges_from_occurred_live", "idx_edges_from_live", "idx_edges_live_triple"},
		},
		{
			name: "fetched idempotency lookup — (logical_id, source_event_hash)",
			query: `EXPLAIN QUERY PLAN
                SELECT id FROM entries
                 WHERE logical_id = ? AND source_event_hash = ? AND is_current = 1`,
			args:        []any{"L", "H"},
			mustContain: []string{"idx_entries_source_event_hash", "idx_entries_logical_occurred", "idx_entries_logical_id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := queryPlan(ctx, s, tc.query, tc.args...)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			found := false
			for _, want := range tc.mustContain {
				if strings.Contains(plan, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("plan regression:\n  expected one of %v in plan\n  got: %s",
					tc.mustContain, plan)
			}
		})
	}
}

func queryPlan(ctx context.Context, s *Service, q string, args ...any) (string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	var b strings.Builder
	for rows.Next() {
		// EXPLAIN QUERY PLAN columns: id, parent, notused, detail
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "[%d:%d] %s\n", id, parent, detail)
	}
	return b.String(), rows.Err()
}
