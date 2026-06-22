package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

// funcEmbedder is a controlled ai.Embedder for tests: it maps each text to a
// caller-chosen vector so clustering outcomes are deterministic (the hash-based
// mock embedder would give arbitrary, occasionally-colliding vectors).
type funcEmbedder func(texts []string) [][]float32

func (f funcEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return f(texts), nil
}
func (f funcEmbedder) Dim() int { return 2 }

// byKeyword returns [1,0] for texts containing any hot keyword (so they cluster)
// and [0,1] otherwise (orthogonal → singletons).
func byKeyword(hot ...string) funcEmbedder {
	return func(texts []string) [][]float32 {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			out[i] = []float32{0, 1}
			for _, h := range hot {
				if strings.Contains(t, h) {
					out[i] = []float32{1, 0}
					break
				}
			}
		}
		return out
	}
}

func TestAggregate_SimilarEntriesMergeIntoOneNode(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	a := seedActivity(t, s, "alpha work")
	b := seedActivity(t, s, "beta work")
	g := seedActivity(t, s, "gamma work")

	// alpha+beta share a direction → one cluster; gamma is orthogonal → singleton.
	res, err := s.AggregateEntries(c, []domain.Entry{a, b, g}, byKeyword("alpha", "beta"), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.ClustersFormed != 2 || res.NodesCreated != 2 || res.EntriesAggregated != 3 {
		t.Fatalf("res = %+v, want clusters2/nodes2/entries3", res)
	}
	// alpha and beta map to the SAME node; gamma to its own.
	na, err := s.NodesForEntry(c, a.ID)
	if err != nil {
		t.Fatalf("NodesForEntry(a): %v", err)
	}
	nb, err := s.NodesForEntry(c, b.ID)
	if err != nil {
		t.Fatalf("NodesForEntry(b): %v", err)
	}
	ng, err := s.NodesForEntry(c, g.ID)
	if err != nil {
		t.Fatalf("NodesForEntry(g): %v", err)
	}
	if len(na) != 1 || len(nb) != 1 || na[0].ID != nb[0].ID {
		t.Fatalf("alpha/beta should share one node: %v vs %v", na, nb)
	}
	if len(ng) != 1 || ng[0].ID == na[0].ID {
		t.Fatalf("gamma should have its own node, got %v", ng)
	}
	// The merged node carries both entries.
	ents, err := s.EntriesForNode(c, na[0].ID)
	if err != nil {
		t.Fatalf("EntriesForNode: %v", err)
	}
	if len(ents) != 2 {
		t.Fatalf("merged node has %d entries, want 2", len(ents))
	}
}

func TestAggregate_AllDistinct_OneNodeEach(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e1 := seedActivity(t, s, "one")
	e2 := seedActivity(t, s, "two")
	// Distinct orthogonal vectors → no merge → one node each.
	distinct := funcEmbedder(func(texts []string) [][]float32 {
		out := make([][]float32, len(texts))
		for i, txt := range texts {
			if strings.Contains(txt, "one") {
				out[i] = []float32{1, 0}
			} else {
				out[i] = []float32{0, 1}
			}
		}
		return out
	})
	res, err := s.AggregateEntries(c, []domain.Entry{e1, e2}, distinct, mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.NodesCreated != 2 || res.EntriesAggregated != 2 || res.ClustersFormed != 2 {
		t.Fatalf("res = %+v, want 2/2/2 singletons", res)
	}
}

func TestAggregate_SummarizeFailure_QueuesClusterMembers(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	a := seedActivity(t, s, "alpha one")
	b := seedActivity(t, s, "alpha two")
	provider := mock.New().WithSummarizeFunc(func(_ context.Context, _ []domain.Summarizable) (string, string, error) {
		return "", "", errors.New("summarize boom")
	})

	res, err := s.AggregateEntries(c, []domain.Entry{a, b}, byKeyword("alpha"), provider)
	if err != nil {
		t.Fatalf("AggregateEntries should not abort on summarize failure: %v", err)
	}
	if res.NodesCreated != 0 || len(res.Queued) != 2 {
		t.Fatalf("res = %+v, want 0 nodes + 2 queued", res)
	}
	for _, q := range res.Queued {
		if q.Step != "aggregate" {
			t.Fatalf("queued step = %q, want aggregate", q.Step)
		}
	}
	nodes, nerr := s.NodesForEntry(c, a.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 0 {
		t.Fatalf("failed cluster left %d nodes, want 0", len(nodes))
	}
}

func TestAggregate_LazySkipsAlreadyMapped(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "solo")
	// First pass creates a node.
	if _, err := s.AggregateEntries(c, []domain.Entry{e}, byKeyword(), mock.New()); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	// Second pass skips it (no new nodes).
	res, err := s.AggregateEntries(c, []domain.Entry{e}, byKeyword(), mock.New())
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if res.Skipped != 1 || res.NodesCreated != 0 || res.ClustersFormed != 0 {
		t.Fatalf("res = %+v, want skipped1/nodes0/clusters0", res)
	}
}

func seedGoal(t *testing.T, s *Service, title string) domain.Entry {
	t.Helper()
	e, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeGoal,
		Status: domain.StatusOpen,
		Origin: domain.OriginLocal,
		Source: domain.SourceGit,
		Title:  title,
	})
	if err != nil {
		t.Fatalf("seedGoal: %v", err)
	}
	return e
}

func TestAggregate_GoalCluster_MergedNodeCarriesStatus(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	g1 := seedGoal(t, s, "alpha goal")
	g2 := seedGoal(t, s, "alpha goal too")
	// Both share a direction → one goal cluster → one merged goal node. Without
	// carrying Status, validateNode would reject the goal node and queue it.
	res, err := s.AggregateEntries(c, []domain.Entry{g1, g2}, byKeyword("alpha"), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.NodesCreated != 1 || res.EntriesAggregated != 2 || len(res.Queued) != 0 {
		t.Fatalf("res = %+v, want 1 node / 2 entries / 0 queued", res)
	}
	nodes, err := s.NodesForEntry(c, g1.ID)
	if err != nil {
		t.Fatalf("NodesForEntry: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Type != domain.EntryTypeGoal || nodes[0].Status != domain.StatusOpen {
		t.Fatalf("merged node = %+v, want goal/open", nodes)
	}
}

func TestAggregate_MixedTypeCluster_Queued(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	act := seedActivity(t, s, "alpha activity")
	goal := seedGoal(t, s, "alpha goal")
	// Forced into one cluster (shared keyword) but different types → refused.
	res, err := s.AggregateEntries(c, []domain.Entry{act, goal}, byKeyword("alpha"), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.NodesCreated != 0 || len(res.Queued) != 2 {
		t.Fatalf("res = %+v, want 0 nodes + 2 queued (mixed types)", res)
	}
	if nodes, nerr := s.NodesForEntry(c, act.ID); nerr != nil || len(nodes) != 0 {
		t.Fatalf("mixed cluster left nodes (%v) or err %v", nodes, nerr)
	}
}

func TestAggregate_BlankText_Queued(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	// Title passes validateEntry (non-empty) but is whitespace-only, so
	// aggregateText trims to "" → not embeddable → queued, not a zero-vector.
	blank, err := s.InsertEntry(c, domain.Entry{
		Type: domain.EntryTypeActivity, Origin: domain.OriginLocal,
		Source: domain.SourceGit, Title: "   ",
	})
	if err != nil {
		t.Fatalf("InsertEntry(blank): %v", err)
	}
	res, err := s.AggregateEntries(c, []domain.Entry{blank}, byKeyword(), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.NodesCreated != 0 || len(res.Queued) != 1 || res.Queued[0].Step != "aggregate" {
		t.Fatalf("res = %+v, want 0 nodes + 1 queued(aggregate)", res)
	}
}

func TestAggregate_MixedSkipAndPending(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	done := seedActivity(t, s, "already done")
	fresh := seedActivity(t, s, "to process")
	// Pre-map `done` so it's skipped; `fresh` is processed.
	if _, err := s.AggregateEntries(c, []domain.Entry{done}, byKeyword(), mock.New()); err != nil {
		t.Fatalf("pre-map: %v", err)
	}
	res, err := s.AggregateEntries(c, []domain.Entry{done, fresh}, byKeyword(), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries: %v", err)
	}
	if res.Skipped != 1 || res.EntriesAggregated != 1 || res.NodesCreated != 1 {
		t.Fatalf("res = %+v, want skipped1/aggregated1/nodes1", res)
	}
}

func TestAggregate_EmptyInput(t *testing.T) {
	s := newService(t)
	res, err := s.AggregateEntries(ctx(t), nil, byKeyword(), mock.New())
	if err != nil {
		t.Fatalf("AggregateEntries(nil): %v", err)
	}
	if res.NodesCreated != 0 || res.ClustersFormed != 0 || len(res.Queued) != 0 {
		t.Fatalf("non-zero result on empty input: %+v", res)
	}
}
