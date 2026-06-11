package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
	"github.com/Leon180/workingbad/internal/web/layout"
)

// handleGraph renders the knowledge graph at /graph. Goals pin to the
// right, every other entry flows from the left toward whichever goal
// it's part_of. Click a node → /entries/{id} (or /goals/{id} for goals).
//
// Phase-1 scope (grill: docs/grill/2026-06-09-graph-ui.md):
//   - sink-anchored hierarchical layout
//   - 5 type colours + shape
//   - part_of as the main backbone, other relations toggled off by
//     default (next PR adds the legend + filter)
//   - click drill-in only; no pan/zoom / no detail panel yet
//
// Filters today: ?repo= (carries through to the list view's existing
// filter) and ?at= (time-travel, reuses parseAtParam from the list
// handler). Type filter intentionally absent — a graph filtered to one
// type collapses to a column with no edges, which is just the list
// view in a worse shape.
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repo")
	atStr := r.URL.Query().Get("at")
	asOf, asOfErr := parseAtParam(atStr)
	timeTravel := atStr != "" && asOfErr == nil

	var (
		entries []domain.Entry
		err     error
	)
	if timeTravel {
		entries, err = s.svc.ListEntriesAt(r.Context(), asOf,
			repository.ListFilter{RepoID: repoID, Limit: 1000})
	} else {
		entries, err = s.svc.ListEntries(r.Context(),
			repository.ListFilter{RepoID: repoID, Limit: 1000})
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	// Pull live (or as-of) edges. EdgesAt with now = "live edges right
	// now" — same query layer powering Slice B's bitemporal contract.
	edgeAsOf := asOf
	if !timeTravel {
		edgeAsOf = time.Now().UTC()
	}
	edges, err := s.svc.EdgesAt(r.Context(), edgeAsOf, repository.EdgeFilter{})
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	// Timeline density (px-per-day) comes from ?ppd= so the client's zoom
	// control can re-request at a new density via htmx; BuildTimeline clamps
	// it. The graph lands at DefaultPxPerDay, then the client measures the
	// viewport and fits-to-width on load.
	canvas := layout.BuildTimeline(entries, edges, parsePpd(r.URL.Query().Get("ppd")))

	s.renderPage(w, r, "graph.html", graphData{
		Title:           "workingbad — graph",
		Canvas:          canvas,
		ActiveAt:        atStr,
		AsOf:            asOf,
		AtParseErr:      asOfErrString(atStr, asOfErr),
		ActiveRepo:      repoID,
		EmptyHint:       len(entries) == 0,
		MaterializedNum: r.URL.Query().Get("materialized"),
		FailedNum:       r.URL.Query().Get("failed"),
	})
}

// parsePpd reads the px-per-day zoom density from the query string. Empty or
// unparseable falls back to the default; layout.BuildTimeline clamps range.
func parsePpd(s string) float64 {
	if s == "" {
		return layout.DefaultPxPerDay
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return layout.DefaultPxPerDay
	}
	return v
}

type graphData struct {
	Title           string
	Canvas          layout.Canvas
	ActiveAt        string
	AsOf            time.Time
	AtParseErr      string
	ActiveRepo      string
	EmptyHint       bool
	MaterializedNum string // ?materialized=N flash after /materialize POST → toast
	FailedNum       string // ?failed=N companion to MaterializedNum
}
