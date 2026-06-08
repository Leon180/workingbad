package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

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
//	GET /                           — live entries, default limit
//	GET /?type=goal                 — only goals
//	GET /?type=activity&limit=200
//	GET /?at=2026-06-08T14:00:00Z   — time-travel: state as of that moment
//	GET /?at=2026-06-08             — date alone = midnight UTC
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	filter := repository.ListFilter{
		Type:  domain.EntryType(r.URL.Query().Get("type")),
		Limit: parseLimit(r.URL.Query().Get("limit")),
	}
	if !isKnownType(filter.Type) {
		filter.Type = "" // ignore garbage rather than 400 — UI errors are
		// noisy and the list itself shows what was actually filtered.
	}

	atStr := r.URL.Query().Get("at")
	asOf, asOfErr := parseAtParam(atStr)

	var (
		entries []domain.Entry
		err     error
	)
	switch {
	case atStr != "" && asOfErr == nil:
		entries, err = s.svc.ListEntriesAt(r.Context(), asOf, filter)
	default:
		entries, err = s.svc.ListEntries(r.Context(), filter)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	data := listData{
		Title:           "workingbad — entries",
		Entries:         entries,
		Types:           allEntryTypes,
		ActiveType:      filter.Type,
		ActiveLimit:     filter.Limit,
		ActiveAt:        atStr,
		AsOf:            asOf,
		AtParseErr:      asOfErrString(atStr, asOfErr),
		MaterializedNum: r.URL.Query().Get("materialized"),
		FailedNum:       r.URL.Query().Get("failed"),
	}
	s.renderPage(w, r, "index.html", data)
}

type listData struct {
	Title           string
	Entries         []domain.Entry
	Types           []domain.EntryType
	ActiveType      domain.EntryType
	ActiveLimit     int
	ActiveAt        string    // raw query value, echoed into the form input
	AsOf            time.Time // parsed (zero when ActiveAt is "" or unparseable)
	AtParseErr      string    // human-readable; empty when ActiveAt is blank or parsed OK
	MaterializedNum string    // ?materialized=N flash param after /materialize POST
	FailedNum       string    // ?failed=N companion to MaterializedNum
}

// parseAtParam accepts the same shapes as the CLI's --at flag:
//   - RFC3339 timestamp (2026-06-08T14:00:00Z)
//   - date-only (2026-06-08, interpreted as midnight UTC)
//
// Empty string returns zero time with no error — callers branch on the
// zero check to decide between live and at-time queries.
func parseAtParam(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q (expected RFC3339 or YYYY-MM-DD)", s)
}

func asOfErrString(raw string, err error) string {
	if raw == "" || err == nil {
		return ""
	}
	return err.Error()
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
