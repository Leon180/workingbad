package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// labelsBody is the request shape both content types reduce to. JSON
// callers send {"labels": [...]} explicitly; form callers send any
// number of repeating `labels=<value>` fields (htmx checkbox lists).
type labelsBody struct {
	Labels []string `json:"labels"`
}

// handleGetLabels returns the secondary-label set for the entry as a
// JSON array, alphabetical. Empty for entries with no labels — never
// 404 for "no labels" (use the dedicated Not Found mapping for unknown
// entries).
//
// Route: GET /entries/{id}/labels
func (s *Server) handleGetLabels(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	if entryID == "" {
		http.Error(w, "missing entry id", http.StatusBadRequest)
		return
	}

	labels, err := s.svc.GetLabels(r.Context(), entryID)
	if err != nil {
		http.Error(w, fmt.Sprintf("labels: %v", err), statusFor(err))
		return
	}
	// Empty slice → []; nil → []. Either way JSON serialises to a real
	// array, not "null", so the UI can unconditionally call .map().
	if labels == nil {
		labels = []domain.EntryType{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(labels); err != nil {
		// Log-level only; the response is already partially written.
		return
	}
}

// handleSetLabels replaces the secondary-label set for the entry.
// Accepts:
//
//	Content-Type: application/json
//	{"labels": ["decision", "research"]}
//
//	Content-Type: application/x-www-form-urlencoded
//	labels=decision&labels=research
//
// Empty input clears all labels. Returns the new set as JSON (same
// shape as GET). Mapped errors:
//
//	400 — bad body, invalid label, label==entry primary type, duplicates
//	404 — unknown / superseded entry
//
// Route: POST /entries/{id}/labels
func (s *Server) handleSetLabels(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	if entryID == "" {
		http.Error(w, "missing entry id", http.StatusBadRequest)
		return
	}

	rawLabels, err := readLabelsBody(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad body: %v", err), http.StatusBadRequest)
		return
	}

	labels := make([]domain.EntryType, 0, len(rawLabels))
	for _, l := range rawLabels {
		labels = append(labels, domain.EntryType(strings.TrimSpace(l)))
	}

	if err := s.svc.SetLabels(r.Context(), entryID, labels); err != nil {
		http.Error(w, fmt.Sprintf("set labels: %v", err), statusFor(err))
		return
	}

	// Read back so the response matches what's actually persisted (the
	// validator may have re-ordered or applied future invariants we
	// haven't surfaced yet). Costs one extra SELECT; worth it for the
	// honest "what did the server keep?" round-trip.
	persisted, err := s.svc.GetLabels(r.Context(), entryID)
	if err != nil {
		http.Error(w, fmt.Sprintf("read back: %v", err), statusFor(err))
		return
	}
	if persisted == nil {
		persisted = []domain.EntryType{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(persisted); err != nil {
		return
	}
}

// readLabelsBody decodes either content type into the same string list.
// Empty body → empty slice (clear-all is a legitimate operation).
func readLabelsBody(r *http.Request) ([]string, error) {
	ct := r.Header.Get("Content-Type")
	// Strip params like "; charset=utf-8" before comparing.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/json":
		var body labelsBody
		// Empty body decodes to zero-value labelsBody{Labels: nil},
		// which translates downstream to "clear all". Reject only
		// malformed JSON (mid-stream syntax errors).
		if r.ContentLength == 0 {
			return nil, nil
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			return nil, err
		}
		return body.Labels, nil
	case "application/x-www-form-urlencoded", "":
		// "" is the htmx default when the form has no `enctype` set.
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return r.PostForm["labels"], nil
	default:
		return nil, fmt.Errorf("unsupported content-type: %q", ct)
	}
}
