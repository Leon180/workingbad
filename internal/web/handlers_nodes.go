package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// The node read surface (Slice D2f). Additive — the entry views at / and
// /entries are untouched. Nodes are the graph-level work units (backfilled
// from entries, curated by hand via the manual ops API, and — later — produced
// by the LLM split/aggregate pipeline). Entries remain the raw-record view.
//
//	GET /nodes                       — live nodes, default limit
//	GET /nodes?type=goal             — filter by type
//	GET /nodes?q=cache               — FTS5 search (bm25)
//	GET /nodes?at=2026-06-08T14:00Z  — time-travel: node graph as of that moment
//	GET /nodes/{id}                  — node detail + supersede-chain history

func (s *Server) handleNodeIndex(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := repository.NodeListFilter{
		Type:            domain.EntryType(r.URL.Query().Get("type")),
		Limit:           parseLimit(r.URL.Query().Get("limit")),
		IncludeArchived: r.URL.Query().Get("include_archived") == "true",
	}
	if !isKnownType(filter.Type) {
		filter.Type = ""
	}

	atStr := r.URL.Query().Get("at")
	asOf, asOfErr := parseAtParam(atStr)

	var (
		nodes []domain.Node
		err   error
	)
	switch {
	case query != "":
		// Search is its own mode: bm25-ranked over live nodes, ignores type/at
		// (the FTS index holds live rows only).
		nodes, err = s.svc.SearchNodes(r.Context(), query, filter.Limit)
	case atStr != "" && asOfErr == nil:
		nodes, err = s.svc.ListNodesAt(r.Context(), asOf, filter)
	default:
		nodes, err = s.svc.ListNodes(r.Context(), filter)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}

	data := nodeListData{
		Title:       "workingbad — nodes",
		Nodes:       nodes,
		Types:       allEntryTypes,
		ActiveType:  filter.Type,
		ActiveLimit: filter.Limit,
		Query:       query,
	}
	// Search is its own mode and ignores `at`; don't echo a stale time-travel
	// banner/state alongside search results.
	if query == "" {
		data.ActiveAt = atStr
		data.AsOf = asOf
		data.AtParseErr = asOfErrString(atStr, asOfErr)
	}
	s.renderPage(w, r, "nodes.html", data)
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.svc.GetNode(r.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}
	history, err := s.svc.NodeHistory(r.Context(), node.LogicalID)
	if err != nil {
		http.Error(w, fmt.Sprintf("repository: %v", err), http.StatusInternalServerError)
		return
	}
	s.renderPage(w, r, "node_detail.html", nodeDetailData{
		Title:   "node — " + node.Title,
		Node:    node,
		History: history,
	})
}

type nodeListData struct {
	Title       string
	Nodes       []domain.Node
	Types       []domain.EntryType
	ActiveType  domain.EntryType
	ActiveLimit int
	ActiveAt    string
	AsOf        time.Time
	AtParseErr  string
	Query       string // ?q= search term, echoed into the search box
}

type nodeDetailData struct {
	Title   string
	Node    domain.Node   // the requested version (any in the chain)
	History []domain.Node // full supersede chain, newest first
}
