package web

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/config"
	"github.com/Leon180/workingbad/internal/repository"
)

// newTestServer is the testing.T entry point — thin wrapper around
// newTestServerFromTB so both Test* and Benchmark* code paths can build
// a wired-up Server from the same body.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerFromTB(t)
}

// newTestServerFromTB builds a Server backed by a temp SQLite DB with a
// freshly-migrated schema. Caller drives via srv.mux.ServeHTTP — Serve()
// is never started, and the listener is closed immediately so the kernel
// reclaims the ephemeral port.
//
// Accepts testing.TB so Benchmark code paths can share the helper — the
// TempDir / Helper / Cleanup / Fatalf surface is identical on T and B.
func newTestServerFromTB(tb testing.TB) *Server {
	tb.Helper()
	dbPath := tb.TempDir() + "/web.sqlite"
	db, err := repository.Open(dbPath)
	if err != nil {
		tb.Fatalf("open db: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	svc := repository.NewService(db)

	// Port=0 → kernel picks a free port. We close the listener immediately
	// because tests/benches talk to srv.mux directly.
	srv, err := NewServer(svc, config.Web{Port: 0})
	if err != nil {
		tb.Fatalf("NewServer: %v", err)
	}
	_ = srv.Close()
	return srv
}

// TestServer_IndexRenders is the wiring smoke test: index template parses,
// embed.FS is wired, the host allowlist accepts loopback, and the response
// body contains the expected landing copy.
func TestServer_IndexRenders(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "workingbad") {
		t.Errorf("body missing brand: %q", body)
	}
	if !strings.Contains(string(body), "local truth source") {
		t.Errorf("body missing footer tagline: %q", body)
	}
}

func TestServer_HealthzOK(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestHostAllowlist_BlocksForeignHost proves the DNS-rebinding defence:
// even when the request reaches the handler chain, a Host header pointing
// at a non-loopback domain is rejected.
func TestHostAllowlist_BlocksForeignHost(t *testing.T) {
	srv := newTestServer(t)

	// Wrap the mux through the same middleware stack actionServe uses.
	handler := srv.hostAllowlist(srv.mux)

	cases := []struct {
		host     string
		wantCode int
	}{
		{"127.0.0.1:7878", http.StatusOK},
		{"localhost:7878", http.StatusOK},
		{"127.0.0.1", http.StatusOK},
		{"evil.example.com", http.StatusForbidden},
		{"phishing.local", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/healthz", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("host %q: status = %d, want %d", tc.host, rec.Code, tc.wantCode)
			}
		})
	}

	// Operator-supplied additions:
	srv.cfg.AllowedHosts = []string{"workingbad.local"}
	handler = srv.hostAllowlist(srv.mux)
	req := httptest.NewRequest(http.MethodGet, "http://workingbad.local/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("operator-allowed host workingbad.local: status = %d, want 200", rec.Code)
	}
}

func TestNewServer_BindsToLoopbackOnly(t *testing.T) {
	srv := newTestServer(t)
	// listener was closed in newTestServer, but the bound address is captured
	// from before close — we reconstruct via a fresh NewServer at port 0 here.
	dbPath := t.TempDir() + "/web2.sqlite"
	db, err := repository.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	svc := repository.NewService(db)
	srv2, err := NewServer(svc, config.Web{Port: 0})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv2.Close() }()
	if !strings.HasPrefix(srv2.Addr(), "127.0.0.1:") {
		t.Errorf("listener bound to %q, expected 127.0.0.1:*", srv2.Addr())
	}
	_ = srv // appease unused-var
}

// TestRenderPage_TemplateErrorReturnsCleanResponse proves the
// buffer-then-write fix: a template that fails mid-render must emit a
// clean 5xx response, NOT a partial 200 body with the error appended.
//
// The pre-fix bug: w.Header().Set("Content-Type") was called before
// ExecuteTemplate started writing, so the status header + partial HTML
// were already committed when http.Error tried to write a second body.
// The buffer-then-write fix means template errors are caught before
// the response is committed.
func TestRenderPage_TemplateErrorReturnsCleanResponse(t *testing.T) {
	srv := newTestServer(t)
	// Inject a template that refers to a missing field — triggers a
	// runtime template error mid-execution.
	srv.templates["broken.html"] = template.Must(template.New("broken.html").
		Parse(`{{define "base"}}<html>{{.Nope.Missing}}</html>{{end}}`))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	srv.renderPage(rec, req, "broken.html", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("template error should produce 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html>") {
		t.Errorf("partial HTML leaked into response: %q", body)
	}
	if !strings.Contains(body, "template:") {
		t.Errorf("error message missing: %q", body)
	}
}

// Ensure context cancellation shuts the server down cleanly.
func TestServer_ServeShutsDownOnContextCancel(t *testing.T) {
	dbPath := t.TempDir() + "/web3.sqlite"
	db, err := repository.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	svc := repository.NewService(db)
	srv, err := NewServer(svc, config.Web{Port: 0})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve returned error after context cancel: %v", err)
	}
}
