package layout

import (
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

func tlEntry(id string, typ domain.EntryType, title string, occurred time.Time) domain.Entry {
	return domain.Entry{ID: id, Type: typ, Title: title, OccurredAt: occurred}
}

// TestBuildTimeline_HonestTimePositioning — node X reflects real occurred_at,
// so a later entry sits to the right of an earlier one (not evenly spaced).
func TestBuildTimeline_HonestTimePositioning(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	g := tlEntry("G", domain.EntryTypeGoal, "Goal", time.Time{})
	early := tlEntry("E", domain.EntryTypeActivity, "early", base)
	late := tlEntry("L", domain.EntryTypeActivity, "late", base.Add(72*time.Hour))
	edges := []domain.Edge{
		{FromID: "E", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
		{FromID: "L", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
	}
	c := BuildTimeline([]domain.Entry{g, early, late}, edges, DefaultPxPerDay)

	var ex, lx float64
	for _, n := range c.Nodes {
		switch n.Entry.ID {
		case "E":
			ex = n.X
		case "L":
			lx = n.X
		}
	}
	if lx <= ex {
		t.Errorf("later entry X=%.1f should be right of earlier X=%.1f", lx, ex)
	}
	// 3 days at DefaultPxPerDay should be a meaningful gap, not min-spacing.
	if lx-ex < 3*DefaultPxPerDay-1 {
		t.Errorf("gap %.1f should be ~3·pxPerDay (%.1f) for a 3-day span", lx-ex, 3*DefaultPxPerDay)
	}
}

// TestBuildTimeline_GoalsPinRight — goal anchors live in the right pinned
// column at ContentWidth − GoalColW, regardless of their lane's last entry.
func TestBuildTimeline_GoalsPinRight(t *testing.T) {
	g := tlEntry("G", domain.EntryTypeGoal, "Goal", time.Time{})
	a := tlEntry("A", domain.EntryTypeActivity, "a", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	c := BuildTimeline(
		[]domain.Entry{g, a},
		[]domain.Edge{{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true}},
		DefaultPxPerDay,
	)
	if c.GoalColW <= 0 || c.ContentWidth <= 0 {
		t.Fatalf("timeline canvas missing geometry: GoalColW=%.1f ContentWidth=%.1f", c.GoalColW, c.ContentWidth)
	}
	wantGoalX := c.ContentWidth - c.GoalColW
	if c.Lanes[0].GoalX != wantGoalX {
		t.Errorf("goal X=%.1f, want pinned right at %.1f", c.Lanes[0].GoalX, wantGoalX)
	}
}

// TestBuildTimeline_ZoomChangesDensity — a higher px-per-day widens the
// content (time stretches) without changing the lane count.
func TestBuildTimeline_ZoomChangesDensity(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	g := tlEntry("G", domain.EntryTypeGoal, "Goal", time.Time{})
	a := tlEntry("A", domain.EntryTypeActivity, "a", base)
	b := tlEntry("B", domain.EntryTypeActivity, "b", base.Add(96*time.Hour))
	edges := []domain.Edge{
		{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
		{FromID: "B", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
	}
	in := []domain.Entry{g, a, b}
	out := BuildTimeline(in, edges, 100)
	zoomed := BuildTimeline(in, edges, 400)
	if zoomed.ContentWidth <= out.ContentWidth {
		t.Errorf("zoom-in width %.1f should exceed zoom-out width %.1f", zoomed.ContentWidth, out.ContentWidth)
	}
}

// TestBuildTimeline_CollisionNudge — two entries at the same timestamp in one
// lane get pushed apart by at least the min gap instead of fully overlapping.
func TestBuildTimeline_CollisionNudge(t *testing.T) {
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	g := tlEntry("G", domain.EntryTypeGoal, "Goal", time.Time{})
	a := tlEntry("A", domain.EntryTypeActivity, "a", ts)
	b := tlEntry("B", domain.EntryTypeActivity, "b", ts)
	edges := []domain.Edge{
		{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
		{FromID: "B", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
	}
	c := BuildTimeline([]domain.Entry{g, a, b}, edges, DefaultPxPerDay)
	var ax, bx float64
	for _, n := range c.Nodes {
		switch n.Entry.ID {
		case "A":
			ax = n.X
		case "B":
			bx = n.X
		}
	}
	if diff := bx - ax; diff < tlMinGap-0.01 {
		t.Errorf("same-timestamp dots only %.1f apart, want ≥ %.1f", diff, tlMinGap)
	}
}

// TestBuildTimeline_TickGranularity — sub-2-day spans label hours; longer
// spans label days.
func TestBuildTimeline_TickGranularity(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	g := tlEntry("G", domain.EntryTypeGoal, "Goal", time.Time{})
	mk := func(end time.Time) Canvas {
		a := tlEntry("A", domain.EntryTypeActivity, "a", base)
		b := tlEntry("B", domain.EntryTypeActivity, "b", end)
		edges := []domain.Edge{
			{FromID: "A", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
			{FromID: "B", ToID: "G", Relation: domain.RelationPartOf, IsCurrent: true},
		}
		return BuildTimeline([]domain.Entry{g, a, b}, edges, DefaultPxPerDay)
	}
	hourly := mk(base.Add(6 * time.Hour))
	daily := mk(base.Add(10 * 24 * time.Hour))
	if len(hourly.Ticks) == 0 || len(daily.Ticks) == 0 {
		t.Fatal("expected ticks on both spans")
	}
	// "15:04" contains a colon; "01-02" does not.
	if !contains(hourly.Ticks[0].Label, ":") {
		t.Errorf("≤2-day span should label hours, got %q", hourly.Ticks[0].Label)
	}
	if contains(daily.Ticks[len(daily.Ticks)-1].Label, ":") {
		t.Errorf("multi-day span should label days, got %q", daily.Ticks[len(daily.Ticks)-1].Label)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
