package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// handleEntryDetail renders one entry plus its full supersede chain
// (EntryHistory). The header shows the live version; the chain table
// underneath shows every prior version with side-by-side occurred / ingested
// timestamps — the bitemporal "git log <file>" equivalent.
//
// With ?at=<RFC3339>: the header switches to the version that was live at
// that instant, and the chain table marks that row as the "as of T"
// snapshot. The full history still renders (it's useful audit context),
// just with the focal version shifted.
//
// Route: GET /entries/{id}
// Resolves either an entry id (any version) or a logical_id directly —
// engineers pasting either should work.
func (s *Server) handleEntryDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	logical, live, err := s.resolveLogical(r.Context(), id)
	if err != nil {
		if errors.Is(err, errNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	history, err := s.svc.EntryHistory(r.Context(), logical)
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	atStr := r.URL.Query().Get("at")
	asOf, asOfErr := parseAtParam(atStr)
	focal := live
	focalID := live.ID
	if atStr != "" && asOfErr == nil {
		// In time-travel mode the focal version is whichever entry was
		// "live at asOf" — its ingested_at <= asOf and any successor
		// arrived after asOf. liveAtAsOf does the walk on the already-
		// loaded history (no extra SQL round-trip).
		if v, ok := liveAtAsOf(history, asOf); ok {
			focal = v
			focalID = v.ID
		}
	}

	s.renderPage(w, r, "entry_detail.html", entryDetailData{
		Title:      "workingbad — " + focal.Title,
		Live:       focal,
		History:    history,
		FocalID:    focalID,
		ActiveAt:   atStr,
		AsOf:       asOf,
		AtParseErr: asOfErrString(atStr, asOfErr),
	})
}

// handleGoalDetail renders the live goal plus its attached activities.
//
// Without ?at=: uses GetGoalActivities (walks the goal's LogicalID chain
// so status changes don't break attachment).
//
// With ?at=<RFC3339>: uses GoalActivitiesAt(goalID, asOf) and shifts the
// header to the goal version that was live at asOf. Mutation forms are
// suppressed (ReadOnly = true) — you can't supersede the past.
//
// Route: GET /goals/{id}
func (s *Server) handleGoalDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	logical, live, err := s.resolveLogical(r.Context(), id)
	if err != nil {
		if errors.Is(err, errNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}
	if live.Type != domain.EntryTypeGoal {
		http.Error(w, "not a goal", http.StatusBadRequest)
		return
	}

	atStr := r.URL.Query().Get("at")
	asOf, asOfErr := parseAtParam(atStr)
	timeTravel := atStr != "" && asOfErr == nil

	// In time-travel mode, shift the focal goal version to whichever was
	// live at asOf. We load the chain once and walk it in-memory; the
	// header data + the GoalActivitiesAt call both key off the focal id.
	focal := live
	if timeTravel {
		chain, herr := s.svc.EntryHistory(r.Context(), logical)
		if herr != nil {
			http.Error(w, fmt.Sprintf("repository: %v", herr), http.StatusInternalServerError)
			return
		}
		if v, ok := liveAtAsOf(chain, asOf); ok && v.Type == domain.EntryTypeGoal {
			focal = v
		}
	}

	var activities []domain.Entry
	if timeTravel {
		activities, err = s.svc.GoalActivitiesAt(r.Context(), focal.ID, asOf)
	} else {
		activities, err = s.svc.GetGoalActivities(r.Context(), focal.ID)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	// Each attached activity comes with the edge id that links it, so the
	// detail page can show a one-click detach button per row in live mode.
	// In time-travel mode collectAttached is skipped (no detach action
	// available) so we avoid the EdgesAt round-trips entirely.
	var attached []attachedActivity
	if !timeTravel {
		attached, err = s.collectAttached(r.Context(), focal, activities)
		if err != nil {
			http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		attached = make([]attachedActivity, 0, len(activities))
		for _, a := range activities {
			attached = append(attached, attachedActivity{Activity: a})
		}
	}

	s.renderPage(w, r, "goal_detail.html", goalDetailData{
		Title:         "workingbad — " + focal.Title,
		Goal:          focal,
		Attached:      attached,
		StatusOptions: goalStatusOptions,
		ActiveAt:      atStr,
		AsOf:          asOf,
		AtParseErr:    asOfErrString(atStr, asOfErr),
		ReadOnly:      timeTravel,
	})
}

// goalStatusOptions drives the status dropdown on the goal detail page.
// Order matches typical workflow (open → in_progress → done; archived as
// a terminal sink).
var goalStatusOptions = []domain.Status{
	domain.StatusOpen,
	domain.StatusInProgress,
	domain.StatusDone,
	domain.StatusArchived,
}

// attachedActivity bundles an activity with the live edge id that links
// it, so the template can render a per-row detach button.
type attachedActivity struct {
	Activity domain.Entry
	EdgeID   string
}

// collectAttached pairs each activity returned by GetGoalActivities with
// its live part_of edge id. One EdgesAt round-trip per page render
// (issue #12): fetch every part_of edge pointing at this goal once,
// index by from_id, then look up per activity in Go.
func (s *Server) collectAttached(ctx context.Context, goal domain.Entry, activities []domain.Entry) ([]attachedActivity, error) {
	edges, err := s.svc.EdgesAt(ctx, time.Now().UTC(),
		repository.EdgeFilter{Relation: domain.RelationPartOf, ToID: goal.ID})
	if err != nil {
		return nil, err
	}
	// from_id -> live edge id. The EdgesAt result is bounded by the
	// number of part_of edges into this single goal, so the map cost
	// is O(edges) for the build and O(1) per activity lookup.
	byFrom := make(map[string]string, len(edges))
	for _, e := range edges {
		if !e.IsCurrent {
			continue
		}
		if _, seen := byFrom[e.FromID]; seen {
			continue
		}
		byFrom[e.FromID] = e.ID
	}
	out := make([]attachedActivity, 0, len(activities))
	for _, a := range activities {
		out = append(out, attachedActivity{Activity: a, EdgeID: byFrom[a.ID]})
	}
	return out, nil
}

type entryDetailData struct {
	Title      string
	Live       domain.Entry // focal version (= "live at asOf" in time-travel mode, else the current row)
	History    []domain.Entry
	FocalID    string    // id of the focal row, so the chain table can highlight it
	ActiveAt   string    // raw ?at= query value, echoed for forms / back-link
	AsOf       time.Time // parsed asOf, zero = live mode
	AtParseErr string    // empty when ActiveAt is "" or parsed OK
}

type goalDetailData struct {
	Title         string
	Goal          domain.Entry
	Attached      []attachedActivity
	StatusOptions []domain.Status
	ActiveAt      string
	AsOf          time.Time
	AtParseErr    string
	ReadOnly      bool // true in time-travel mode — mutation forms suppressed (can't edit the past)
}

// liveAtAsOf walks an EntryHistory chain (newest-first by version) and
// returns the version that was current at the given asOf instant. Pure
// in-memory predicate, no SQL — the caller already loaded the chain.
//
// Rule: an entry was current at asOf iff
//   - ingested_at <= asOf  (it existed by then)
//   - it has no successor OR the successor was ingested AFTER asOf (the
//     supersede had not happened yet at asOf)
//
// Same logic as the supersede-chain walk in queries_temporal.go's
// ListEntriesAt, just applied client-side to a known chain.
func liveAtAsOf(history []domain.Entry, asOf time.Time) (domain.Entry, bool) {
	for _, e := range history {
		if e.IngestedAt.After(asOf) {
			continue // didn't exist yet
		}
		if e.SupersededBy == "" {
			return e, true // tail of chain that was alive at asOf
		}
		// Look ahead in the chain (history is version-DESC, so the
		// successor lives at a lower index) — if it was ingested after
		// asOf, this version was still live then.
		successorAlive := false
		for _, s := range history {
			if s.ID == e.SupersededBy {
				if s.IngestedAt.After(asOf) {
					successorAlive = true
				}
				break
			}
		}
		if successorAlive {
			return e, true
		}
	}
	return domain.Entry{}, false
}

// errNotFound is the sentinel resolveLogical returns when neither lookup
// path finds the entry. Handlers map this to 404 cleanly without parsing
// error strings.
var errNotFound = errors.New("web: entry not found")

// resolveLogical accepts either an entry id or a logical_id and returns
// (logicalID, liveEntry). EntryHistory takes a logical_id by contract,
// so we try that first; if it comes up empty we look up the row by entry
// id via GetEntryLogicalIDByID — an indexed PK lookup, bounded regardless
// of total entry count. Pre-fix the fallback was a ListEntries scan
// capped at 1000 rows which silently 404'd past that limit (architect
// review #10 P1).
func (s *Server) resolveLogical(ctx context.Context, id string) (string, domain.Entry, error) {
	history, err := s.svc.EntryHistory(ctx, id)
	if err != nil {
		return "", domain.Entry{}, err
	}
	if len(history) > 0 {
		for _, e := range history {
			if e.IsCurrent {
				return e.LogicalID, e, nil
			}
		}
		return history[0].LogicalID, history[0], nil
	}

	// Treat input as an entry id and resolve to logical_id via PK lookup.
	logical, err := s.svc.GetEntryLogicalIDByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", domain.Entry{}, errNotFound
		}
		return "", domain.Entry{}, err
	}
	chain, err := s.svc.EntryHistory(ctx, logical)
	if err != nil {
		return "", domain.Entry{}, err
	}
	for _, e := range chain {
		if e.IsCurrent {
			return logical, e, nil
		}
	}
	if len(chain) > 0 {
		return logical, chain[0], nil
	}
	return "", domain.Entry{}, errNotFound
}
