package repository

import (
	"errors"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// --- programmatic node API (CreateNode / GetNode / map / round-trips) ---

func TestCreateNode_AssignsIDAndTimestamps(t *testing.T) {
	s := newService(t)
	got, err := s.CreateNode(ctx(t), domain.Node{
		Type: domain.EntryTypeActivity, Title: "a node",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if got.ID == "" {
		t.Error("id not assigned")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps not stamped")
	}
	// Round-trips through GetNode with identical content.
	back, err := s.GetNode(ctx(t), got.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if back.Title != "a node" || back.Type != domain.EntryTypeActivity {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestCreateNode_Validation(t *testing.T) {
	s := newService(t)
	cases := []struct {
		name string
		n    domain.Node
	}{
		{"empty title", domain.Node{Type: domain.EntryTypeActivity}},
		{"bad type", domain.Node{Type: domain.EntryType("nope"), Title: "x"}},
		{"goal without status", domain.Node{Type: domain.EntryTypeGoal, Title: "g"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateNode(ctx(t), tc.n)
			if err == nil || !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestCreateNode_GoalWithStatusAccepted(t *testing.T) {
	s := newService(t)
	_, err := s.CreateNode(ctx(t), domain.Node{
		Type: domain.EntryTypeGoal, Title: "ship it", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("goal node with status should be accepted: %v", err)
	}
}

func TestGetNode_NotFound(t *testing.T) {
	s := newService(t)
	_, err := s.GetNode(ctx(t), "0192f6c0-7e31-7c2b-9b8a-ffffffffffff")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMapEntryToNode_IdempotentAndRoundTrip(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "an entry")
	n, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "its node"})
	if err != nil {
		t.Fatal(err)
	}

	// Map twice — second is a no-op, not an error.
	if err := s.MapEntryToNode(c, e.ID, n.ID); err != nil {
		t.Fatalf("map 1: %v", err)
	}
	if err := s.MapEntryToNode(c, e.ID, n.ID); err != nil {
		t.Fatalf("map 2 (idempotent): %v", err)
	}

	// NodesForEntry → [n]
	nodes, err := s.NodesForEntry(c, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != n.ID {
		t.Errorf("NodesForEntry = %v, want [%s]", nodes, n.ID)
	}
	// EntriesForNode → [e]
	entries, err := s.EntriesForNode(c, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != e.ID {
		t.Errorf("EntriesForNode = %v, want [%s]", entries, e.ID)
	}
}

func TestMapEntryToNode_NotFoundOnBadIDs(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "real entry")
	n, _ := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "real node"})

	if err := s.MapEntryToNode(c, "bogus-entry", n.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad entry id: expected ErrNotFound, got %v", err)
	}
	if err := s.MapEntryToNode(c, e.ID, "bogus-node"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad node id: expected ErrNotFound, got %v", err)
	}
}

// --- backfill SQL correctness (white-box) ---

// backfillNodes runs the exact two INSERT...SELECT statements from
// 0013_nodes_and_entry_node_map.sql. The migration runs them once at upgrade
// time against existing data; in a fresh test DB the entries table is empty
// when 0013 applies, so we re-run them here after seeding to assert the SQL
// logic (one node per logical_id from the live version; every version mapped).
// Kept in lockstep with the migration — if you change the backfill SQL there,
// change it here too.
func backfillNodes(t *testing.T, s *Service) {
	t.Helper()
	c := ctx(t)
	if _, err := s.db.ExecContext(c,
		`INSERT OR IGNORE INTO nodes (id, type, title, body, status, created_at, updated_at)
		 SELECT logical_id, type, title, COALESCE(body, ''), status, created_at, updated_at
		 FROM entries WHERE is_current = 1`); err != nil {
		t.Fatalf("backfill nodes: %v", err)
	}
	if _, err := s.db.ExecContext(c,
		`INSERT OR IGNORE INTO entry_node_map (entry_id, node_id)
		 SELECT id, logical_id FROM entries`); err != nil {
		t.Fatalf("backfill map: %v", err)
	}
}

// TestBackfill_OneNodePerLogicalID — a superseded chain (2 entry rows, 1
// logical_id) + a standalone entry (1 logical_id) backfills to 2 nodes, with
// every entry version mapped, and the chain's node carrying the LIVE version's
// content.
func TestBackfill_OneNodePerLogicalID(t *testing.T) {
	s := newService(t)
	c := ctx(t)

	// Goal v1 → supersede to v2 (same logical_id, 2 rows, v2 live).
	v1, err := s.InsertEntry(c, domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "goal-1",
		Title: "Ship Slice D (v1)", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.Supersede(c, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "goal-1",
		Title: "Ship Slice D (v2)", Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Standalone activity (distinct logical_id).
	act := seedActivity(t, s, "a standalone activity")

	backfillNodes(t, s)

	// 2 distinct logical_ids → 2 nodes.
	count, err := s.CountNodes(c)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("node count = %d, want 2 (one per logical_id)", count)
	}

	// The goal node id == its logical_id, content == LIVE (v2).
	goalNode, err := s.GetNode(c, v2.LogicalID)
	if err != nil {
		t.Fatalf("goal node by logical_id: %v", err)
	}
	if goalNode.Title != "Ship Slice D (v2)" || goalNode.Status != domain.StatusInProgress {
		t.Errorf("goal node carries stale content: %+v (want v2 / in_progress)", goalNode)
	}

	// Both goal versions map to the same goal node.
	for _, id := range []string{v1.ID, v2.ID} {
		nodes, err := s.NodesForEntry(c, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 || nodes[0].ID != v2.LogicalID {
			t.Errorf("entry %s maps to %v, want goal node %s", id, nodes, v2.LogicalID)
		}
	}

	// The goal node gathers exactly its 2 entry versions.
	entries, err := s.EntriesForNode(c, v2.LogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("goal node gathers %d entries, want 2 (v1 + v2)", len(entries))
	}

	// The standalone activity backfilled to its own node.
	if _, err := s.GetNode(c, act.LogicalID); err != nil {
		t.Errorf("standalone activity node missing: %v", err)
	}
}
