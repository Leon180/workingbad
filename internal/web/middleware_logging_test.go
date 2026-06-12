package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureSlog points slog at an in-memory buffer for the duration of t
// and returns it. Restores the previous default on cleanup so other
// tests don't see the JSON handler.
//
// Caller-side filter: goose's migration log goes through stdlib `log`
// which Go 1.21+ bridges into slog, so the buffer also collects
// "OK 0001_entries.sql"-shaped noise from the test DB setup. Use
// httpLogLines to read only the `msg=http` lines this middleware emits.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// httpLogLines returns only the lines emitted by loggingMiddleware
// (msg=http), filtering out the goose-via-stdlib-log noise that Go's
// log/slog bridge folds into the same handler at test-DB setup time.
func httpLogLines(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, `"msg":"http"`) {
			out = append(out, line)
		}
	}
	return out
}

// TestLoggingMiddleware_EmitsOneLinePerRequest — happy-path coverage for
// the acceptance criterion on issue #19. One request → one log line with
// method, path, status, duration_ms, bytes.
func TestLoggingMiddleware_EmitsOneLinePerRequest(t *testing.T) {
	buf := captureSlog(t)
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	srv.loggingMiddleware(srv.mux).ServeHTTP(rec, req)

	lines := httpLogLines(buf)
	if len(lines) != 1 {
		t.Fatalf("got %d http log lines, want 1: %q", len(lines), buf.String())
	}
	got := lines[0]
	for _, want := range []string{
		`"msg":"http"`,
		`"method":"GET"`,
		`"path":"/"`,
		`"status":200`,
		`"duration_ms":`,
		`"bytes":`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log line missing %s — got %s", want, got)
		}
	}
}

// TestLoggingMiddleware_SkipsHealthAndStatic — periodic /healthz hits and
// asset GETs would dominate the log; the middleware must skip them.
func TestLoggingMiddleware_SkipsHealthAndStatic(t *testing.T) {
	buf := captureSlog(t)
	srv := newTestServer(t)
	h := srv.loggingMiddleware(srv.mux)

	for _, path := range []string{"/healthz", "/static/app.css"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	if lines := httpLogLines(buf); len(lines) != 0 {
		t.Errorf("expected zero http log lines, got %d: %v", len(lines), lines)
	}
}

// TestLoggingMiddleware_CapturesNon200Status — a handler that writes a
// 4xx must be reflected in the log line, not silently downgraded to 200.
func TestLoggingMiddleware_CapturesNon200Status(t *testing.T) {
	buf := captureSlog(t)
	srv := newTestServer(t)

	// GET on a goal id that doesn't exist returns 404 from the handler.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/goals/0192f6c0-7e31-7c2b-9b8a-ffffffffffff", nil)
	rec := httptest.NewRecorder()
	srv.loggingMiddleware(srv.mux).ServeHTTP(rec, req)

	if !strings.Contains(buf.String(), `"status":404`) {
		t.Errorf("expected status=404 in log, got: %s", buf.String())
	}
}

// TestServerClose_IsIdempotent — closing twice is safe (test harness
// pattern: NewServer + immediate Close, then t.Cleanup's Close).
func TestServerClose_IsIdempotent(t *testing.T) {
	srv := newTestServer(t) // newTestServer already calls Close once
	if err := srv.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

// TestStatusCapturingWriter_DefaultsTo200 — handlers that just Write
// without an explicit WriteHeader implicitly send 200; the wrapper must
// reflect that.
func TestStatusCapturingWriter_DefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &statusCapturingWriter{ResponseWriter: rec, status: http.StatusOK}
	_, _ = w.Write([]byte("ok"))
	if w.status != http.StatusOK {
		t.Errorf("status = %d, want 200", w.status)
	}
	if w.bytes != 2 {
		t.Errorf("bytes = %d, want 2", w.bytes)
	}
}

// Ensure context-aware InfoContext doesn't panic with a nil-bodied request.
func TestLoggingMiddleware_NilContext(t *testing.T) {
	_ = captureSlog(t)
	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	srv.loggingMiddleware(srv.mux).ServeHTTP(rec, req)
}
