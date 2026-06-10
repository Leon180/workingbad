package layout

import (
	"fmt"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Issue #28: layout hot path. Build() runs on every /graph render — if
// it gets slower we want to know before users do. Two sizes bracket the
// dogfooding range to the early-Phase-2 range.
//
// Run with:
//   go test -bench=BenchmarkBuild -benchmem -count=10 ./internal/web/layout/

func BenchmarkBuild_50nodes_2lanes(b *testing.B)    { benchBuild(b, 50, 2) }
func BenchmarkBuild_500nodes_5lanes(b *testing.B)   { benchBuild(b, 500, 5) }
func BenchmarkBuild_2000nodes_10lanes(b *testing.B) { benchBuild(b, 2000, 10) }

func benchBuild(b *testing.B, nodeCount, laneCount int) {
	entries, edges := makeBenchGraph(nodeCount, laneCount)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = Build(entries, edges)
	}
}

func makeBenchGraph(nodeCount, laneCount int) ([]domain.Entry, []domain.Edge) {
	now := time.Now().UTC()
	entries := make([]domain.Entry, 0, nodeCount+laneCount)
	edges := make([]domain.Edge, 0, nodeCount)
	// Goals.
	for g := 0; g < laneCount; g++ {
		entries = append(entries, domain.Entry{
			ID:    fmt.Sprintf("g-%d", g),
			Type:  domain.EntryTypeGoal,
			Title: fmt.Sprintf("goal %d", g),
		})
	}
	// Distribute nodes across goal lanes round-robin, alternating types
	// so colour rendering reflects the realistic mix.
	types := []domain.EntryType{
		domain.EntryTypeActivity,
		domain.EntryTypeDecision,
		domain.EntryTypeResearch,
		domain.EntryTypeDiscuss,
	}
	for i := 0; i < nodeCount; i++ {
		id := fmt.Sprintf("n-%d", i)
		goal := i % laneCount
		entries = append(entries, domain.Entry{
			ID:         id,
			Type:       types[i%len(types)],
			Title:      fmt.Sprintf("node %d", i),
			OccurredAt: now.Add(time.Duration(i) * time.Second),
		})
		edges = append(edges, domain.Edge{
			FromID:    id,
			ToID:      fmt.Sprintf("g-%d", goal),
			Relation:  domain.RelationPartOf,
			IsCurrent: true,
		})
	}
	return entries, edges
}
