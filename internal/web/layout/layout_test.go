package layout

import (
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

func TestBuild_OneLanePerGoal(t *testing.T) {
	g1 := domain.Entry{ID: "G1", Type: domain.EntryTypeGoal, Title: "Alpha"}
	g2 := domain.Entry{ID: "G2", Type: domain.EntryTypeGoal, Title: "Beta"}
	c := Build([]domain.Entry{g1, g2}, nil)
	if len(c.Lanes) != 2 {
		t.Fatalf("expected 2 lanes, got %d", len(c.Lanes))
	}
	// Sorted by title for determinism: Alpha first.
	if c.Lanes[0].Goal.Title != "Alpha" {
		t.Errorf("first lane should be Alpha, got %q", c.Lanes[0].Goal.Title)
	}
	if c.Lanes[1].Y <= c.Lanes[0].Y {
		t.Errorf("lanes should be vertically stacked, got Y0=%.1f Y1=%.1f", c.Lanes[0].Y, c.Lanes[1].Y)
	}
}

func TestBuild_GoalsAnchorRight(t *testing.T) {
	goal := domain.Entry{ID: "G", Type: domain.EntryTypeGoal, Title: "G"}
	act := domain.Entry{ID: "A", Type: domain.EntryTypeActivity, Title: "A"}
	c := Build(
		[]domain.Entry{goal, act},
		[]domain.Edge{{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true}},
	)
	var gn, an *Node
	for i := range c.Nodes {
		switch c.Nodes[i].Entry.ID {
		case "G":
			gn = &c.Nodes[i]
		case "A":
			an = &c.Nodes[i]
		}
	}
	if !gn.IsGoal {
		t.Error("G should be a goal node")
	}
	if gn.X <= an.X {
		t.Errorf("goal X=%.1f should be right of entry X=%.1f", gn.X, an.X)
	}
	if gn.LaneIndex != an.LaneIndex {
		t.Errorf("activity should land on its goal's lane (G=%d, A=%d)", gn.LaneIndex, an.LaneIndex)
	}
}

func TestBuild_OrphanLane(t *testing.T) {
	goal := domain.Entry{ID: "G", Type: domain.EntryTypeGoal, Title: "G"}
	attached := domain.Entry{ID: "A", Type: domain.EntryTypeActivity, Title: "A"}
	orphan := domain.Entry{ID: "O", Type: domain.EntryTypeResearch, Title: "O"}
	c := Build(
		[]domain.Entry{goal, attached, orphan},
		[]domain.Edge{{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true}},
	)
	if len(c.Lanes) != 2 {
		t.Fatalf("expected goal-lane + orphan-lane (2), got %d", len(c.Lanes))
	}
	if c.Lanes[1].Goal != nil {
		t.Error("second lane should be the orphan lane (Goal == nil)")
	}
	var on *Node
	for i := range c.Nodes {
		if c.Nodes[i].Entry.ID == "O" {
			on = &c.Nodes[i]
		}
	}
	if on.LaneIndex != 1 {
		t.Errorf("orphan should sit in orphan lane (1), got %d", on.LaneIndex)
	}
}

func TestBuild_EntriesOrderedByOccurredAt(t *testing.T) {
	now := time.Now().UTC()
	goal := domain.Entry{ID: "G", Type: domain.EntryTypeGoal, Title: "G"}
	early := domain.Entry{ID: "E", Type: domain.EntryTypeDecision, Title: "early",
		OccurredAt: now.Add(-2 * time.Hour)}
	late := domain.Entry{ID: "L", Type: domain.EntryTypeDecision, Title: "late",
		OccurredAt: now}

	c := Build(
		[]domain.Entry{goal, late, early},
		[]domain.Edge{
			{FromID: "E", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
			{FromID: "L", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
		},
	)
	var en, ln *Node
	for i := range c.Nodes {
		switch c.Nodes[i].Entry.ID {
		case "E":
			en = &c.Nodes[i]
		case "L":
			ln = &c.Nodes[i]
		}
	}
	if en.X >= ln.X {
		t.Errorf("early should be left of late: early.X=%.1f late.X=%.1f", en.X, ln.X)
	}
}

func TestBuild_SameLanePartOfDroppedFromEdges(t *testing.T) {
	goal := domain.Entry{ID: "G", Type: domain.EntryTypeGoal, Title: "G"}
	act := domain.Entry{ID: "A", Type: domain.EntryTypeActivity, Title: "A"}
	c := Build(
		[]domain.Entry{goal, act},
		[]domain.Edge{{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true}},
	)
	if len(c.Edges) != 0 {
		t.Errorf("same-lane part_of edges should not render (the lane line implies it), got %d", len(c.Edges))
	}
}

func TestBuild_CrossLaneEdgeRenders(t *testing.T) {
	g1 := domain.Entry{ID: "G1", Type: domain.EntryTypeGoal, Title: "G1"}
	g2 := domain.Entry{ID: "G2", Type: domain.EntryTypeGoal, Title: "G2"}
	a := domain.Entry{ID: "A", Type: domain.EntryTypeActivity, Title: "A"}
	b := domain.Entry{ID: "B", Type: domain.EntryTypeActivity, Title: "B"}

	c := Build(
		[]domain.Entry{g1, g2, a, b},
		[]domain.Edge{
			{FromID: "A", ToID: "G1", Relation: domain.RelationPartOf, IsCurrent: true},
			{FromID: "B", ToID: "G2", Relation: domain.RelationPartOf, IsCurrent: true},
			// Cross-lane relates_to between A and B → MUST render.
			{FromID: "A", ToID: "B", Relation: domain.RelationRelatesTo, IsCurrent: true},
		},
	)
	if len(c.Edges) != 1 {
		t.Errorf("expected 1 cross-lane edge, got %d", len(c.Edges))
	}
	if c.Edges[0].Style != "relates_to" {
		t.Errorf("style should be relates_to, got %q", c.Edges[0].Style)
	}
}

func TestBuild_EmptyInputReturnsEmptyCanvas(t *testing.T) {
	c := Build(nil, nil)
	if len(c.Lanes) != 0 || len(c.Nodes) != 0 || len(c.Edges) != 0 {
		t.Error("empty input → empty canvas")
	}
	if c.Width <= 0 || c.Height <= 0 {
		t.Error("empty canvas should still have positive viewport dims")
	}
}

func TestBuild_DropsEdgesToUnknownNode(t *testing.T) {
	c := Build(
		[]domain.Entry{{ID: "G", Type: domain.EntryTypeGoal, Title: "G"}},
		[]domain.Edge{{FromID: "ghost", ToID: "G", Relation: domain.RelationRelatesTo, IsCurrent: true}},
	)
	if len(c.Edges) != 0 {
		t.Error("edge referencing unknown node should be dropped")
	}
}
