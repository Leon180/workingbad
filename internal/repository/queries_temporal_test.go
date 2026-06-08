package repository

import (
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestListEntriesAt_RevealsSupersededVersion proves the core time-travel
// promise: at time T0 the live entry was v1; after supersede at T1 the
// live entry is v2; ListEntriesAt(T0) must still return v1.
func TestListEntriesAt_RevealsSupersededVersion(t *testing.T) {
	s := newService(t)

	v1, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-tt-1",
		Title: "Original finding", Body: "v1 body",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	t0 := v1.IngestedAt
	tBetween := t0.Add(50 * time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	v2, err := s.Supersede(ctx(t), v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-tt-2",
		Title: "Revised finding", Body: "v2 body",
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	tFuture := v2.IngestedAt.Add(time.Hour)

	// At T-between: should see v1 (alive, no successor yet).
	got, err := s.ListEntriesAt(ctx(t), tBetween, ListFilter{Type: domain.EntryTypeResearch})
	if err != nil {
		t.Fatalf("ListEntriesAt T-between: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEntriesAt T-between: got %d rows, want 1", len(got))
	}
	if got[0].ID != v1.ID {
		t.Errorf("ListEntriesAt T-between: returned %q, want v1 %q", got[0].ID, v1.ID)
	}
	if got[0].Title != "Original finding" {
		t.Errorf("ListEntriesAt T-between: title %q, want 'Original finding'", got[0].Title)
	}

	// At T-future: should see v2 (v1 superseded before this point).
	got, err = s.ListEntriesAt(ctx(t), tFuture, ListFilter{Type: domain.EntryTypeResearch})
	if err != nil {
		t.Fatalf("ListEntriesAt T-future: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEntriesAt T-future: got %d rows, want 1", len(got))
	}
	if got[0].ID != v2.ID {
		t.Errorf("ListEntriesAt T-future: returned %q, want v2 %q", got[0].ID, v2.ID)
	}
}

func TestEntryHistory_ReturnsFullChainNewestFirst(t *testing.T) {
	s := newService(t)

	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d-1",
		Title: "Pick SQLite", Body: "v1",
	})
	v2, _ := s.Supersede(ctx(t), v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d-2",
		Title: "Pick SQLite (revised)", Body: "v2",
	})
	v3, _ := s.Supersede(ctx(t), v2.ID, v2.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d-3",
		Title: "Pick SQLite (final)", Body: "v3",
	})

	history, err := s.EntryHistory(ctx(t), v1.LogicalID)
	if err != nil {
		t.Fatalf("EntryHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if history[0].ID != v3.ID || history[1].ID != v2.ID || history[2].ID != v1.ID {
		t.Errorf("history order wrong: %s, %s, %s (want v3, v2, v1)",
			history[0].ID, history[1].ID, history[2].ID)
	}
	if history[0].Version != 3 || history[1].Version != 2 || history[2].Version != 1 {
		t.Errorf("version chain wrong: %d, %d, %d (want 3, 2, 1)",
			history[0].Version, history[1].Version, history[2].Version)
	}
}

func TestEdgesAt_HidesEdgesFromFuture(t *testing.T) {
	s := newService(t)
	g, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-edge-1",
		Title: "Goal", Status: domain.StatusOpen,
	})
	a, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "a-edge-1",
		Title: "Activity",
	})
	beforeAttach := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	_, _ = s.AttachToGoal(ctx(t), a.ID, g.ID)
	time.Sleep(50 * time.Millisecond)
	afterAttach := time.Now().UTC()

	// At beforeAttach: no edges yet.
	got, err := s.EdgesAt(ctx(t), beforeAttach, EdgeFilter{Relation: domain.RelationPartOf})
	if err != nil {
		t.Fatalf("EdgesAt before: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("EdgesAt(beforeAttach): got %d edges, want 0", len(got))
	}

	// At afterAttach: edge exists.
	got, err = s.EdgesAt(ctx(t), afterAttach, EdgeFilter{Relation: domain.RelationPartOf})
	if err != nil {
		t.Fatalf("EdgesAt after: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("EdgesAt(afterAttach): got %d edges, want 1", len(got))
	}
}

func TestListEntriesAt_RequiresNonZeroAsOf(t *testing.T) {
	s := newService(t)
	if _, err := s.ListEntriesAt(ctx(t), time.Time{}, ListFilter{}); err == nil {
		t.Error("expected error on zero asOf")
	}
}
