package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

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
	if got.IngestedAt.IsZero() || got.OccurredAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("bitemporal timestamps should be set after InsertEntry")
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
	// Validator now allows status=archived on non-goal types (#51), so the
	// error message changed. It still must reject status=open on activity.
	if err == nil || !strings.Contains(err.Error(), "can only carry status=archived") {
		t.Errorf("expected non-goal status rejection, got %v", err)
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

	v2, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
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
	if _, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
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
	_, err := s.Supersede(ctx(t), "00000000-0000-7000-8000-000000000000", 0, domain.Entry{
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
// Superseding an entity no longer re-points its edges: they key on logical_id
// (stable across supersede, decision (a)), so the original edge stays live and
// the link resolves to the new version automatically. (Was
// TestSupersede_RePointsOutgoingEdges.)
func TestSupersede_KeepsEdgeViaLogicalID(t *testing.T) {
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

	v2, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h-out-2",
		Title: "v2",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// The original edge is untouched: still live (no re-point).
	var cur int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, edge.ID).Scan(&cur)
	if cur != 1 {
		t.Errorf("edge should stay live (no re-point), is_current = %d", cur)
	}
	// No edge references the new per-version id — edges key on logical_id.
	var byNewID int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM edges WHERE from_id = ? OR to_id = ?`, v2.ID, v2.ID).Scan(&byNewID)
	if byNewID != 0 {
		t.Errorf("no edge should reference the new version id %s, found %d", v2.ID, byNewID)
	}
	// The edge keys on the shared logical_id; the link resolves v2.logical → g.
	if edge.FromID != v2.LogicalID {
		t.Errorf("edge.from_id = %q, want logical_id %q", edge.FromID, v2.LogicalID)
	}
	live, err := s.EdgesAt(ctx(t), time.Now().UTC(),
		EdgeFilter{Relation: domain.RelationPartOf, FromID: v2.LogicalID, ToID: g.LogicalID})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != edge.ID {
		t.Errorf("EdgesAt(v2.logical→g) = %v, want original edge %s", live, edge.ID)
	}
}

// A single entity with both an incoming and an outgoing live edge: supersede
// leaves BOTH untouched (they key on the entity's logical_id), and both still
// resolve to the new version. (Was TestSupersede_RePointsBothDirections.)
func TestSupersede_BothDirectionEdgesSurvive(t *testing.T) {
	s := newService(t)

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

	// Outgoing: middle → sink (part_of).
	mEdge, _ := s.AttachToGoal(ctx(t), middle.ID, sink.ID)
	// Incoming: source → middle (relates_to). source/middle are fresh, so
	// their ids equal their logical_ids — the edge is already node-keyed.
	// Bind RFC3339Nano timestamps (the format the app uses). SQLite strftime
	// emits millisecond precision + 'Z', which mis-compares LEXICALLY against
	// Go's nanosecond RFC3339Nano in EdgesAt's string-typed TEXT columns ('Z'
	// sorts after digits). A fixed past instant keeps the edge live at now().
	const past = "2026-01-01T00:00:00Z"
	if _, err := s.db.Exec(
		`INSERT INTO edges (id, from_id, to_id, relation, is_current, metadata,
		                    created_at, occurred_at, ingested_at)
		 VALUES ('test-edge-in', ?, ?, 'relates_to', 1, '{}', ?, ?, ?)`,
		source.LogicalID, middle.LogicalID, past, past, past,
	); err != nil {
		t.Fatal(err)
	}

	v2, err := s.Supersede(ctx(t), middle.ID, 0, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "mid-v2",
		Title: "middle v2",
	})
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	if v2.LogicalID != middle.LogicalID {
		t.Fatalf("logical_id drift: %q → %q", middle.LogicalID, v2.LogicalID)
	}

	// Outgoing edge: untouched + still live, keyed on middle's logical_id.
	var outCur int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = ?`, mEdge.ID).Scan(&outCur)
	if outCur != 1 {
		t.Errorf("outgoing edge should stay live, is_current = %d", outCur)
	}
	// Incoming edge: untouched + still live.
	var inCur int
	_ = s.db.QueryRow(`SELECT is_current FROM edges WHERE id = 'test-edge-in'`).Scan(&inCur)
	if inCur != 1 {
		t.Errorf("incoming edge should stay live, is_current = %d", inCur)
	}
	// Both resolve to v2 via its logical_id.
	out, _ := s.EdgesAt(ctx(t), time.Now().UTC(), EdgeFilter{FromID: v2.LogicalID, ToID: sink.LogicalID})
	if len(out) != 1 || out[0].ID != mEdge.ID {
		t.Errorf("outgoing EdgesAt(v2.logical→sink) = %v, want %s", out, mEdge.ID)
	}
	in, _ := s.EdgesAt(ctx(t), time.Now().UTC(), EdgeFilter{ToID: v2.LogicalID})
	if len(in) != 1 || in[0].ID != "test-edge-in" {
		t.Errorf("incoming EdgesAt(→v2.logical) = %v, want test-edge-in", in)
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
	if _, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
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
	_, err := s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h2", Title: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Attempting to supersede v1 again should fail — it's no longer current.
	_, err = s.Supersede(ctx(t), v1.ID, 0, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "h3", Title: "third",
	})
	if err == nil {
		t.Error("expected error: cannot supersede non-current entry")
	}
}

// TestSupersede_OptimisticLockMismatch proves that the optimistic-lock
// contract works: superseding with an expected_version that does not match
// the live row's Version returns ErrVersionConflict. Phase 1 single-writer
// has no real race today, but the structure is in place for Phase 2 sync
// workers (red team #1 in grill doc).
func TestSupersede_OptimisticLockMismatch(t *testing.T) {
	s := newService(t)

	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "lock-1",
		Title: "v1",
	})

	// v1 starts at Version=1. Passing expectedVersion=99 must fail loudly,
	// not silently succeed (the latter would be the silent-failure pattern
	// the design exists to prevent).
	_, err := s.Supersede(ctx(t), v1.ID, 99, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "lock-2",
		Title: "v2",
	})
	if err == nil {
		t.Fatal("expected ErrVersionConflict, got nil")
	}
	if !errors.Is(err, ErrVersionConflict) {
		t.Errorf("expected ErrVersionConflict, got %v", err)
	}
}

func TestSupersede_OptimisticLockMatch(t *testing.T) {
	s := newService(t)

	v1, _ := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "lock-m-1",
		Title: "v1",
	})

	v2, err := s.Supersede(ctx(t), v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "lock-m-2",
		Title: "v2",
	})
	if err != nil {
		t.Fatalf("Supersede with matching version: %v", err)
	}
	if v2.Version != v1.Version+1 {
		t.Errorf("v2.Version = %d, want %d (chain increment)", v2.Version, v1.Version+1)
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
