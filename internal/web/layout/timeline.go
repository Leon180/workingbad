package layout

import (
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Timeline geometry. The live graph view (graph-ui v2) is a plurk-style
// pinned timeline: lane titles pin to the left edge, goal anchors pin to the
// right edge, and the middle band scrolls horizontally along a real
// occurred_at axis (node X = true event time, so clusters and gaps are
// honest). Zoom changes px-per-day (time density), not a viewBox scale.
const (
	tlLaneHeight = 88.0  // vertical gap between timeline lanes
	tlTopPad     = 52.0  // headroom above the first lane
	tlAxisPad    = 44.0  // room below the last lane for the time-tick axis
	tlTitleW     = 150.0 // pinned left lane-title column width
	tlGoalW      = 212.0 // pinned right goal-anchor column width
	tlLeftPad    = 28.0  // gap between title column and the first node
	tlRightPad   = 28.0  // gap between the last node and the goal column
	tlDotR       = 7.0   // entry dot radius
	tlMinGap     = 17.0  // collision nudge: min px between same-lane dots
	tlMinSpanPx  = 240.0 // floor so zero/tiny-span graphs still get a band

	// DefaultPxPerDay is the density the server renders before the client
	// measures the viewport and requests a fit-to-width zoom on load.
	DefaultPxPerDay = 160.0
	MinPxPerDay     = 6.0
	MaxPxPerDay     = 6000.0
)

// BuildTimeline lays the graph out as a horizontally-scrollable timeline at a
// given px-per-day density. Lanes/buckets come from the shared assignLanes;
// only the X geometry is timeline-specific:
//
//   - node X = bandLeft + (occurred_at − minT) · pxPerDay, so spacing is honest
//   - light per-lane collision nudging keeps same-timestamp dots from fully
//     overlapping (pushes right only, preserving time order)
//   - goal anchors pin at the right column; lane titles at the left column
//     (both rendered as overlays by the template, not in the scroll SVG)
//   - day ticks (hour ticks when the span is ≤ 2 days) label the bottom axis
func BuildTimeline(entries []domain.Entry, edges []domain.Edge, pxPerDay float64) Canvas {
	if len(entries) == 0 {
		return Canvas{Width: MinCanvasW, Height: 200}
	}
	pxPerDay = clampPxPerDay(pxPerDay)

	la := assignLanes(entries, edges)
	lanes := la.lanes

	minT, maxT := occurredRange(entries)
	spanSec := maxT.Sub(minT).Seconds()
	spanDays := spanSec / 86400.0

	bandLeft := tlTitleW + tlLeftPad
	spanPx := spanDays * pxPerDay
	if spanPx < tlMinSpanPx {
		spanPx = tlMinSpanPx
	}
	contentWidth := bandLeft + spanPx + tlRightPad + tlGoalW
	if contentWidth < MinCanvasW {
		contentWidth = MinCanvasW
	}
	goalX := contentWidth - tlGoalW

	// xOf maps an event time onto the scroll band. Collapses to bandLeft when
	// every entry shares a timestamp (spanSec == 0) — nudging spreads them.
	xOf := func(t time.Time) float64 {
		if spanSec <= 0 || t.IsZero() {
			return bandLeft
		}
		return bandLeft + (t.Sub(minT).Seconds()/spanSec)*spanPx
	}

	height := tlTopPad + float64(len(lanes))*tlLaneHeight + tlAxisPad
	if height < 240 {
		height = 240
	}
	for i := range lanes {
		lanes[i].Y = tlTopPad + float64(i)*tlLaneHeight + tlLaneHeight/2
		lanes[i].GoalX = goalX
		lanes[i].GoalW = tlGoalW
		lanes[i].GoalH = GoalNodeH
	}

	// Place dots by event time, then nudge for collisions within each lane.
	nodes := make([]Node, 0, len(entries))
	for li, bucket := range la.buckets {
		laneY := lanes[li].Y
		colourIdx := lanes[li].ColorIndex
		prevX := -1e9
		for _, e := range bucket {
			x := xOf(e.OccurredAt)
			if x < prevX+tlMinGap {
				x = prevX + tlMinGap
			}
			prevX = x
			nodes = append(nodes, Node{
				Entry:      e,
				X:          x,
				Y:          laneY,
				R:          tlDotR,
				LaneIndex:  li,
				ColorIndex: colourIdx,
			})
		}
	}

	edgePaths := buildEdges(edges, nodes, lanes, la.goalToLane)
	ticks, axisX1, axisX2 := tlTicks(minT, maxT, bandLeft, spanPx, spanDays)

	return Canvas{
		Lanes:        lanes,
		Nodes:        nodes,
		Edges:        edgePaths,
		Width:        contentWidth,
		Height:       height,
		Ticks:        ticks,
		AxisX1:       axisX1,
		AxisX2:       axisX2,
		ContentWidth: contentWidth,
		PxPerDay:     pxPerDay,
		TitleW:       tlTitleW,
		GoalColW:     tlGoalW,
		SpanDays:     spanDays,
	}
}

func clampPxPerDay(p float64) float64 {
	switch {
	case p <= 0:
		return DefaultPxPerDay
	case p < MinPxPerDay:
		return MinPxPerDay
	case p > MaxPxPerDay:
		return MaxPxPerDay
	default:
		return p
	}
}

// occurredRange returns the min/max occurred_at across non-goal entries,
// ignoring zero times. Both are zero when nothing has a usable event time.
func occurredRange(entries []domain.Entry) (minT, maxT time.Time) {
	for _, e := range entries {
		if e.Type == domain.EntryTypeGoal || e.OccurredAt.IsZero() {
			continue
		}
		if minT.IsZero() || e.OccurredAt.Before(minT) {
			minT = e.OccurredAt
		}
		if maxT.IsZero() || e.OccurredAt.After(maxT) {
			maxT = e.OccurredAt
		}
	}
	return minT, maxT
}

// buildEdges resolves cross-lane edges against dot positions plus the pinned
// goal anchors (so part_of edges that cross lanes still draw to the goal).
// Same-lane part_of edges stay implicit (the lane line already says it).
func buildEdges(edges []domain.Edge, nodes []Node, lanes []Lane, goalToLane map[string]int) []EdgePath {
	type pos struct {
		x, y float64
		lane int
	}
	posByID := make(map[string]pos, len(nodes)+len(goalToLane))
	for i := range nodes {
		posByID[nodes[i].Entry.ID] = pos{nodes[i].X, nodes[i].Y, nodes[i].LaneIndex}
	}
	for goalID, li := range goalToLane {
		posByID[goalID] = pos{lanes[li].GoalX, lanes[li].Y, li}
	}

	out := make([]EdgePath, 0, len(edges))
	for _, e := range edges {
		fn, ok := posByID[e.FromID]
		if !ok {
			continue
		}
		tn, ok := posByID[e.ToID]
		if !ok {
			continue
		}
		if e.Relation == domain.RelationPartOf && fn.lane == tn.lane {
			continue
		}
		midX := (fn.x + tn.x) / 2
		midY := (fn.y + tn.y) / 2
		if fn.lane == tn.lane {
			midY -= 24 // bow same-lane non-part_of edges above the lane line
		}
		out = append(out, EdgePath{
			Edge:  e,
			FromX: fn.x, FromY: fn.y,
			ToX: tn.x, ToY: tn.y,
			MidX: midX, MidY: midY,
			Style: string(e.Relation),
		})
	}
	return out
}

// tlTicks labels the bottom axis with day ticks (hour ticks when the span is
// ≤ 2 days), evenly spaced at roughly one per 140px and capped so dense zooms
// don't crowd. Mapped onto the same band the dots use.
func tlTicks(minT, maxT time.Time, bandLeft, spanPx, spanDays float64) ([]AxisTick, float64, float64) {
	if minT.IsZero() || maxT.IsZero() || spanDays <= 0 {
		return nil, bandLeft, bandLeft + spanPx
	}
	format := "01-02"
	if spanDays <= 2 {
		format = "15:04"
	}
	count := int(spanPx/140) + 1
	if count < 2 {
		count = 2
	}
	if count > 14 {
		count = 14
	}
	span := maxT.Sub(minT)
	ticks := make([]AxisTick, 0, count)
	for i := 0; i < count; i++ {
		frac := float64(i) / float64(count-1)
		t := minT.Add(time.Duration(float64(span) * frac))
		ticks = append(ticks, AxisTick{
			X:     bandLeft + frac*spanPx,
			Label: t.UTC().Format(format),
		})
	}
	return ticks, bandLeft, bandLeft + spanPx
}
