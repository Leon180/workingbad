package repository

import (
	"testing"

	"github.com/Leon180/workingbad/internal/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

// --- ListEntries ---

func TestListEntries_OnlyCurrent(t *testing.T) {
	s := newService(t)
	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h1", Title: "first",
	})
	if _, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h2", Title: "second",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListEntries(ctx(t), ListFilter{})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only current)", len(got))
	}
	if got[0].Title != "second" {
		t.Errorf("title = %q, want second", got[0].Title)
	}
}

func TestListEntries_FilterByType(t *testing.T) {
	s := newService(t)
	for i, typ := range []struct {
		typ   domain.EntryType
		title string
	}{
		{domain.EntryTypeResearch, "r1"},
		{domain.EntryTypeDecision, "d1"},
		{domain.EntryTypeResearch, "r2"},
	} {
		sourceRef := []string{"h1", "h2", "h3"}[i]
		if _, err := s.InsertEntry(ctx(t), domain.Entry{
			Type: typ.typ, Origin: domain.OriginLocal,
			Source: domain.SourceManual, SourceRef: sourceRef,
			Title: typ.title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListEntries(ctx(t), ListFilter{Type: domain.EntryTypeResearch})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("research count = %d, want 2", len(got))
	}
}

func TestListEntries_FilterByRepo(t *testing.T) {
	s := newService(t)
	provider := mock.New()
	_ = setupSegmentWithChanges(t, s, "repo-A", "ref-A", "patch-A", "sha-A")
	_ = setupSegmentWithChanges(t, s, "repo-B", "ref-B", "patch-B", "sha-B")
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListEntries(ctx(t), ListFilter{RepoID: "repo-A"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RepoID != "repo-A" {
		t.Errorf("got %d entries, want exactly 1 from repo-A: %+v", len(got), got)
	}
}

func TestListEntries_Limit(t *testing.T) {
	s := newService(t)
	for i, ref := range []string{"h1", "h2", "h3", "h4", "h5"} {
		if _, err := s.InsertEntry(ctx(t), domain.Entry{
			Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
			Source: domain.SourceManual, SourceRef: ref,
			Title: "item",
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	got, err := s.ListEntries(ctx(t), ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("limited len = %d, want 2", len(got))
	}
}

// --- CountPendingSegments ---

func TestCountPendingSegments_PendingAndStaleOnly(t *testing.T) {
	s := newService(t)
	provider := mock.New()
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-1", "patch-1", "sha-1")
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-2", "patch-2", "sha-2")
	// One materialised, one still pending after we set its state stale.
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE segments SET summary_state = 'stale' WHERE source_ref = 'ref-1'`); err != nil {
		t.Fatal(err)
	}
	// Insert another fresh pending segment.
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-3", "patch-3", "sha-3")

	n, err := s.CountPendingSegments(ctx(t), MaterializeScope{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pending count = %d, want 2 (1 stale + 1 fresh pending)", n)
	}
}

func TestCountPendingSegments_RepoScope(t *testing.T) {
	s := newService(t)
	_ = setupSegmentWithChanges(t, s, "repo-A", "ref-A", "patch-A", "sha-A")
	_ = setupSegmentWithChanges(t, s, "repo-B", "ref-B", "patch-B", "sha-B")
	n, err := s.CountPendingSegments(ctx(t), MaterializeScope{RepoID: "repo-A"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("repo-A pending = %d, want 1", n)
	}
}

// --- GetGoalActivities ---

func TestGetGoalActivities_FindsAttachedActivity(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	if _, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetGoalActivities(ctx(t), goal.ID)
	if err != nil {
		t.Fatalf("GetGoalActivities: %v", err)
	}
	if len(got) != 1 || got[0].ID != activity.ID {
		t.Errorf("got %+v, want activity %q", got, activity.ID)
	}
}

func TestGetGoalActivities_SurvivesGoalSupersede(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	if _, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID); err != nil {
		t.Fatal(err)
	}

	// Status change supersedes goal — edges should re-point and the query
	// should still find the activity using either the old id or the new id.
	v2, err := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusDone)
	if err != nil {
		t.Fatal(err)
	}

	// Query via new id.
	got, err := s.GetGoalActivities(ctx(t), v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("via v2.ID len = %d, want 1", len(got))
	}

	// Query via old id (same LogicalID chain).
	got, err = s.GetGoalActivities(ctx(t), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("via goal.ID len = %d, want 1 (logical_id walks both versions)", len(got))
	}
}

func TestGetGoalActivities_NoAttachments(t *testing.T) {
	s := newService(t)
	goal, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-empty",
		Title: "Empty goal", Status: domain.StatusOpen,
	})
	got, err := s.GetGoalActivities(ctx(t), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty goal activities = %d, want 0", len(got))
	}
}

func TestGetGoalActivities_IgnoresDetached(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	if err := s.DetachFromGoal(ctx(t), edge.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGoalActivities(ctx(t), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("after detach: len = %d, want 0", len(got))
	}
}
