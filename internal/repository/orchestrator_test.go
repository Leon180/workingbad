package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

func TestOrchestrate_HappyPath_IdentitySplit(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e1 := seedActivity(t, s, "first entry")
	e2 := seedActivity(t, s, "second entry")

	res, err := s.Orchestrate(c, []domain.Entry{e1, e2}, mock.New())
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if res.EntriesProcessed != 2 || res.NodesCreated != 2 {
		t.Fatalf("processed=%d nodes=%d, want 2/2", res.EntriesProcessed, res.NodesCreated)
	}
	if len(res.Queued) != 0 {
		t.Fatalf("Queued = %v, want none", res.Queued)
	}
	// Each entry now maps to exactly one node carrying its title.
	for _, e := range []domain.Entry{e1, e2} {
		nodes, nerr := s.NodesForEntry(c, e.ID)
		if nerr != nil {
			t.Fatalf("NodesForEntry(%s): %v", e.ID, nerr)
		}
		if len(nodes) != 1 || nodes[0].Title != e.Title {
			t.Fatalf("entry %s → %v, want one node titled %q", e.ID, nodes, e.Title)
		}
	}
}

func TestOrchestrate_MultiNodeSplit(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "splits into two")
	provider := mock.New().WithSplitFunc(func(_ context.Context, in domain.Entry) ([]domain.NodeDraft, error) {
		return []domain.NodeDraft{
			{Type: domain.EntryTypeActivity, Title: in.Title + " A"},
			{Type: domain.EntryTypeActivity, Title: in.Title + " B"},
		}, nil
	})

	res, err := s.Orchestrate(c, []domain.Entry{e}, provider)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if res.NodesCreated != 2 || res.EntriesProcessed != 1 {
		t.Fatalf("nodes=%d processed=%d, want 2/1", res.NodesCreated, res.EntriesProcessed)
	}
	nodes, nerr := s.NodesForEntry(c, e.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 2 {
		t.Fatalf("NodesForEntry = %d nodes, want 2", len(nodes))
	}
}

func TestOrchestrate_SplitFailure_QueuesNoPartial(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	good := seedActivity(t, s, "good")
	bad := seedActivity(t, s, "bad")
	provider := mock.New().WithSplitFunc(func(_ context.Context, in domain.Entry) ([]domain.NodeDraft, error) {
		if in.ID == bad.ID {
			return nil, errors.New("split boom")
		}
		return []domain.NodeDraft{{Type: in.Type, Title: in.Title}}, nil
	})

	res, err := s.Orchestrate(c, []domain.Entry{good, bad}, provider)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if res.EntriesProcessed != 1 || res.NodesCreated != 1 {
		t.Fatalf("processed=%d nodes=%d, want 1/1", res.EntriesProcessed, res.NodesCreated)
	}
	if len(res.Queued) != 1 || res.Queued[0].EntryID != bad.ID || res.Queued[0].Step != "split" {
		t.Fatalf("Queued = %v, want one {bad, split}", res.Queued)
	}
	// The failed entry left no node (no partial graph).
	nodes, nerr := s.NodesForEntry(c, bad.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 0 {
		t.Fatalf("bad entry created %d nodes, want 0", len(nodes))
	}
}

func TestOrchestrate_RelateFailure_PersistsNodesButQueues(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "relate fails")
	provider := mock.New().WithRelateFunc(func(_ context.Context, _ domain.Entry, _ []domain.Entry) ([]domain.Edge, error) {
		return nil, errors.New("relate boom")
	})

	res, err := s.Orchestrate(c, []domain.Entry{e}, provider)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	// Split + persist succeeded, so the node stands; relate is queued for retry.
	if res.EntriesProcessed != 1 || res.NodesCreated != 1 {
		t.Fatalf("processed=%d nodes=%d, want 1/1", res.EntriesProcessed, res.NodesCreated)
	}
	if len(res.Queued) != 1 || res.Queued[0].Step != "relate" {
		t.Fatalf("Queued = %v, want one {relate}", res.Queued)
	}
	nodes, nerr := s.NodesForEntry(c, e.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 1 {
		t.Fatalf("relate-failed entry has %d nodes, want 1 (node persisted)", len(nodes))
	}
}

func TestOrchestrate_InvalidDraft_QueuesAtAggregate(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "yields invalid draft")
	// A draft with an empty title fails validateNode (ErrInvalidInput) inside
	// persistEntryNodes → queued at the aggregate step, run not aborted.
	provider := mock.New().WithSplitFunc(func(_ context.Context, in domain.Entry) ([]domain.NodeDraft, error) {
		return []domain.NodeDraft{{Type: in.Type, Title: ""}}, nil
	})

	res, err := s.Orchestrate(c, []domain.Entry{e}, provider)
	if err != nil {
		t.Fatalf("Orchestrate should not abort on an invalid draft: %v", err)
	}
	if res.EntriesProcessed != 0 || res.NodesCreated != 0 {
		t.Fatalf("processed=%d nodes=%d, want 0/0", res.EntriesProcessed, res.NodesCreated)
	}
	if len(res.Queued) != 1 || res.Queued[0].Step != "aggregate" || res.Queued[0].EntryID != e.ID {
		t.Fatalf("Queued = %v, want one {%s, aggregate}", res.Queued, e.ID)
	}
	nodes, nerr := s.NodesForEntry(c, e.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 0 {
		t.Fatalf("invalid-draft entry created %d nodes, want 0", len(nodes))
	}
}

func TestOrchestrate_LazyIdempotent_SkipsAlreadySplit(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "split once")
	provider := mock.New()

	// First run splits + persists.
	res1, err := s.Orchestrate(c, []domain.Entry{e}, provider)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.EntriesProcessed != 1 || res1.NodesCreated != 1 || res1.Skipped != 0 {
		t.Fatalf("run 1 = %+v, want processed1/nodes1/skipped0", res1)
	}
	_, _, _, splitAfter1 := provider.Counts()

	// Second run skips the already-mapped entry: no new nodes, no extra split call.
	res2, err := s.Orchestrate(c, []domain.Entry{e}, provider)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Skipped != 1 || res2.EntriesProcessed != 0 || res2.NodesCreated != 0 {
		t.Fatalf("run 2 = %+v, want skipped1/processed0/nodes0", res2)
	}
	if _, _, _, splitAfter2 := provider.Counts(); splitAfter2 != splitAfter1 {
		t.Fatalf("split called on the skip path: %d → %d (lazy guard should gate it)", splitAfter1, splitAfter2)
	}
	// Still exactly one node — no duplicate.
	nodes, nerr := s.NodesForEntry(c, e.ID)
	if nerr != nil {
		t.Fatalf("NodesForEntry: %v", nerr)
	}
	if len(nodes) != 1 {
		t.Fatalf("entry has %d nodes after re-run, want 1 (no duplicate)", len(nodes))
	}
}

func TestOrchestrate_EmptyInput(t *testing.T) {
	s := newService(t)
	res, err := s.Orchestrate(ctx(t), nil, mock.New())
	if err != nil {
		t.Fatalf("Orchestrate(nil): %v", err)
	}
	if res.EntriesProcessed != 0 || res.NodesCreated != 0 || len(res.Queued) != 0 {
		t.Fatalf("non-zero result on empty input: %+v", res)
	}
}
