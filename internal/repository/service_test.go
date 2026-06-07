package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

func newService(t *testing.T) *Service {
	t.Helper()
	return NewService(openTempDB(t))
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// --- InsertEntry — per-type and per-source validation ---

func TestInsertEntry_ActivitySuccess(t *testing.T) {
	s := newService(t)

	got, err := s.InsertEntry(ctx(t), domain.Entry{
		Type:   domain.EntryTypeActivity,
		Origin: domain.OriginLocal,
		Source: domain.SourceGit,
		Title:  "Refactor auth path",
		Body:   "Extracted token validator into its own package.",
		RepoID: "repo-1",
	})
	if err != nil {
		t.Fatalf("InsertEntry: %v", err)
	}
	if got.ID == "" {
		t.Error("ID not assigned")
	}
	if got.LogicalID != got.ID {
		t.Errorf("fresh insert: LogicalID = %q, want %q", got.LogicalID, got.ID)
	}
	if !got.IsCurrent {
		t.Error("IsCurrent should be true after InsertEntry")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps should be set")
	}
	assertFTSHit(t, s.db, "refactor")
}

func TestInsertEntry_GoalRequiresStatus(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "hash-1",
		Title: "Ship v0.1.0",
	})
	if err == nil {
		t.Error("expected error: goal without status")
	}
}

func TestInsertEntry_GoalInvalidStatus(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "hash-1",
		Title: "Goal", Status: "halfway",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid goal status") {
		t.Errorf("expected invalid status error, got %v", err)
	}
}

func TestInsertEntry_GoalValidStatusAccepted(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "hash-1",
		Title: "Ship v0.1.0", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("InsertEntry goal: %v", err)
	}
}

func TestInsertEntry_ActivityRejectsStatus(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeActivity, Origin: domain.OriginLocal,
		Source: domain.SourceGit, Title: "x", Status: domain.StatusOpen,
	})
	if err == nil || !strings.Contains(err.Error(), "must have empty status") {
		t.Errorf("expected status-must-be-empty error, got %v", err)
	}
}

func TestInsertEntry_ManualNeedsSourceRef(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, Title: "Note",
	})
	if err == nil || !strings.Contains(err.Error(), "source_ref") {
		t.Errorf("expected source_ref error, got %v", err)
	}
}

func TestInsertEntry_InvalidOrigin(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeActivity, Origin: "bogus",
		Source: domain.SourceGit, Title: "x",
	})
	if err == nil {
		t.Error("expected invalid origin error")
	}
}

func TestInsertEntry_NoType(t *testing.T) {
	s := newService(t)
	_, err := s.InsertEntry(ctx(t), domain.Entry{
		Origin: domain.OriginLocal, Source: domain.SourceGit, Title: "x",
	})
	if err == nil {
		t.Error("expected error: missing type")
	}
}

// --- Supersede — append-only, identity preservation, FTS reflects latest ---

func TestSupersede_ShareLogicalIDFlipIsCurrent(t *testing.T) {
	s := newService(t)

	v1, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "hash-1",
		Title: "Original investigation", Body: "v1 body",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	v2, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "hash-2",
		Title: "Revised investigation", Body: "v2 body",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	if v1.ID == v2.ID {
		t.Error("v1 and v2 must have distinct IDs")
	}
	if v2.LogicalID != v1.LogicalID {
		t.Errorf("LogicalID drift: v1=%s, v2=%s", v1.LogicalID, v2.LogicalID)
	}

	var (
		v1Current int
		v1SupBy   sql.NullString
	)
	if err := s.db.QueryRow(
		`SELECT is_current, superseded_by FROM entries WHERE id = ?`, v1.ID,
	).Scan(&v1Current, &v1SupBy); err != nil {
		t.Fatal(err)
	}
	if v1Current != 0 {
		t.Errorf("v1 is_current = %d, want 0", v1Current)
	}
	if !v1SupBy.Valid || v1SupBy.String != v2.ID {
		t.Errorf("v1.superseded_by = %v, want %s", v1SupBy, v2.ID)
	}

	var v2Current int
	if err := s.db.QueryRow(
		`SELECT is_current FROM entries WHERE id = ?`, v2.ID,
	).Scan(&v2Current); err != nil {
		t.Fatal(err)
	}
	if v2Current != 1 {
		t.Errorf("v2 is_current = %d, want 1", v2Current)
	}
}

func TestSupersede_FTSMirrorsLatest(t *testing.T) {
	s := newService(t)
	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h1",
		Title: "Antiquated finding", Body: "...",
	})
	if _, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h2",
		Title: "Refreshed conclusion", Body: "...",
	}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	if !ftsHit(t, s.db, "refreshed") {
		t.Error("FTS5 should find the new title 'refreshed'")
	}
	if ftsHit(t, s.db, "antiquated") {
		t.Error("FTS5 must not find the old title 'antiquated' after supersede")
	}
}

func TestSupersede_OldMissing(t *testing.T) {
	s := newService(t)
	_, err := s.Supersede(ctx(t), "00000000-0000-7000-8000-000000000000", domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h", Title: "x",
	})
	if err == nil {
		t.Error("expected error superseding missing entry")
	}
}

// TestSupersede_RePointsOutgoingEdges proves that service.Supersede now
// inherits the round-6 outgoing re-point. A live edge from V1 to G is
// rewritten to be from V2 to G after Supersede(V1, V2).
func TestSupersede_RePointsOutgoingEdges(t *testing.T) {
	s := newService(t)

	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-out-1",
		Title: "v1",
	})
	g, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "g-out",
		Title: "Goal", Status: domain.StatusOpen,
	})
	edge, err := s.AttachToGoal(ctx(t), v1.ID, g.ID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	v2, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-out-2",
		Title: "v2",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// Old edge superseded.
	var oldEdgeCurrent int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, edge.ID).Scan(&oldEdgeCurrent)
	if oldEdgeCurrent != 0 {
		t.Errorf("old edge is_current = %d, want 0", oldEdgeCurrent)
	}

	// A new live edge points from v2 to g.
	var n int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'part_of' AND is_current = 1`,
		v2.ID, g.ID,
	).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 live re-pointed outgoing edge from v2 to g, got %d", n)
	}
}

// TestSupersede_RePointsBothDirections covers a single entry with both
// incoming and outgoing live edges; supersede must rewrite both.
func TestSupersede_RePointsBothDirections(t *testing.T) {
	s := newService(t)

	// Three entries: source → middle → sink (middle has incoming from source,
	// outgoing to sink). Then supersede middle and verify both edges follow.
	source, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "src",
		Title: "source",
	})
	sink, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "sink-goal",
		Title: "Sink goal", Status: domain.StatusOpen,
	})
	middle, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "mid",
		Title: "middle",
	})

	// source → middle (where middle is a goal target — actually middle is
	// research, so we attach to sink goal). AttachToGoal requires to_id to
	// be a goal. So: source attaches to sink-goal, middle attaches to
	// sink-goal, middle is then superseded — outgoing edge from middle to
	// sink-goal should re-point to middle-v2.
	mEdge, _ := s.AttachToGoal(ctx(t), middle.ID, sink.ID)

	// Add an incoming edge to middle from source via a relates_to: but
	// AttachToGoal won't do that. Use direct DB insert for the test.
	if _, err := s.db.Exec(
		`INSERT INTO edges (id, from_id, to_id, relation, is_current, metadata, created_at)
		 VALUES ('test-edge-in', ?, ?, 'relates_to', 1, '{}', datetime('now'))`,
		source.ID, middle.ID,
	); err != nil {
		t.Fatal(err)
	}

	v2, err := s.Supersede(ctx(t), middle.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "mid-v2",
		Title: "middle v2",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// Outgoing: middle → sink should now be v2 → sink.
	var outN int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'part_of' AND is_current = 1`,
		v2.ID, sink.ID,
	).Scan(&outN)
	if outN != 1 {
		t.Errorf("outgoing re-point missing: want 1 live v2→sink edge, got %d", outN)
	}
	var outOld int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, mEdge.ID).Scan(&outOld)
	if outOld != 0 {
		t.Errorf("old outgoing edge is_current = %d, want 0", outOld)
	}

	// Incoming: source → middle should now be source → v2.
	var inN int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE from_id = ? AND to_id = ? AND relation = 'relates_to' AND is_current = 1`,
		source.ID, v2.ID,
	).Scan(&inN)
	if inN != 1 {
		t.Errorf("incoming re-point missing: want 1 live source→v2 edge, got %d", inN)
	}
	var inOld int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = 'test-edge-in'`).Scan(&inOld)
	if inOld != 0 {
		t.Errorf("old incoming edge is_current = %d, want 0", inOld)
	}
}

// TestSupersede_NoEdgesIsNoOp ensures the re-point pass is safe on an entry
// with no live edges (the common path).
func TestSupersede_NoEdgesIsNoOp(t *testing.T) {
	s := newService(t)
	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-noedge",
		Title: "alone",
	})
	if _, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-noedge-v2",
		Title: "alone v2",
	}); err != nil {
		t.Errorf("supersede without edges should be a no-op, got %v", err)
	}
}

func TestSupersede_NotCurrentRejected(t *testing.T) {
	s := newService(t)
	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h1", Title: "first",
	})
	_, err := s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h2", Title: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Attempting to supersede v1 again should fail — it's no longer current.
	_, err = s.Supersede(ctx(t), v1.ID, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h3", Title: "third",
	})
	if err == nil {
		t.Error("expected error: cannot supersede non-current entry")
	}
}

// --- helpers ---

func assertFTSHit(t *testing.T, db *sql.DB, term string) {
	t.Helper()
	if !ftsHit(t, db, term) {
		t.Errorf("FTS5 missing term %q", term)
	}
}

func ftsHit(t *testing.T, db *sql.DB, term string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM entries_fts WHERE entries_fts MATCH ?`, term,
	).Scan(&n); err != nil {
		t.Fatalf("fts query %q: %v", term, err)
	}
	return n > 0
}
