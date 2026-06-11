package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Leon180/workingbad/internal/domain"
)

// errUnsupportedContentType signals that readLabelsBody rejected the
// request's Content-Type. Caller maps it to 415; any other readLabelsBody
// error (malformed JSON, parse-form failure) is a 400.
var errUnsupportedContentType = errors.New("unsupported content-type")

// labelsBody is the request shape both content types reduce to. JSON
// callers send {"labels": [...]} explicitly; form callers send any
// number of repeating `labels=<value>` fields (htmx checkbox lists).
type labelsBody struct {
	Labels []string `json:"labels"`
}

// handleGetLabels returns the secondary-label set for the entry as a
// JSON array, alphabetical. Empty for entries that exist but carry no
// labels; 404 when the entry id is unknown (so this endpoint can't be
// abused as an existence probe for fabricated ids).
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
		if errors.Is(err, errUnsupportedContentType) {
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		http.Error(w, fmt.Sprintf("bad body: %v", err), http.StatusBadRequest)
		return
	}

	labels := make([]domain.EntryType, 0, len(rawLabels))
	for _, l := range rawLabels {
		labels = append(labels, domain.EntryType(l))
	}

	if err := s.svc.SetLabels(r.Context(), entryID, labels); err != nil {
		http.Error(w, fmt.Sprintf("set labels: %v", err), statusFor(err))
		return
	}

	// SetLabels is the canonical write — anything it accepted is what's
	// persisted, in the same alphabetical order GetLabels would return.
	// Avoid the read-back round-trip (which previously hid a 500 path
	// when GetLabels failed after a successful Set, leaving the client
	// without a response body confirming the mutation).
	persisted := append([]domain.EntryType{}, labels...)
	sort.Slice(persisted, func(i, j int) bool { return persisted[i] < persisted[j] })

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(persisted); err != nil {
		return
	}
}

// readLabelsBody decodes either content type into the same string list.
// "Clear all" requires an explicit `{"labels":[]}` on the JSON path or
// a POST with no `labels=` field on the form path — empty bodies are
// rejected at the decoder so a chunked-transfer mistake (ContentLength
// reports -1, body is real) can't silently nuke the set.
func readLabelsBody(r *http.Request) ([]string, error) {
	ct := r.Header.Get("Content-Type")
	// Strip params like "; charset=utf-8" before comparing.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/json":
		var body labelsBody
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
		return nil, fmt.Errorf("%w: %q", errUnsupportedContentType, ct)
	}
}
