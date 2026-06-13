package repository

import (
	"testing"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
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
	if _, err := s.Supersede(ctx(t), activity.ID, 0, domain.Entry{
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

// --- migration 0017: entry-id → node-id remap + dedup ---

// remapEdgesToNode re-runs migration 0017's destructive steps (drop index →
// remap from_id/to_id via entries.logical_id → dedup colliding live triples →
// rebuild index) so the remap can be asserted on seeded data; the migration
// itself runs once on an empty test DB. Kept in lockstep with
// 0017_edges_rekey_to_node.sql.
func remapEdgesToNode(t *testing.T, s *Service) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_edges_live_triple`,
		`UPDATE edges SET from_id = (SELECT e.logical_id FROM entries e WHERE e.id = edges.from_id)
		   WHERE EXISTS (SELECT 1 FROM entries e WHERE e.id = edges.from_id)`,
		`UPDATE edges SET to_id = (SELECT e.logical_id FROM entries e WHERE e.id = edges.to_id)
		   WHERE EXISTS (SELECT 1 FROM entries e WHERE e.id = edges.to_id)`,
		`UPDATE edges SET is_current = 0
		   WHERE is_current = 1 AND rowid NOT IN (
		     SELECT MAX(rowid) FROM edges WHERE is_current = 1 GROUP BY from_id, to_id, relation)`,
		`CREATE UNIQUE INDEX idx_edges_live_triple ON edges(from_id, to_id, relation) WHERE is_current = 1`,
	} {
		if _, err := s.db.ExecContext(ctx(t), q); err != nil {
			t.Fatalf("remap step %q: %v", q, err)
		}
	}
}

func TestMigration0017_RemapAndDedup(t *testing.T) {
	s := newService(t)
	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeActivity, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "rk-1", Title: "v1",
	})
	v2, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
		Type: domain.EntryTypeActivity, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "rk-2", Title: "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "rk-g", Title: "g", Status: domain.StatusOpen,
	})

	// Two pre-migration (entry-id keyed) live edges. v1.id and v2.id are
	// distinct (so the OLD unique index permits both), but they share
	// logical_id == v1.id, so the remap collapses them onto one node triple.
	for _, ed := range []struct{ id, from string }{{"edge-a", v1.ID}, {"edge-b", v2.ID}} {
		if _, err := s.db.Exec(
			`INSERT INTO edges (id, from_id, to_id, relation, is_current, metadata,
			                    created_at, occurred_at, ingested_at)
			 VALUES (?, ?, ?, 'part_of', 1, '{}',
			         strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			         strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			         strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
			ed.id, ed.from, g.ID,
		); err != nil {
			t.Fatal(err)
		}
	}

	remapEdgesToNode(t, s)

	// Collapsed to exactly one live edge on the node triple (v1.logical → g.logical).
	var live int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation='part_of' AND is_current=1`,
		v1.LogicalID, g.LogicalID,
	).Scan(&live)
	if live != 1 {
		t.Errorf("after remap+dedup: %d live edges, want 1", live)
	}
	// No edge still references a per-version entry id.
	var stale int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM edges WHERE from_id = ? OR to_id = ?`, v2.ID, v2.ID).Scan(&stale)
	if stale != 0 {
		t.Errorf("%d edges still reference per-version id %s, want 0", stale, v2.ID)
	}
	// Both rows preserved (1 live + 1 retired) — dedup retires, never deletes.
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM edges`).Scan(&total)
	if total != 2 {
		t.Errorf("edge rows = %d, want 2 (1 live + 1 retired)", total)
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

// SetGoalStatus supersedes the goal, but edges key on the goal's logical_id
// (stable, decision (a)), so the attachment survives with NO edge rewriting:
// the original edge stays live and the activity is still listed under the new
// goal version. (Was TestSetGoalStatus_RePointsIncomingEdges — re-point is gone.)
func TestSetGoalStatus_KeepsAttachmentViaLogicalID(t *testing.T) {
	s := newService(t)
	goal, activity := setupGoalAndActivity(t, s)
	edge, _ := s.AttachToGoal(ctx(t), activity.ID, goal.ID)

	v2, err := s.SetGoalStatus(ctx(t), goal.ID, domain.StatusDone)
	if err != nil {
		t.Fatalf("SetGoalStatus: %v", err)
	}

	// The original edge is untouched: still live, not re-pointed.
	var current int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, edge.ID).Scan(&current)
	if current != 1 {
		t.Errorf("edge should stay live (no re-point), is_current = %d", current)
	}
	// It keys on the goal's logical_id (== v2.LogicalID), not a version id.
	if edge.ToID != v2.LogicalID {
		t.Errorf("edge.to_id = %q, want goal logical_id %q", edge.ToID, v2.LogicalID)
	}
	// The activity is still attached under the new goal version.
	acts, err := s.GetGoalActivities(ctx(t), v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].LogicalID != activity.LogicalID {
		t.Errorf("GetGoalActivities(v2) = %v, want [activity %s]", acts, activity.LogicalID)
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
	row, err := s.q.GetEntryByID(ctx(t), activityID)
	if err != nil {
		t.Fatalf("load activity: %v", err)
	}
	activity, err := entryFromSqlc(row)
	if err != nil {
		t.Fatalf("convert activity: %v", err)
	}
	return goal, activity
}
