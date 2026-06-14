package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// Manual node operations (Slice D2f-2) — v1.0.0 goal #3, "LLM 以外，在本地能夠
// 人工修正歷史和進度". Create + edit nodes by hand. Edit = supersede with an
// optimistic lock (expected_version) so a concurrent edit can't be silently
// clobbered. All mutations route through RepositoryService.CreateNode /
// SupersedeNode (the sole write gate). CSRF is the mux's CrossOriginProtection
// (Go 1.25, same-origin enforced) — no token field needed.

// nodeCreateTypes mirrors the entry create set (curate: think → decide → commit).
var nodeCreateTypes = []domain.EntryType{
	domain.EntryTypeResearch,
	domain.EntryTypeDecision,
	domain.EntryTypeGoal,
}

func isNodeCreatableType(t domain.EntryType) bool {
	for _, k := range nodeCreateTypes {
		if t == k {
			return true
		}
	}
	return false
}

type nodeFormData struct {
	Title    string // page <title>
	Heading  string // form heading
	Action   string // form POST target
	Type     domain.EntryType
	TitleIn  string
	Body     string
	Status   domain.Status
	Version  int // expected_version for edit (0 on create)
	IsEdit   bool
	Conflict bool
	FormErr  string
}

// GET /nodes/new (?type=research|decision|goal)
func (s *Server) handleNodeNewForm(w http.ResponseWriter, r *http.Request) {
	typ := domain.EntryType(r.URL.Query().Get("type"))
	if typ == "" {
		typ = domain.EntryTypeResearch
	}
	if !isNodeCreatableType(typ) {
		http.Error(w, "unknown type: "+string(typ), http.StatusBadRequest)
		return
	}
	status := domain.Status("")
	if typ == domain.EntryTypeGoal {
		status = domain.StatusOpen
	}
	s.renderPage(w, r, "node_form.html", nodeFormData{
		Title: "workingbad — new node", Heading: "new " + string(typ) + " node",
		Action: "/nodes", Type: typ, Status: status,
	})
}

// POST /nodes
func (s *Server) handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	typ := domain.EntryType(strings.TrimSpace(r.FormValue("type")))
	if !isNodeCreatableType(typ) {
		http.Error(w, "unknown type: "+string(typ), http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	status := domain.Status("") // non-goal types carry no status

	render := func(msg string) {
		s.renderPage(w, r, "node_form.html", nodeFormData{
			Title: "workingbad — new node", Heading: "new " + string(typ) + " node",
			Action: "/nodes", Type: typ, TitleIn: title, Body: body, Status: status,
			FormErr: msg,
		})
	}

	if typ == domain.EntryTypeGoal {
		st, ok := parseGoalStatus(r.FormValue("status"), domain.StatusOpen)
		if !ok {
			render("invalid status (expected open/in_progress/done/archived)")
			return
		}
		status = st
	}
	if title == "" {
		render("title is required")
		return
	}
	n, err := s.svc.CreateNode(r.Context(), domain.Node{
		Type: typ, Title: title, Body: body, Status: status,
	})
	if err != nil {
		// Validation errors re-render the form; infrastructure errors must
		// surface as 5xx so monitoring sees them (not a 200 success-looking page).
		if errors.Is(err, repository.ErrInvalidInput) {
			render(err.Error())
			return
		}
		http.Error(w, "repository: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/nodes/"+n.ID, http.StatusSeeOther)
}

// GET /nodes/{id}/edit — always edits the LIVE version of the chain, even if
// {id} is a superseded version (stale bookmark).
func (s *Server) handleNodeEditForm(w http.ResponseWriter, r *http.Request) {
	live, err := s.liveNodeFor(r.Context(), r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "repository: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPage(w, r, "node_form.html", editFormFor(live, ""))
}

// POST /nodes/{id} — edit = supersede with an optimistic lock.
//
// The edit always targets the LIVE head of the chain (the form may post to a
// version id that a concurrent edit has since superseded). expected_version is
// the lock: if the live head's version no longer matches what the form
// rendered, we re-render with the latest version + a "re-apply" banner instead
// of clobbering. A genuine 404 is only when the node/chain doesn't exist at
// all; any concurrency race (version moved, head superseded mid-flight) is a
// conflict, never a silent overwrite.
func (s *Server) handleNodeUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// GetNode(pathID) only to resolve the chain's logical_id; the path id may be
	// a superseded version. ErrNotFound here = the id never existed → true 404.
	ref, err := s.svc.GetNode(ctx, r.PathValue("id"))
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "repository: "+err.Error(), http.StatusInternalServerError)
		return
	}
	live, err := s.svc.GetLiveNodeByLogicalID(ctx, ref.LogicalID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "repository: "+err.Error(), http.StatusInternalServerError)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	// status only applies to goal nodes; the field is intentionally ignored for
	// other types (they have no status concept).
	status := live.Status
	if live.Type == domain.EntryTypeGoal {
		st, ok := parseGoalStatus(r.FormValue("status"), live.Status)
		if !ok {
			d := editFormFor(live, "invalid status (expected open/in_progress/done/archived)")
			d.TitleIn, d.Body = title, body
			s.renderPage(w, r, "node_form.html", d)
			return
		}
		status = st
	}
	expected := parseVersion(r.FormValue("expected_version"))

	// renderConflict reloads the freshest live version (it may have moved again)
	// and shows the re-apply banner — the single path for every concurrency race.
	renderConflict := func() {
		fresh, ferr := s.svc.GetLiveNodeByLogicalID(ctx, ref.LogicalID)
		if ferr != nil {
			http.Error(w, "repository: "+ferr.Error(), http.StatusInternalServerError)
			return
		}
		d := editFormFor(fresh, "this node changed since you opened the form — showing the latest; re-apply your edit")
		d.Conflict = true
		s.renderPage(w, r, "node_form.html", d)
	}

	if title == "" {
		d := editFormFor(live, "title is required")
		d.TitleIn, d.Body, d.Status = title, body, status // keep the user's edits
		s.renderPage(w, r, "node_form.html", d)
		return
	}
	// Explicit version check up front: catches the stale-form case (and a
	// missing/zero expected_version, which would otherwise skip SupersedeNode's
	// `expectedVersion > 0` lock) as a conflict rather than a silent overwrite.
	if expected != live.Version {
		renderConflict()
		return
	}

	updated, err := s.svc.SupersedeNode(ctx, live.ID, expected,
		domain.Node{Type: live.Type, Title: title, Body: body, Status: status})
	switch {
	case errors.Is(err, repository.ErrVersionConflict), errors.Is(err, repository.ErrNotFound):
		// A concurrent edit landed in the TOCTOU window (version moved, or the
		// head we read was just superseded) → conflict, not a 404/overwrite.
		renderConflict()
		return
	case errors.Is(err, repository.ErrInvalidInput):
		d := editFormFor(live, err.Error())
		d.TitleIn, d.Body, d.Status = title, body, status
		s.renderPage(w, r, "node_form.html", d)
		return
	case err != nil:
		http.Error(w, "repository: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/nodes/"+updated.ID, http.StatusSeeOther)
}

// liveNodeFor resolves any node id (live or superseded) to the live head of its
// chain, so edits always target the current version.
func (s *Server) liveNodeFor(ctx context.Context, id string) (domain.Node, error) {
	n, err := s.svc.GetNode(ctx, id)
	if err != nil {
		return domain.Node{}, err
	}
	return s.svc.GetLiveNodeByLogicalID(ctx, n.LogicalID)
}

func editFormFor(n domain.Node, msg string) nodeFormData {
	return nodeFormData{
		Title: "workingbad — edit node", Heading: "edit " + string(n.Type) + " node",
		Action: "/nodes/" + n.ID, Type: n.Type,
		TitleIn: n.Title, Body: n.Body, Status: n.Status,
		Version: n.Version, IsEdit: true, FormErr: msg,
	}
}

// parseGoalStatus validates a goal status form value. An empty value takes the
// default (open on create, the current status on edit); a non-empty value must
// be a known status, else ok=false so the caller can reject it rather than
// silently coercing a tampered/garbage value to the default.
func parseGoalStatus(raw string, def domain.Status) (status domain.Status, ok bool) {
	if raw == "" {
		return def, true
	}
	switch domain.Status(raw) {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusDone, domain.StatusArchived:
		return domain.Status(raw), true
	default:
		return "", false
	}
}

func parseVersion(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
