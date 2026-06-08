package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// createTypes enumerates the entry types creatable from the Web UI. We
// deliberately omit activity (synthesised by materialize, not user-created)
// and discuss (Phase 2 conversation-source territory). Order matches the
// dogfooding workflow: think (research), decide (decision), commit
// (goal).
var createTypes = []domain.EntryType{
	domain.EntryTypeResearch,
	domain.EntryTypeDecision,
	domain.EntryTypeGoal,
}

// handleNewForm renders the empty create form for a given entry type.
// Route: GET /new/{type}
func (s *Server) handleNewForm(w http.ResponseWriter, r *http.Request) {
	typ := domain.EntryType(r.PathValue("type"))
	if !isCreatableType(typ) {
		http.Error(w, "unknown type: "+string(typ), http.StatusBadRequest)
		return
	}
	s.render(w, "new_entry.html", newEntryData{
		Title: "workingbad — new " + string(typ),
		Type:  typ,
	})
}

// handleNewSubmit accepts the form POST and creates the entry. Mirrors
// the CLI's note/decision/goal commands so engineers get the same shape
// of write through either surface.
// Route: POST /new/{type}
func (s *Server) handleNewSubmit(w http.ResponseWriter, r *http.Request) {
	typ := domain.EntryType(r.PathValue("type"))
	if !isCreatableType(typ) {
		http.Error(w, "unknown type: "+string(typ), http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	if title == "" {
		// Re-render the form with the user's body preserved + an error.
		s.render(w, "new_entry.html", newEntryData{
			Title:   "workingbad — new " + string(typ),
			Type:    typ,
			Body:    body,
			FormErr: "title is required",
		})
		return
	}

	status := domain.Status("")
	if typ == domain.EntryTypeGoal {
		status = domain.StatusOpen
	}

	e, err := s.svc.InsertEntry(r.Context(), domain.Entry{
		Type:      typ,
		Title:     title,
		Body:      body,
		Source:    domain.SourceManual,
		SourceRef: sha256Hex(typ, title, body),
		Origin:    domain.OriginLocal,
		Status:    status,
	})
	if err != nil {
		// Validator errors (e.g. body too long if we ever add a cap) come
		// back here. Surface them inline rather than 500ing.
		s.render(w, "new_entry.html", newEntryData{
			Title:   "workingbad — new " + string(typ),
			Type:    typ,
			TitleIn: title,
			Body:    body,
			FormErr: err.Error(),
		})
		return
	}

	// Goals land on the goal detail page (where status changes happen);
	// other types land on the entry detail page.
	target := "/entries/" + e.ID
	if typ == domain.EntryTypeGoal {
		target = "/goals/" + e.ID
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

type newEntryData struct {
	Title   string
	Type    domain.EntryType
	TitleIn string
	Body    string
	FormErr string
}

func isCreatableType(t domain.EntryType) bool {
	for _, k := range createTypes {
		if t == k {
			return true
		}
	}
	return false
}

// sha256Hex mirrors cmd/workingbad/commands.go's helper so manual entries
// created via either CLI or Web UI share the same source_ref derivation
// (same title+body → same hash → idempotent create-dedupe).
func sha256Hex(typ domain.EntryType, title, body string) string {
	h := sha256.New()
	h.Write([]byte(string(typ)))
	h.Write([]byte{0})
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}
