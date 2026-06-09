// Package layout computes a 2D placement for the workingbad knowledge
// graph in a git-style swim-lane visual:
//
//   - one horizontal lane per goal (its "branch")
//   - each entry attached to that goal is a coloured dot on the lane
//   - the goal itself anchors the right end of its lane
//   - X axis = time (occurred_at), left = old, right = recent → goal
//   - lane colour propagates to every entry in that lane (so the lane
//     identity is visible even when entries cluster)
//   - cross-lane edges (relates_to / blocks / derived_from / iteration_of)
//     draw bezier curves between dots in different lanes
//
// Phase-1 scope (grill: docs/grill/2026-06-09-graph-ui.md): time spread
// uses occurred_at when the data has meaningful variance, otherwise falls
// back to even spacing within each lane so demo data isn't cramped.
package layout

import (
	"sort"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// Lane is one horizontal track: a goal (or the orphan placeholder) plus
// every entry attached to it via part_of. Lanes are ordered top-to-bottom
// and each has a distinct colour from the laneColours palette.
type Lane struct {
	Goal       *domain.Entry // nil for the orphan lane
	Y          float64
	ColorIndex int           // index into laneColours; rendered via CSS class lane-N
	GoalX      float64       // X of the goal node (or right edge if orphan)
	GoalW      float64
	GoalH      float64
}

// Node is one positioned entry — for goals these are the lane anchor; for
// everything else, a small coloured dot on its lane.
type Node struct {
	Entry      domain.Entry
	X, Y       float64
	R          float64 // dot radius (or shape extent for goal)
	LaneIndex  int     // which lane this belongs to (matches Canvas.Lanes)
	IsGoal     bool
	ColorIndex int
}

// EdgePath is a cross-lane edge between two dots. Same-lane part_of edges
// are implicit (the lane line) so we skip them — adding them would clutter
// the visual without semantic gain (the lane already says "these belong
// to this goal").
type EdgePath struct {
	Edge        domain.Edge
	FromX, FromY float64
	ToX, ToY     float64
	MidX, MidY   float64
	Style        string
}

// Canvas is what the SVG template consumes.
type Canvas struct {
	Lanes  []Lane
	Nodes  []Node
	Edges  []EdgePath
	Width  float64
	Height float64
}

// Visual constants. Tuned to feel airy on a 1440-wide laptop browser.
const (
	LaneHeight    = 90.0  // vertical gap between lanes
	DotRadius     = 9.0   // entry dot radius
	GoalNodeW     = 180.0
	GoalNodeH     = 56.0
	MarginTop     = 60.0
	MarginBottom  = 40.0
	MarginLeft    = 180.0 // wide enough to fit the lane-name label
	MarginRight   = 40.0
	MinCanvasW    = 720.0
	NodeMinSpacing = 110.0 // minimum horizontal gap between adjacent dots in a lane
)

// laneColours is the palette — same family as git branch colors but
// muted for a notebook aesthetic. Indexes wrap if more lanes than colours.
var laneColours = []string{
	"blue",   // lane-0
	"green",  // lane-1
	"orange", // lane-2
	"purple", // lane-3
	"teal",   // lane-4
	"pink",   // lane-5
}

// Build is the entry point. Strategy:
//
//  1. Identify goals → each gets a lane in title-asc order (deterministic).
//  2. Find every non-goal entry's primary goal via incoming part_of edges.
//     If none, the entry goes to the orphan lane (the lowest track).
//  3. Within each lane, sort entries by OccurredAt asc.
//  4. Position: goals pin to the right (MarginRight + GoalNodeW),
//     entries map onto their lane's row, X by occurred_at spread when
//     the range > NodeMinSpacing, else by even index spacing.
//  5. Cross-lane edges (everything that isn't a same-lane part_of) become
//     bezier curves between the two endpoint dots.
func Build(entries []domain.Entry, edges []domain.Edge) Canvas {
	if len(entries) == 0 {
		return Canvas{Width: MinCanvasW, Height: 200}
	}

	// Step 1: lanes from goals (sorted by title for determinism).
	goals := []domain.Entry{}
	for _, e := range entries {
		if e.Type == domain.EntryTypeGoal {
			goals = append(goals, e)
		}
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].Title < goals[j].Title })

	lanes := make([]Lane, 0, len(goals)+1)
	goalToLane := make(map[string]int, len(goals))
	for i := range goals {
		lanes = append(lanes, Lane{
			Goal:       &goals[i],
			ColorIndex: i % len(laneColours),
		})
		goalToLane[goals[i].ID] = i
	}
	orphanLaneIdx := -1

	// Step 2: assign each non-goal to its goal's lane via incoming part_of.
	// An entry can technically be part_of multiple goals; we pick the
	// alphabetically-first goal as its lane home so the assignment is
	// deterministic. relates_to / blocks / derived_from / iteration_of
	// do NOT drive lane assignment — they become cross-lane edges.
	goalsOfEntry := make(map[string][]string) // entryID → []goalID
	for _, e := range edges {
		if e.Relation != domain.RelationPartOf {
			continue
		}
		if _, isGoal := goalToLane[e.ToID]; !isGoal {
			continue
		}
		goalsOfEntry[e.FromID] = append(goalsOfEntry[e.FromID], e.ToID)
	}

	entryLane := make(map[string]int, len(entries))
	for _, e := range entries {
		if e.Type == domain.EntryTypeGoal {
			entryLane[e.ID] = goalToLane[e.ID]
			continue
		}
		homeGoals := goalsOfEntry[e.ID]
		if len(homeGoals) == 0 {
			if orphanLaneIdx == -1 {
				orphanLaneIdx = len(lanes)
				lanes = append(lanes, Lane{
					Goal:       nil,
					ColorIndex: len(laneColours), // ".lane-orphan" in CSS
				})
			}
			entryLane[e.ID] = orphanLaneIdx
			continue
		}
		sort.Strings(homeGoals)
		entryLane[e.ID] = goalToLane[homeGoals[0]]
	}

	// Step 3: bucket entries by lane, sort within bucket by OccurredAt.
	buckets := make([][]domain.Entry, len(lanes))
	for _, e := range entries {
		if e.Type == domain.EntryTypeGoal {
			continue // goals render as the lane anchor, not as a bucket entry
		}
		idx := entryLane[e.ID]
		buckets[idx] = append(buckets[idx], e)
	}
	for li := range buckets {
		sort.Slice(buckets[li], func(i, j int) bool {
			return buckets[li][i].OccurredAt.Before(buckets[li][j].OccurredAt)
		})
	}

	// Step 4: compute X spread. Use real time mapping when the global
	// range is wider than the minimum even-spacing for the densest lane;
	// otherwise fall back to even-spacing per lane.
	var minT, maxT time.Time
	for _, e := range entries {
		if e.Type == domain.EntryTypeGoal {
			continue
		}
		if e.OccurredAt.IsZero() {
			continue
		}
		if minT.IsZero() || e.OccurredAt.Before(minT) {
			minT = e.OccurredAt
		}
		if maxT.IsZero() || e.OccurredAt.After(maxT) {
			maxT = e.OccurredAt
		}
	}
	maxBucketLen := 0
	for _, b := range buckets {
		if len(b) > maxBucketLen {
			maxBucketLen = len(b)
		}
	}
	width := MarginLeft + MarginRight + float64(maxBucketLen+1)*NodeMinSpacing + GoalNodeW
	if width < MinCanvasW {
		width = MinCanvasW
	}
	usableW := width - MarginLeft - MarginRight - GoalNodeW
	useTimeAxis := !minT.IsZero() && !maxT.IsZero() && maxT.Sub(minT) >= 5*time.Second

	height := MarginTop + float64(len(lanes))*LaneHeight + MarginBottom
	if height < 240 {
		height = 240
	}

	// Lane Y + goal X positions.
	for i := range lanes {
		lanes[i].Y = MarginTop + float64(i)*LaneHeight + LaneHeight/2
		lanes[i].GoalX = width - MarginRight - GoalNodeW
		lanes[i].GoalW = GoalNodeW
		lanes[i].GoalH = GoalNodeH
	}

	nodes := make([]Node, 0, len(entries))

	// Place goals at the right end of their lane.
	for li, l := range lanes {
		if l.Goal == nil {
			continue
		}
		nodes = append(nodes, Node{
			Entry:      *l.Goal,
			X:          l.GoalX,
			Y:          l.Y - GoalNodeH/2,
			R:          0, // goals use rect, not dot
			LaneIndex:  li,
			IsGoal:     true,
			ColorIndex: l.ColorIndex,
		})
	}

	// Place entries within their lane.
	for li, bucket := range buckets {
		if len(bucket) == 0 {
			continue
		}
		laneY := lanes[li].Y
		colourIdx := lanes[li].ColorIndex
		if useTimeAxis {
			totalNs := maxT.Sub(minT).Nanoseconds()
			if totalNs == 0 {
				totalNs = 1
			}
			for _, e := range bucket {
				frac := float64(e.OccurredAt.Sub(minT).Nanoseconds()) / float64(totalNs)
				x := MarginLeft + frac*usableW
				nodes = append(nodes, Node{
					Entry:      e,
					X:          x,
					Y:          laneY,
					R:          DotRadius,
					LaneIndex:  li,
					ColorIndex: colourIdx,
				})
			}
		} else {
			step := usableW / float64(len(bucket)+1)
			for i, e := range bucket {
				nodes = append(nodes, Node{
					Entry:      e,
					X:          MarginLeft + float64(i+1)*step,
					Y:          laneY,
					R:          DotRadius,
					LaneIndex:  li,
					ColorIndex: colourIdx,
				})
			}
		}
	}

	// Step 5: cross-lane edges. Skip same-lane part_of (the lane line
	// itself says "these belong here"). Everything else becomes a bezier
	// curve between the two endpoint coordinates.
	posByID := make(map[string]*Node, len(nodes))
	for i := range nodes {
		posByID[nodes[i].Entry.ID] = &nodes[i]
	}
	edgePaths := make([]EdgePath, 0, len(edges))
	for _, e := range edges {
		fn, ok := posByID[e.FromID]
		if !ok {
			continue
		}
		tn, ok := posByID[e.ToID]
		if !ok {
			continue
		}
		if e.Relation == domain.RelationPartOf && fn.LaneIndex == tn.LaneIndex {
			continue
		}
		midX := (fn.X + tn.X) / 2
		midY := (fn.Y + tn.Y) / 2
		if fn.LaneIndex == tn.LaneIndex {
			// Same lane non-part_of edge — bow it above the lane line.
			midY -= 24
		} else {
			// Cross-lane — bow it toward the midpoint already (no extra lift).
		}
		edgePaths = append(edgePaths, EdgePath{
			Edge:  e,
			FromX: fn.X, FromY: fn.Y,
			ToX: tn.X, ToY: tn.Y,
			MidX: midX, MidY: midY,
			Style: string(e.Relation),
		})
	}

	return Canvas{
		Lanes:  lanes,
		Nodes:  nodes,
		Edges:  edgePaths,
		Width:  width,
		Height: height,
	}
}
