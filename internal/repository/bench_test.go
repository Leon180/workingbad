package repository

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Issue #28: hot-path benchmarks for the repository layer. Run with:
//
//   go test -bench=. -benchmem -count=10 ./internal/repository/ > /tmp/new.txt
//   benchstat /tmp/main.txt /tmp/new.txt
//
// Targets the four operations that dominate wall time in real dogfood
// workflows (per the load-test scenarios in issue #30): InsertEntry,
// Supersede chain build-up, ListEntries (live query), ListEntriesAt
// (bitemporal time-travel query).
//
// See docs/PERF.md for the baseline → compare workflow.

func openBenchDB(b *testing.B) *sql.DB {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.db")
	db, err := Open(path)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}

func BenchmarkInsertEntry(b *testing.B) {
	s := NewService(openBenchDB(b))
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, err := s.InsertEntry(ctx, domain.Entry{
			Type:      domain.EntryTypeResearch,
			Origin:    domain.OriginLocal,
			Source:    domain.SourceManual,
			SourceRef: fmt.Sprintf("bench-%d", i),
			Title:     "bench note",
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSupersede(b *testing.B) {
	s := NewService(openBenchDB(b))
	ctx := context.Background()
	// Seed one entry per future supersede; the b.Loop body otherwise has
	// to pay for the seed insert each iteration, which would dominate the
	// measurement and hide the actual Supersede cost.
	v1, err := s.InsertEntry(ctx, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "bench-supersede",
		Title: "v1",
	})
	if err != nil {
		b.Fatal(err)
	}
	current := v1
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		v, err := s.Supersede(ctx, current.ID, current.Version, domain.Entry{
			Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
			Source: domain.SourceManual, SourceRef: fmt.Sprintf("v%d", i+2),
			Title: fmt.Sprintf("v%d", i+2),
		})
		if err != nil {
			b.Fatal(err)
		}
		current = v
	}
}

// BenchmarkListEntries_100 measures the live-list query at the
// dogfooding-typical row count. The actual query is the COALESCE-heavy
// hand-written SQL in queries.go.
func BenchmarkListEntries_100(b *testing.B)  { benchListEntries(b, 100) }
func BenchmarkListEntries_1000(b *testing.B) { benchListEntries(b, 1000) }

func benchListEntries(b *testing.B, n int) {
	s := NewService(openBenchDB(b))
	ctx := context.Background()
	seedEntries(b, s, ctx, n)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, err := s.ListEntries(ctx, ListFilter{Limit: n})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListEntriesAt isolates the bitemporal supersede-chain walk
// (the EXISTS subquery in queries_temporal.go). Even when only one
// version per logical_id exists, this should not be dramatically slower
// than ListEntries — if it ever is, an index regressed.
func BenchmarkListEntriesAt_100(b *testing.B)  { benchListEntriesAt(b, 100) }
func BenchmarkListEntriesAt_1000(b *testing.B) { benchListEntriesAt(b, 1000) }

func benchListEntriesAt(b *testing.B, n int) {
	s := NewService(openBenchDB(b))
	ctx := context.Background()
	seedEntries(b, s, ctx, n)
	asOf := time.Now().UTC().Add(time.Hour)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, err := s.ListEntriesAt(ctx, asOf, ListFilter{Limit: n})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func seedEntries(b *testing.B, s *Service, ctx context.Context, n int) {
	b.Helper()
	for i := 0; i < n; i++ {
		typ := domain.EntryTypeResearch
		if i%5 == 0 {
			typ = domain.EntryTypeGoal
		}
		entry := domain.Entry{
			Type: typ, Origin: domain.OriginLocal,
			Source:    domain.SourceManual,
			SourceRef: fmt.Sprintf("seed-%d", i),
			Title:     fmt.Sprintf("seed entry %d", i),
		}
		if typ == domain.EntryTypeGoal {
			entry.Status = domain.StatusOpen
		}
		if _, err := s.InsertEntry(ctx, entry); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
}
