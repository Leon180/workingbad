package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// handleEntryDetail renders one entry plus its full supersede chain
// (EntryHistory). The header shows the live version; the chain table
// underneath shows every prior version with side-by-side occurred / ingested
// timestamps — the bitemporal "git log <file>" equivalent.
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

	s.render(w, "entry_detail.html", entryDetailData{
		Title:   "workingbad — " + live.Title,
		Live:    live,
		History: history,
	})
}

// handleGoalDetail renders the live goal plus its attached activities
// (via repository.GetGoalActivities, which already walks the goal's
// LogicalID chain so status changes don't break attachment).
//
// Route: GET /goals/{id}
func (s *Server) handleGoalDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	_, live, err := s.resolveLogical(r.Context(), id)
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

	activities, err := s.svc.GetGoalActivities(r.Context(), live.ID)
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	s.render(w, "goal_detail.html", goalDetailData{
		Title:      "workingbad — " + live.Title,
		Goal:       live,
		Activities: activities,
	})
}

type entryDetailData struct {
	Title   string
	Live    domain.Entry
	History []domain.Entry
}

type goalDetailData struct {
	Title      string
	Goal       domain.Entry
	Activities []domain.Entry
}

// errNotFound is the sentinel resolveLogical returns when neither lookup
// path finds the entry. Handlers map this to 404 cleanly without parsing
// error strings.
var errNotFound = errors.New("web: entry not found")

// resolveLogical accepts either an entry id or a logical_id and returns
// (logicalID, liveEntry). EntryHistory takes a logical_id by contract,
// so we try that first; if it comes up empty we treat the input as an
// entry id and discover the chain via a scan of ListEntries. Both fast
// paths are O(history depth) which is bounded by supersede count.
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

	// Fallback: scan live entries for one with this id, then walk its chain.
	// 1000 = the same hard cap parseLimit enforces. Acceptable for Phase 1
	// row counts; a dedicated SQL lookup is a straightforward additive
	// improvement when entry counts grow past this.
	all, err := s.svc.ListEntries(ctx, repository.ListFilter{Limit: 1000})
	if err != nil {
		return "", domain.Entry{}, err
	}
	for _, e := range all {
		if e.ID == id {
			return e.LogicalID, e, nil
		}
	}
	return "", domain.Entry{}, errNotFound
}
