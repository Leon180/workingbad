package web

import (
	"fmt"
	"net/http"
)

// handleIndex is the placeholder landing handler. Real list view lands in
// commit 3 of Slice B; this proves the wiring (template parse, render,
// embed FS) end-to-end.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "index.html", indexData{
		Title: "workingbad — local truth source",
	}); err != nil {
		http.Error(w, fmt.Sprintf("template: %v", err), http.StatusInternalServerError)
	}
	_ = r
}

type indexData struct {
	Title string
}
