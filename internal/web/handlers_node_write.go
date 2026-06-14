package web

import (
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
	status := domain.Status("")
	if typ == domain.EntryTypeGoal {
		status = parseGoalStatus(r.FormValue("status"), domain.StatusOpen)
	}

	render := func(msg string) {
		s.renderPage(w, r, "node_form.html", nodeFormData{
			Title: "workingbad — new node", Heading: "new " + string(typ) + " node",
			Action: "/nodes", Type: typ, TitleIn: title, Body: body, Status: status,
			FormErr: msg,
		})
	}
	if title == "" {
		render("title is required")
		return
	}
	n, err := s.svc.CreateNode(r.Context(), domain.Node{
		Type: typ, Title: title, Body: body, Status: status,
	})
	if err != nil {
		render(err.Error())
		return
	}
	http.Redirect(w, r, "/nodes/"+n.ID, http.StatusSeeOther)
}

// GET /nodes/{id}/edit — always edits the LIVE version of the chain, even if
// {id} is a superseded version (stale bookmark).
func (s *Server) handleNodeEditForm(w http.ResponseWriter, r *http.Request) {
	live, err := s.liveNodeFor(r, r.PathValue("id"))
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
func (s *Server) handleNodeUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	old, err := s.svc.GetNode(r.Context(), r.PathValue("id"))
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
	status := old.Status
	if old.Type == domain.EntryTypeGoal {
		status = parseGoalStatus(r.FormValue("status"), old.Status)
	}
	expected := parseVersion(r.FormValue("expected_version"))
	replacement := domain.Node{Type: old.Type, Title: title, Body: body, Status: status}

	if title == "" {
		d := editFormFor(old, "title is required")
		d.TitleIn, d.Body, d.Status = title, body, status // keep the user's edits
		s.renderPage(w, r, "node_form.html", d)
		return
	}

	updated, err := s.svc.SupersedeNode(r.Context(), old.ID, expected, replacement)
	switch {
	case errors.Is(err, repository.ErrVersionConflict):
		// Someone edited between form-open and submit. Show the live version so
		// the user re-applies intentionally rather than clobbering.
		live, lerr := s.svc.GetLiveNodeByLogicalID(r.Context(), old.LogicalID)
		if lerr != nil {
			http.Error(w, "repository: "+lerr.Error(), http.StatusInternalServerError)
			return
		}
		d := editFormFor(live, "this node changed since you opened the form — showing the latest; re-apply your edit")
		d.Conflict = true
		s.renderPage(w, r, "node_form.html", d)
		return
	case errors.Is(err, repository.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, repository.ErrInvalidInput):
		d := editFormFor(old, err.Error())
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
func (s *Server) liveNodeFor(r *http.Request, id string) (domain.Node, error) {
	n, err := s.svc.GetNode(r.Context(), id)
	if err != nil {
		return domain.Node{}, err
	}
	return s.svc.GetLiveNodeByLogicalID(r.Context(), n.LogicalID)
}

func editFormFor(n domain.Node, msg string) nodeFormData {
	return nodeFormData{
		Title: "workingbad — edit node", Heading: "edit " + string(n.Type) + " node",
		Action: "/nodes/" + n.ID, Type: n.Type,
		TitleIn: n.Title, Body: n.Body, Status: n.Status,
		Version: n.Version, IsEdit: true, FormErr: msg,
	}
}

func parseGoalStatus(s string, def domain.Status) domain.Status {
	switch domain.Status(s) {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusDone, domain.StatusArchived:
		return domain.Status(s)
	default:
		return def
	}
}

func parseVersion(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
