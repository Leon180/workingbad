package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

const defaultListLimit = 50

// allEntryTypes drives the type-filter dropdown. Order is "most-common
// first" for dogfooding ergonomics: goal/activity head the list because
// they dominate the view, then the manual derived-layer types.
var allEntryTypes = []domain.EntryType{
	domain.EntryTypeGoal,
	domain.EntryTypeActivity,
	domain.EntryTypeResearch,
	domain.EntryTypeDecision,
	domain.EntryTypeDiscuss,
}

// handleIndex renders the entry list. The list is the home — engineers
// land directly on what they care about, no separate dashboard. Filters
// live in the query string so URLs are sharable and back-button works:
//
//	GET /              — all entries, default limit
//	GET /?type=goal    — only goals
//	GET /?type=activity&limit=200
//
// (Time-travel `?at=` lands in the next commit.)
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	filter := repository.ListFilter{
		Type:  domain.EntryType(r.URL.Query().Get("type")),
		Limit: parseLimit(r.URL.Query().Get("limit")),
	}
	if !isKnownType(filter.Type) {
		filter.Type = "" // ignore garbage rather than 400 — UI errors are
		// noisy and the list itself shows what was actually filtered.
	}

	entries, err := s.svc.ListEntries(r.Context(), filter)
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	data := listData{
		Title:       "workingbad — entries",
		Entries:     entries,
		Types:       allEntryTypes,
		ActiveType:  filter.Type,
		ActiveLimit: filter.Limit,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, fmt.Sprintf("template: %v", err), http.StatusInternalServerError)
	}
}

type listData struct {
	Title       string
	Entries     []domain.Entry
	Types       []domain.EntryType
	ActiveType  domain.EntryType
	ActiveLimit int
}

func parseLimit(s string) int {
	if s == "" {
		return defaultListLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 1000 {
		return defaultListLimit
	}
	return n
}

func isKnownType(t domain.EntryType) bool {
	if t == "" {
		return true
	}
	for _, k := range allEntryTypes {
		if t == k {
			return true
		}
	}
	return false
}
