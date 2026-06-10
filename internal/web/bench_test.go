package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Issue #28: web hot paths — safeRedirectPath runs on every POST that
// redirects (every mutation); the index render runs on every GET / page
// view. Both are tight enough that a 5% regression matters.

func BenchmarkSafeRedirectPath(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7878/", nil)
	// Cover the mix: same-host pass-through, foreign-host rejection,
	// backslash-smuggled rejection, path-traversal rejection. Roughly
	// matches the actual traffic shape (most paths are legit, fast path
	// dominates).
	targets := []string{
		"/?type=goal",
		"/goals/019eac6e-d377-74b3-8a9f-2bd5fe30cc76",
		"http://127.0.0.1:7878/?materialized=1",
		"http://evil.example.com/steal",
		`/\evil.example.com`,
		"/../etc/passwd",
	}
	b.ResetTimer()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = safeRedirectPath(req, targets[i%len(targets)], "/")
		i++
	}
}

func BenchmarkRenderIndex_Empty(b *testing.B) {
	srv := newTestServerFromTB(b)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
	}
}
