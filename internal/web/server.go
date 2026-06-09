// Package web is the localhost truth-source UI. Single binary still: HTML
// templates + minimal CSS are embedded via embed.FS so deployment is one
// file. htmx is the only client-side dep (also embedded). Heavy frontend
// frameworks are intentionally out of scope (ROADMAP technical constraint).
//
// Security posture (Slice B unblocks v0.1.0 + locks the irreversible bits):
//   - Listener hard-bound to 127.0.0.1 in NewServer regardless of config —
//     DNS rebinding defence at the wire, not just middleware.
//   - Host-header allowlist middleware ("127.0.0.1" / "localhost" / "[::1]"
//     plus operator-supplied additions in config.Web.AllowedHosts).
//   - http.CrossOriginProtection (Go 1.25) wired on the mux.
//   - GET handlers are pure reads; mutations go through POST with a
//     mutationGuard chain seam that's idempotent today (single-user
//     assumption) but ready to layer CSRF/local-token auth on later
//     without touching call sites.
//
// CLI / HTTP both call the same RepositoryService — adapter layer is
// thin, integration tests cover the wiring.
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/repository"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server bundles the HTTP listener, the repository service it dispatches
// to, the parsed templates, and the security middleware chain.
//
// templates is keyed by page filename. Each entry is a separately-parsed
// template set: base.html + that one page. Splitting per-page avoids the
// html/template "last-defined wins" trap where multiple pages all
// declaring {{define "content"}} would trample each other and every page
// would render the same body.
type Server struct {
	svc       *repository.Service
	cfg       config.Web
	templates map[string]*template.Template
	mux       *http.ServeMux
	httpSrv   *http.Server
	listener  net.Listener
}

// NewServer wires templates, mux, middleware, and the loopback-only
// listener. It does NOT start serving — Serve does that. Splitting setup
// from start lets tests grab the listener address before Run blocks.
func NewServer(svc *repository.Service, cfg config.Web) (*Server, error) {
	if cfg.Port == 0 {
		cfg.Port = config.DefaultWebPort
	}

	tmpls, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static fs: %w", err)
	}

	s := &Server{
		svc:       svc,
		cfg:       cfg,
		templates: tmpls,
		mux:       http.NewServeMux(),
	}

	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /entries/{id}", s.handleEntryDetail)
	s.mux.HandleFunc("GET /goals/{id}", s.handleGoalDetail)
	s.mux.HandleFunc("GET /new/{type}", s.handleNewForm)
	s.mux.HandleFunc("POST /new/{type}", s.handleNewSubmit)
	s.mux.HandleFunc("POST /goals/{id}/status", s.handleGoalStatus)
	s.mux.HandleFunc("POST /goals/{id}/attach", s.handleGoalAttach)
	s.mux.HandleFunc("POST /edges/{id}/detach", s.handleEdgeDetach)
	s.mux.HandleFunc("POST /materialize", s.handleMaterialize)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Hard-bind to loopback at the listener layer. Even if the operator
	// puts 0.0.0.0 in config we refuse — Phase 1 is single-user only.
	addr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("web: listen %s: %w", addr, err)
	}
	s.listener = ln

	// Stack: CrossOriginProtection → Host allowlist → mutationGuard → mux.
	// Order matters:
	//   - CrossOriginProtection outermost: bails fastest on cross-origin
	//     browser POSTs via Sec-Fetch-Site.
	//   - Host allowlist next: closes the DNS-rebinding hole at HTTP
	//     layer (the listener bind is the other half).
	//   - mutationGuard innermost: identity today, the named seam where
	//     CSRF / local-token-auth / rate-limit / actor-injection land
	//     when Phase 2 needs them — additive, no handler changes
	//     required (architect review #10 P0).
	handler := s.mutationGuard(s.mux)
	handler = s.hostAllowlist(handler)
	cop := http.NewCrossOriginProtection()
	handler = cop.Handler(handler)

	s.httpSrv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s, nil
}

// renderPage wraps the page-specific data in a pageEnvelope that carries
// global header info (pending segment count) before executing the
// template. Centralising this avoids every handler having to fetch the
// header count + populate every data struct separately.
//
// Two correctness invariants this method now enforces:
//
//  1. Template execution writes into a bytes.Buffer first. Pre-fix the
//     Content-Type header was set on w before ExecuteTemplate started
//     writing the body, so a mid-render template error left a partial
//     HTML document followed by http.Error's plaintext appended into
//     the same response body — the engineer saw garbled output with no
//     visible signal of the failure (go-reviewer P1 / hunter P0).
//
//  2. CountPendingSegments errors are no longer silently zeroed. A
//     locked DB or context-cancelled query previously produced a
//     "nothing pending" header even though the count was actually
//     unknown — silent confusing UX. The error is now logged AND
//     surfaced on the envelope so templates can render an indicator.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name string, page any) {
	tmpl, ok := s.templates[name]
	if !ok {
		http.Error(w, "template: unknown page "+name, http.StatusInternalServerError)
		return
	}

	pending, pendErr := s.svc.CountPendingSegments(r.Context(), repository.MaterializeScope{})
	if pendErr != nil {
		// Don't fail the whole page render on a header-widget query — the
		// engineer can still read the rest. Log it so it's observable, and
		// surface a sentinel so the template can render "?" instead of "0".
		slog.WarnContext(r.Context(), "count pending segments", "err", pendErr)
	}
	envelope := pageEnvelope{
		Header: header{
			PendingSegments: pending,
			PendingErr:      pendErr != nil,
		},
		Page: page,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base", envelope); err != nil {
		// Body untouched — safe to emit a clean 5xx.
		http.Error(w, fmt.Sprintf("template: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// pageEnvelope is what the base template actually receives. Templates
// access {{.Header.PendingSegments}} for the nav bar and {{.Page.Foo}}
// for everything else; this keeps the page data type-safe per route
// while sharing the global header.
type pageEnvelope struct {
	Header header
	Page   any
}

type header struct {
	PendingSegments int
	PendingErr      bool // true when the count query failed; template can render "?"
}

// Addr is the actual bound address; useful for tests + the CLI startup
// banner when Port was 0 (let-kernel-pick).
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve blocks until the context is cancelled or the listener errors.
// Cancelling ctx triggers a graceful shutdown with a 5s drain window.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("web: serve: %w", err)
	}
}

// parseTemplates returns one parsed template set per page. Each set
// includes base.html plus exactly one page file — this is what stops
// {{define "content"}} blocks across pages from clobbering each other
// (html/template's "last define wins" silently produced bug-shaped UIs
// the first time we tried single-set loading).
func parseTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.UTC().Format(time.RFC3339)
		},
	}

	pageFiles, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob templates: %w", err)
	}

	out := make(map[string]*template.Template, len(pageFiles))
	for _, page := range pageFiles {
		name := page[len("templates/"):]
		if name == "base.html" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(templatesFS, "templates/base.html", page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}
