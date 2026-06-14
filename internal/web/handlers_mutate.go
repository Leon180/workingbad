package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

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
		http.Error(w, fmt.Sprintf("set status: %v", err), statusFor(err))
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
		http.Error(w, fmt.Sprintf("attach: %v", err), statusFor(err))
		return
	}
	http.Redirect(w, r, "/goals/"+goalID, http.StatusSeeOther)
}

// handleMaterialize processes all pending segments via the mock AIProvider.
// Phase 1 only: the real local/api providers arrive in Phase 3. Triggered
// from the header CTA that appears whenever the pending count is > 0.
//
// Route: POST /materialize
//
// Redirect target: the Referer header, normalised through safeRedirectPath
// so an attacker-supplied Referer can't turn this into an open redirect.
// Flash params (materialized/failed counts) are appended via net/url so
// any embedded query string in the (already-validated) Referer round-trips
// safely.
func (s *Server) handleMaterialize(w http.ResponseWriter, r *http.Request) {
	// Phase 1 ships the deterministic mock summarizer — same instance the
	// CLI's `summarize` uses, so behaviour is identical across surfaces.
	provider := mock.New()

	res, err := s.svc.BatchMaterialize(r.Context(), repository.MaterializeScope{}, provider)
	if err != nil {
		http.Error(w, fmt.Sprintf("materialize: %v", err), http.StatusInternalServerError)
		return
	}
	// Partial failures only surface as "failed=N" in the flash; log the actual
	// per-segment reasons so an operator can find out what went wrong.
	for _, e := range res.Errors {
		slog.WarnContext(r.Context(), "materialize segment failed", "err", e)
	}

	target := safeRedirectPath(r, r.Header.Get("Referer"), "/")
	target = appendFlash(target, res.Materialized, res.Failed)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// appendFlash adds materialized=N&failed=M to target's query string using
// net/url so existing params are preserved and special chars escaped.
func appendFlash(target string, materialized, failed int) string {
	u, err := url.Parse(target)
	if err != nil {
		// safeRedirectPath gave us this, so a parse error here is internal
		// confusion — fall back to root + flash.
		return fmt.Sprintf("/?materialized=%d&failed=%d", materialized, failed)
	}
	q := u.Query()
	q.Set("materialized", strconv.Itoa(materialized))
	q.Set("failed", strconv.Itoa(failed))
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// handleEdgeDetach marks a live part_of edge as detached.
// Route: POST /edges/{id}/detach
//
// The form's hidden `goal_id` field tells us where to redirect after.
// goal_id is REQUIRED (silent fallback to "/" was hiding template bugs
// — the form always carries it, missing means something is wrong), and
// it must be UUID-shaped before we embed it in a redirect path (raw
// form input previously made "/goals/../../etc/passwd" → /etc/passwd
// via browser normalisation).
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
	if goalID == "" {
		http.Error(w, "goal_id is required", http.StatusBadRequest)
		return
	}

	if err := s.svc.DetachFromGoal(r.Context(), edgeID); err != nil {
		http.Error(w, fmt.Sprintf("detach: %v", err), statusFor(err))
		return
	}

	http.Redirect(w, r, safeGoalRedirect(goalID, "/"), http.StatusSeeOther)
}
