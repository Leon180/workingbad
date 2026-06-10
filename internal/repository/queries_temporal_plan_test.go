package repository

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSlowQueries_AssertNoFullTableScan is the durable replacement for
// the prior index-name-golden test (issue #31). SQLite explicitly does
// NOT guarantee EXPLAIN QUERY PLAN output stability across versions; a
// modernc.org/sqlite or upstream-SQLite bump silently breaks any test
// that pins specific index names.
//
// What we actually need to protect against is the real perf regression:
// "a hot table fell to a full scan". The plan's "USING INDEX" /
// "USING COVERING INDEX" / "USING PRIMARY KEY" tokens reliably surface
// index-driven access; a bare "SCAN <table>" without a qualifying
// "USING" clause is the regression signal.
//
// The full access-path expectations live in docs/PERF.md so the
// reviewer can see what the test enforces without reading the code.
// When the schema or a hot query changes, update BOTH places — the
// test catches the regression, the doc explains what we expected.
func TestSlowQueries_AssertNoFullTableScan(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	asOf := time.Now().UTC().Format(time.RFC3339Nano)

	cases := []struct {
		// name reads as a docs/PERF.md anchor (see "Query Plan Reference").
		name  string
		query string
		args  []any
	}{
		{
			name: "ListEntriesAt — bitemporal goal lookup",
			query: `EXPLAIN QUERY PLAN
                SELECT e.id FROM entries e
                 WHERE COALESCE(e.ingested_at, e.created_at) <= ?
                   AND COALESCE(e.occurred_at, e.ingested_at, e.created_at) <= ?
                   AND e.type = 'goal'`,
			args: []any{asOf, asOf},
		},
		{
			name: "EntryHistory — chain walk by logical_id",
			query: `EXPLAIN QUERY PLAN
                SELECT id, version FROM entries
                 WHERE logical_id = ?`,
			args: []any{"some-logical-id"},
		},
		{
			name: "EdgesAt — from-side live edge fetch",
			query: `EXPLAIN QUERY PLAN
                SELECT id FROM edges
                 WHERE from_id = ?
                   AND COALESCE(ingested_at, created_at) <= ?
                   AND is_current = 1`,
			args: []any{"some-id", asOf},
		},
		{
			name: "EdgesAt — to-side live edge fetch",
			query: `EXPLAIN QUERY PLAN
                SELECT id FROM edges
                 WHERE to_id = ?
                   AND COALESCE(ingested_at, created_at) <= ?
                   AND is_current = 1`,
			args: []any{"some-id", asOf},
		},
		{
			name: "Idempotency lookup — (logical_id, source_event_hash)",
			query: `EXPLAIN QUERY PLAN
                SELECT id FROM entries
                 WHERE logical_id = ? AND source_event_hash = ? AND is_current = 1`,
			args: []any{"L", "H"},
		},
	}
	// EntryBySourceRef plan check is added in the PR that introduces
	// entry_source_refs (see #46) — keeping this PR strictly off main.

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := queryPlan(ctx, s, tc.query, tc.args...)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			if err := assertIndexDriven(plan); err != nil {
				t.Errorf("plan regression:\n  %v\n  full plan:\n%s", err, plan)
			}
		})
	}
}

// assertIndexDriven returns nil iff:
//   - the plan contains at least one of {USING INDEX, USING COVERING
//     INDEX, USING PRIMARY KEY, USING INTEGER PRIMARY KEY} — proves
//     the planner uses an index for at least the driving table
//   - no `SCAN entries` or `SCAN edges` row appears WITHOUT an
//     accompanying USING clause on the same row — a bare scan of the
//     two main tables is the regression signal we care about
//
// Tables NOT in the protected set (e.g. tiny lookup tables, the goose
// version table) are allowed to scan — the planner picks scan over
// index for small tables and that's correct.
func assertIndexDriven(plan string) error {
	hasUsing := strings.Contains(plan, "USING INDEX") ||
		strings.Contains(plan, "USING COVERING INDEX") ||
		strings.Contains(plan, "USING PRIMARY KEY") ||
		strings.Contains(plan, "USING INTEGER PRIMARY KEY")
	if !hasUsing {
		return fmt.Errorf("plan has no USING <index>/COVERING INDEX/PRIMARY KEY — looks like a full scan")
	}
	// Bare-scan check: each line that mentions SCAN must also include USING.
	scanRe := regexp.MustCompile(`SCAN (entries|edges)\b`)
	for _, line := range strings.Split(plan, "\n") {
		if scanRe.MatchString(line) && !strings.Contains(line, "USING") {
			return fmt.Errorf("hot table fell to bare SCAN: %s", strings.TrimSpace(line))
		}
	}
	return nil
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