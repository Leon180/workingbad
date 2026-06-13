package repository

import (
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// --- D2 step 3: node-layer time-travel (ListNodesAt / NodeHistory) ---

// ListNodesAt at an instant between v1 and v2 must reveal v1 (the version that
// was live then), and after v2's ingest must reveal v2 — node analogue of
// TestListEntriesAt_RevealsSupersededVersion.
func TestListNodesAt_RevealsSupersededVersion(t *testing.T) {
	s := newService(t)
	c := ctx(t)

	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeDecision, Title: "pick approach A"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond) // distinct ingested_at across the supersede
	v2, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeDecision, Title: "pick approach B",
	})
	if err != nil {
		t.Fatal(err)
	}

	// At v1's ingest instant the supersede (v2) hadn't happened yet → v1 live.
	atV1, err := s.ListNodesAt(c, v1.IngestedAt, NodeListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(atV1) != 1 || atV1[0].ID != v1.ID {
		t.Errorf("ListNodesAt(v1.ingested) = %v, want [v1 %s]", ids(atV1), v1.ID)
	}

	// At v2's ingest instant v1 has a successor ingested <= asOf → v2 live.
	atV2, err := s.ListNodesAt(c, v2.IngestedAt, NodeListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(atV2) != 1 || atV2[0].ID != v2.ID {
		t.Errorf("ListNodesAt(v2.ingested) = %v, want [v2 %s]", ids(atV2), v2.ID)
	}
}

func TestNodeHistory_ReturnsFullChainNewestFirst(t *testing.T) {
	s := newService(t)
	c := ctx(t)

	v1, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeGoal, Title: "v1", Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SupersedeNode(c, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeGoal, Title: "v2", Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatal(err)
	}
	v3, err := s.SupersedeNode(c, v2.ID, v2.Version, domain.Node{
		Type: domain.EntryTypeGoal, Title: "v3", Status: domain.StatusDone,
	})
	if err != nil {
		t.Fatal(err)
	}

	hist, err := s.NodeHistory(c, v1.LogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	wantIDs := []string{v3.ID, v2.ID, v1.ID}
	wantVer := []int{3, 2, 1}
	for i := range hist {
		if hist[i].ID != wantIDs[i] || hist[i].Version != wantVer[i] {
			t.Errorf("history[%d] = (%s v%d), want (%s v%d)",
				i, hist[i].ID, hist[i].Version, wantIDs[i], wantVer[i])
		}
	}
	// Every version shares the one logical_id.
	for _, n := range hist {
		if n.LogicalID != v1.LogicalID {
			t.Errorf("version %s has logical_id %s, want %s", n.ID, n.LogicalID, v1.LogicalID)
		}
	}
}

func TestListNodesAt_RequiresNonZeroAsOf(t *testing.T) {
	s := newService(t)
	if _, err := s.ListNodesAt(ctx(t), time.Time{}, NodeListFilter{}); err == nil {
		t.Error("expected error for zero asOf")
	}
}

func TestNodeHistory_RequiresLogicalID(t *testing.T) {
	s := newService(t)
	if _, err := s.NodeHistory(ctx(t), ""); err == nil {
		t.Error("expected error for empty logical_id")
	}
}

func TestListNodesAt_TypeFilter(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	if _, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeActivity, Title: "an activity"}); err != nil {
		t.Fatal(err)
	}
	research, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeResearch, Title: "a research"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListNodesAt(c, time.Now().UTC(), NodeListFilter{Type: domain.EntryTypeResearch})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != research.ID {
		t.Errorf("type filter = %v, want [research %s]", ids(got), research.ID)
	}
}

func TestListNodesAt_ExcludesArchivedByDefault(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	open, err := s.CreateNode(c, domain.Node{Type: domain.EntryTypeGoal, Title: "live goal", Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SupersedeNode(c, open.ID, open.Version, domain.Node{
		Type: domain.EntryTypeGoal, Title: "shelved goal", Status: domain.StatusArchived,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	// Default: archived goal hidden.
	got, err := s.ListNodesAt(c, now, NodeListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("default should hide archived, got %v", ids(got))
	}
	// Opt in: archived goal shown.
	got, err = s.ListNodesAt(c, now, NodeListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != domain.StatusArchived {
		t.Errorf("IncludeArchived should show it, got %v", ids(got))
	}
}

// ids is a small helper for readable failure messages.
func ids(ns []domain.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}
