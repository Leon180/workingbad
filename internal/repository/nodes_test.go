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

// backfillNodes reproduces the node backfill from 0013 + 0014. The migrations
// run once at upgrade time against existing data; in a fresh test DB the
// entries table is empty when they apply, so we re-run the logic here after
// seeding to assert the SQL (one node per logical_id from the live version;
// every version mapped; D2 columns set: logical_id=id, version 1, is_current 1,
// bitemporal pair from the live entry). Kept in lockstep with the migrations —
// if you change the backfill SQL there, change it here too.
func backfillNodes(t *testing.T, s *Service) {
	t.Helper()
	c := ctx(t)
	// 0013 + 0014 fused: insert nodes already carrying the supersede-chain
	// columns rather than insert-then-ALTER, since the test DB has 0014 applied.
	if _, err := s.db.ExecContext(c,
		`INSERT OR IGNORE INTO nodes
		   (id, logical_id, type, title, body, status, version, is_current,
		    occurred_at, ingested_at, created_at, updated_at)
		 SELECT logical_id, logical_id, type, title, COALESCE(body, ''), status, 1, 1,
		        COALESCE(occurred_at, ingested_at, created_at),
		        COALESCE(ingested_at, created_at),
		        created_at, updated_at
		 FROM entries WHERE is_current = 1`); err != nil {
		t.Fatalf("backfill nodes: %v", err)
	}
	if _, err := s.db.ExecContext(c,
		`INSERT OR IGNORE INTO entry_node_map (entry_id, node_id)
		 SELECT id, logical_id FROM entries`); err != nil {
		t.Fatalf("backfill map: %v", err)
	}
	// 0015: backfill nodes_fts from the live nodes just inserted. OR IGNORE
	// keeps a second call idempotent (defensive — tests use a fresh DB each).
	if _, err := s.db.ExecContext(c,
		`INSERT OR IGNORE INTO nodes_fts (node_id, title, body)
		 SELECT id, title, body FROM nodes WHERE is_current = 1`); err != nil {
		t.Fatalf("backfill nodes_fts: %v", err)
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

// --- D2 step 1: node supersede chain (model A) ---

func TestCreateNode_IsOwnLogicalRoot(t *testing.T) {
	s := newService(t)
	got, err := s.CreateNode(ctx(t), domain.Node{
		Type: domain.EntryTypeResearch, Title: "a fresh node",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if got.LogicalID != got.ID {
		t.Errorf("logical_id = %q, want = id %q", got.LogicalID, got.ID)
	}
	if got.Version != 1 || !got.IsCurrent {
		t.Errorf("version/is_current = %d/%v, want 1/true", got.Version, got.IsCurrent)
	}
	if got.OccurredAt.IsZero() || got.IngestedAt.IsZero() {
		t.Error("bitemporal pair not stamped")
	}
}

func TestSupersedeNode_AppendsVersionAndRetiresOld(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	v1, err := s.CreateNode(c, domain.Node{
		Type: domain.EntryTypeGoal, Title: "ship D2 (v1)", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatal(err)
	}

	v2, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeGoal, Title: "ship D2 (v2)", Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("SupersedeNode: %v", err)
	}

	// New version: fresh id, same logical_id, version+1, live, occurred inherited.
	if v2.ID == v1.ID {
		t.Error("supersede must mint a fresh id")
	}
	if v2.LogicalID != v1.LogicalID {
		t.Errorf("logical_id changed: %q → %q", v1.LogicalID, v2.LogicalID)
	}
	if v2.Version != v1.Version+1 {
		t.Errorf("version = %d, want %d", v2.Version, v1.Version+1)
	}
	if !v2.IsCurrent {
		t.Error("new version not live")
	}
	if !v2.OccurredAt.Equal(v1.OccurredAt) {
		t.Errorf("occurred_at not inherited: %v → %v", v1.OccurredAt, v2.OccurredAt)
	}

	// Old version: retired, points forward.
	old, err := s.GetNode(c, v1.ID)
	if err != nil {
		t.Fatalf("GetNode(old): %v", err)
	}
	if old.IsCurrent {
		t.Error("old version still live")
	}
	if old.SupersededBy != v2.ID {
		t.Errorf("old.superseded_by = %q, want %q", old.SupersededBy, v2.ID)
	}

	// Live lookup resolves to v2 only.
	live, err := s.GetLiveNodeByLogicalID(c, v1.LogicalID)
	if err != nil {
		t.Fatalf("GetLiveNodeByLogicalID: %v", err)
	}
	if live.ID != v2.ID || live.Title != "ship D2 (v2)" || live.Status != domain.StatusInProgress {
		t.Errorf("live node = %+v, want v2", live)
	}
}

func TestSupersedeNode_VersionConflict(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeResearch, Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	// Stale expectedVersion (caller saw v1 but live is already past it — here
	// we just pass a wrong number) → conflict, no write.
	_, err = s.SupersedeNode(c, v1.ID, v1.Version+5, domain.Node{
		Type: domain.EntryTypeResearch, Title: "v2 attempt",
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}
	// The chain is untouched: v1 still live.
	live, err := s.GetLiveNodeByLogicalID(c, v1.LogicalID)
	if err != nil || live.ID != v1.ID {
		t.Errorf("v1 should remain live, got %+v err=%v", live, err)
	}
}

func TestSupersedeNode_RejectsSupersededAndMissing(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeDiscuss, Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{Type: domain.EntryTypeDiscuss, Title: "v2"})
	if err != nil {
		t.Fatal(err)
	}

	// Superseding an already-retired version is rejected (not the live head).
	if _, err := s.SupersedeNode(c, v1.ID, 0, domain.Node{Type: domain.EntryTypeDiscuss, Title: "v3?"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("superseding retired version: expected ErrNotFound, got %v", err)
	}
	// Superseding a non-existent id is rejected.
	if _, err := s.SupersedeNode(c, "0192f6c0-7e31-7c2b-9b8a-ffffffffffff", 0, domain.Node{Type: domain.EntryTypeDiscuss, Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("superseding missing id: expected ErrNotFound, got %v", err)
	}
	// Superseding the live head (v2) with skip-check (0) still works.
	if _, err := s.SupersedeNode(c, v2.ID, 0, domain.Node{Type: domain.EntryTypeDiscuss, Title: "v3"}); err != nil {
		t.Errorf("superseding live head with expectedVersion=0: %v", err)
	}
}

func TestSupersedeNode_Validation(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeResearch, Title: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	// Replacement must satisfy the same per-type contract as CreateNode.
	if _, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{Type: domain.EntryTypeGoal, Title: "no status"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("goal replacement without status: expected ErrInvalidInput, got %v", err)
	}
}

func TestGetLiveNodeByLogicalID_NotFound(t *testing.T) {
	s := newService(t)
	_, err := s.GetLiveNodeByLogicalID(ctx(t), "0192f6c0-7e31-7c2b-9b8a-ffffffffffff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// --- D2 step 2: FTS5 search over the live node layer ---

func TestSearchNodes_FindsCreatedNode(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	n, err := s.CreateNode(c, domain.Node{
		Type: domain.EntryTypeResearch, Title: "investigate vector indexing",
		Body: "spike sqlite-vec for embeddings",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchNodes(c, "vector", 10)
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if len(got) != 1 || got[0].ID != n.ID {
		t.Errorf("SearchNodes(vector) = %v, want [%s]", got, n.ID)
	}
	// Body is indexed too.
	got, err = s.SearchNodes(c, "embeddings", 10)
	if err != nil || len(got) != 1 || got[0].ID != n.ID {
		t.Errorf("body term search = %v err=%v, want [%s]", got, err, n.ID)
	}
}

// SupersedeNode must keep nodes_fts mirroring the LIVE version only: the old
// term disappears, the new term appears (node analogue of
// TestSupersede_FTSMirrorsLatest).
func TestSupersedeNode_FTSMirrorsLive(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeDecision, Title: "choose alphaengine"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeDecision, Title: "choose omegaengine",
	})
	if err != nil {
		t.Fatal(err)
	}

	if hits, _ := s.SearchNodes(c, "alphaengine", 10); len(hits) != 0 {
		t.Errorf("stale term still indexed: %v", hits)
	}
	hits, err := s.SearchNodes(c, "omegaengine", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != v2.ID {
		t.Errorf("live term search = %v err=%v, want [%s]", hits, err, v2.ID)
	}
}

func TestSearchNodes_RankingAndLimit(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	// Two nodes mention "cache"; one mentions it twice (stronger bm25).
	weak, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "touch cache once"})
	if err != nil {
		t.Fatal(err)
	}
	strong, err := s.CreateNode(c, domain.Node{
		Type: domain.EntryTypeActivity, Title: "cache cache", Body: "cache layer work",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchNodes(c, "cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	if got[0].ID != strong.ID || got[1].ID != weak.ID {
		t.Errorf("ranking wrong: got [%s, %s], want [%s (strong), %s (weak)]",
			got[0].ID, got[1].ID, strong.ID, weak.ID)
	}
	// limit is honoured.
	one, err := s.SearchNodes(c, "cache", 1)
	if err != nil || len(one) != 1 || one[0].ID != strong.ID {
		t.Errorf("limit=1 = %v err=%v, want [%s]", one, err, strong.ID)
	}
}

// Arbitrary user input — FTS5 operators / quotes / parens — must never error.
func TestSearchNodes_SanitisesInput(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	if _, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "normal node"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`"`, `AND OR NOT`, `foo(bar`, `near/2`, `a* OR b`, `"unterminated`, `\`, `title:foo`, `NEAR(a b)`, `""`} {
		if _, err := s.SearchNodes(c, q, 10); err != nil {
			t.Errorf("SearchNodes(%q) errored on hostile input: %v", q, err)
		}
	}
}

func TestSearchNodes_EmptyQueryReturnsNil(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	if _, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", "   ", "\t\n"} {
		got, err := s.SearchNodes(c, q, 10)
		if err != nil || got != nil {
			t.Errorf("SearchNodes(%q) = %v err=%v, want nil/nil", q, got, err)
		}
	}
}

// Backfilled nodes (from the migration path, not CreateNode) are searchable.
func TestBackfill_NodesAreSearchable(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	seedActivity(t, s, "backfilled searchable widget")
	backfillNodes(t, s)
	got, err := s.SearchNodes(c, "widget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("backfilled node not searchable: got %d hits, want 1", len(got))
	}
}
