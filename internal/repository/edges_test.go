package repository

import (
	"testing"

	"github.com/Leon180/workingbad/internal/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

// --- AttachToGoal ---

func TestAttachToGoal_Success(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)

	edge, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	if err != nil {
		t.Fatalf("AttachToGoal: %v", err)
	}
	if edge.FromID != activity.ID || edge.ToID != goal.ID || edge.Relation != domain.RelationPartOf {
		t.Errorf("edge fields wrong: %+v", edge)
	}
	if !edge.IsCurrent {
		t.Error("edge should be current")
	}
}

func TestAttachToGoal_IdempotentReturnsExisting(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)

	first, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	second, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	if err != nil {
		t.Fatalf("second AttachToGoal: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("idempotency broken: first %q != second %q", first.ID, second.ID)
	}

	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'part_of' AND is_current = 1`,
		activity.ID, goal.ID,
	).Scan(&n)
	if n != 1 {
		t.Errorf("live edge count = %d, want 1", n)
	}
}

func TestAttachToGoal_RejectsNonGoal(t *testing.T) {
	s := newService(t)
	_, activity := setupGoalAndActivity(t, s)
	// Try to attach activity to another activity — wrong type.
	other, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-other", Title: "research",
	})
	_, err := s.AttachToGoal(ctx(t), activity.ID, other.ID)
	if err == nil {
		t.Error("expected error: attaching to a non-goal entry")
	}
}

func TestAttachToGoal_RejectsSuperseded(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	// Supersede the activity, then attempt attach using the old id.
	if _, err := s.Supersede(ctx(t), activity.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h2", Title: "v2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID); err == nil {
		t.Error("expected error attaching from non-current activity")
	}
}

// --- DetachFromGoal ---

func TestDetachFromGoal_Success(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)

	if err := s.DetachFromGoal(ctx(t), edge.ID); err != nil {
		t.Fatalf("DetachFromGoal: %v", err)
	}
	var isCurrent int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, edge.ID).Scan(&isCurrent)
	if isCurrent != 0 {
		t.Errorf("edge is_current = %d, want 0", isCurrent)
	}
}

func TestDetachFromGoal_NotFound(t *testing.T) {
	s := newService(t)
	if err := s.DetachFromGoal(ctx(t), "no-such-edge"); err == nil {
		t.Error("expected error detaching non-existent edge")
	}
}

func TestDetachFromGoal_AlreadyDetached(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	_ = s.DetachFromGoal(ctx(t), edge.ID)
	if err := s.DetachFromGoal(ctx(t), edge.ID); err == nil {
		t.Error("expected error detaching already-detached edge")
	}
}

func TestDetachFromGoal_ReAttachWorks(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge1, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	_ = s.DetachFromGoal(ctx(t), edge1.ID)
	edge2, err := s.AttachToGoal(ctx(t), activity.ID, goal.ID)
	if err != nil {
		t.Fatalf("re-attach after detach: %v", err)
	}
	if edge2.ID == edge1.ID {
		t.Error("re-attach should create new edge id, not reuse old")
	}
}

// --- SetGoalStatus ---

func TestSetGoalStatus_SupersedesAndPreservesLogicalID(t *testing.T) {
	s := newService(t)
	goal, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-1",
		Title: "Ship v0.1.0", Status: domain.StatusOpen,
	})
	originalLogicalID := goal.LogicalID

	v2, err := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusInProgress)
	if err != nil {
		t.Fatalf("SetGoalStatus: %v", err)
	}
	if v2.ID == goal.ID {
		t.Error("SetGoalStatus must mint a new id")
	}
	if v2.LogicalID != originalLogicalID {
		t.Errorf("LogicalID drift: %q → %q", originalLogicalID, v2.LogicalID)
	}
	if v2.Status != domain.StatusInProgress {
		t.Errorf("v2 status = %q, want in_progress", v2.Status)
	}

	var oldCurrent int
	_ = s.db.QueryRow(`SELECT is_current FROM entries WHERE id = ?`, goal.ID).Scan(&oldCurrent)
	if oldCurrent != 0 {
		t.Errorf("old goal is_current = %d, want 0", oldCurrent)
	}
}

func TestSetGoalStatus_RePointsIncomingEdges(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)

	v2, err := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusDone)
	if err != nil {
		t.Fatalf("SetGoalStatus: %v", err)
	}

	// Old edge superseded.
	var oldCurrent int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, edge.ID).Scan(&oldCurrent)
	if oldCurrent != 0 {
		t.Errorf("old edge is_current = %d, want 0", oldCurrent)
	}

	// A new live edge exists pointing at v2.ID.
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'part_of' AND is_current = 1`,
		activity.ID, v2.ID,
	).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 live re-pointed edge to v2, got %d", n)
	}
}

func TestSetGoalStatus_InvalidStatus(t *testing.T) {
	s := newService(t)
	goal, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-1",
		Title: "Goal", Status: domain.StatusOpen,
	})
	if _, err := s.SetGoalStatus(ctx(t), goal.ID, "halfway"); err == nil {
		t.Error("expected error: invalid status")
	}
}

func TestSetGoalStatus_RejectsNonGoal(t *testing.T) {
	s := newService(t)
	_, activity := setupGoalAndActivity(t, s)
	if _, err := s.SetGoalStatus(ctx(t), activity.ID, domain.StatusDone); err == nil {
		t.Error("expected error: target is not a goal")
	}
}

func TestSetGoalStatus_RejectsSupersededTarget(t *testing.T) {
	s := newService(t)
	goal, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-1",
		Title: "Goal", Status: domain.StatusOpen,
	})
	v2, _ := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusInProgress)
	_ = v2
	// Calling SetGoalStatus on the old (now superseded) id should fail.
	if _, err := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusDone); err == nil {
		t.Error("expected error: cannot SetGoalStatus on superseded goal")
	}
}

// --- helper used by edge tests ---

func setupGoalAndActivity(t *testing.T, s *Service) (domain.Entry, domain.Entry) {
	t.Helper()
	goal, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-1",
		Title: "Test goal", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	// Materialize an activity by running the full ingest → materialize flow.
	provider := mock.New()
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-attach", "patch-attach", "sha-attach")
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	var activityID string
	if err := s.db.QueryRow(
		`SELECT id FROM entries WHERE type = 'activity' AND source_ref = 'ref-attach' AND is_current = 1`,
	).Scan(&activityID); err != nil {
		t.Fatalf("locate activity: %v", err)
	}
	var activity domain.Entry
	row := s.db.QueryRow(`SELECT `+entryColumns+` FROM entries WHERE id = ?`, activityID)
	activity, err = scanEntry(row)
	if err != nil {
		t.Fatalf("scan activity: %v", err)
	}
	return goal, activity
}
