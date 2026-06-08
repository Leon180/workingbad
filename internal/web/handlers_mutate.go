package web

import (
	"fmt"
	"net/http"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// handleGoalStatus changes a goal's status by superseding it. New version
// lives at a fresh id; we redirect to the new live row's goal-detail page.
// Route: POST /goals/{id}/status
//
// Form fields:
//
//	status — one of open/in_progress/done/archived (required)
func (s *Server) handleGoalStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing goal id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	status := domain.Status(r.FormValue("status"))

	newGoal, err := s.svc.SetGoalStatus(r.Context(), id, status)
	if err != nil {
		http.Error(w, fmt.Sprintf("set status: %v", err), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/goals/"+newGoal.ID, http.StatusSeeOther)
}

// handleGoalAttach attaches an existing entry to the goal via a part_of
// edge. The form value `entry_id` is the activity (or any entry) to
// attach. AttachToGoal is idempotent — re-submitting the same pair
// returns the existing live edge.
// Route: POST /goals/{id}/attach
func (s *Server) handleGoalAttach(w http.ResponseWriter, r *http.Request) {
	goalID := r.PathValue("id")
	if goalID == "" {
		http.Error(w, "missing goal id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	entryID := r.FormValue("entry_id")
	if entryID == "" {
		http.Error(w, "entry_id is required", http.StatusBadRequest)
		return
	}

	if _, err := s.svc.AttachToGoal(r.Context(), entryID, goalID); err != nil {
		http.Error(w, fmt.Sprintf("attach: %v", err), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/goals/"+goalID, http.StatusSeeOther)
}

// handleMaterialize processes all pending segments via the mock AIProvider.
// Phase 1 only: the real local/api providers arrive in Phase 3. Triggered
// from the header CTA that appears whenever the pending count is > 0.
//
// Route: POST /materialize
func (s *Server) handleMaterialize(w http.ResponseWriter, r *http.Request) {
	// Phase 1 ships the deterministic mock summarizer — same instance the
	// CLI's `summarize` uses, so behaviour is identical across surfaces.
	provider := mock.New()

	res, err := s.svc.BatchMaterialize(r.Context(), repository.MaterializeScope{}, provider)
	if err != nil {
		http.Error(w, fmt.Sprintf("materialize: %v", err), http.StatusInternalServerError)
		return
	}
	// Redirect back to wherever the engineer was, falling back to /.
	target := r.Header.Get("Referer")
	if target == "" {
		target = "/"
	}
	// Encode the materialise result into a flash-style query param so the
	// landing page can show a brief success line without persistent state.
	// Cheap signalling — same one-shot pattern Web apps have used forever.
	target += joinSep(target) + fmt.Sprintf("materialized=%d&failed=%d", res.Materialized, res.Failed)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// joinSep returns "?" if u has no query string yet, otherwise "&". A
// minor formality but it keeps the redirect URL well-formed.
func joinSep(u string) string {
	for i := 0; i < len(u); i++ {
		if u[i] == '?' {
			return "&"
		}
	}
	return "?"
}

// handleEdgeDetach marks a live part_of edge as detached.
// Route: POST /edges/{id}/detach
//
// The form's hidden `goal_id` field tells us where to redirect after.
func (s *Server) handleEdgeDetach(w http.ResponseWriter, r *http.Request) {
	edgeID := r.PathValue("id")
	if edgeID == "" {
		http.Error(w, "missing edge id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	goalID := r.FormValue("goal_id")

	if err := s.svc.DetachFromGoal(r.Context(), edgeID); err != nil {
		http.Error(w, fmt.Sprintf("detach: %v", err), http.StatusBadRequest)
		return
	}

	target := "/"
	if goalID != "" {
		target = "/goals/" + goalID
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
