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
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
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
type Server struct {
	svc       *repository.Service
	cfg       config.Web
	templates *template.Template
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

	// Stack: CrossOriginProtection → Host allowlist → mux. Order matters:
	// the outermost (CrossOriginProtection) sees the raw request first
	// and bails fastest on bad input.
	handler := s.hostAllowlist(s.mux)
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

// parseTemplates loads every templates/*.html file as a single template
// set so handlers can ExecuteTemplate by name and partials compose.
func parseTemplates() (*template.Template, error) {
	root := template.New("workingbad").Funcs(template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.UTC().Format(time.RFC3339)
		},
	})
	return root.ParseFS(templatesFS, "templates/*.html")
}
