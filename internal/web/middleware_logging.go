package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// loggingMiddleware emits one structured log line per non-noise request:
// method, path, status, duration, response bytes. Wraps ResponseWriter
// so we can capture the status code (the handler writes it via
// WriteHeader, which the inner writer otherwise swallows from the
// middleware's perspective).
//
// Skips:
//   - /healthz — periodic checks would dominate the log
//   - /static/* — asset fetches are 1:1 with the page that loads them
//
// Sits between hostAllowlist (outer) and mutationGuard (inner) in the
// chain. After hostAllowlist so forbidden-host rejections don't appear
// in the log; before mutationGuard so mutation-rejected POSTs still log
// (we want visibility into rejected mutations).
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLogSkipPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.InfoContext(r.Context(), "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", rw.bytes,
		)
	})
}

func isLogSkipPath(p string) bool {
	if p == "/healthz" {
		return true
	}
	return strings.HasPrefix(p, "/static/")
}

// statusCapturingWriter wraps http.ResponseWriter to remember the
// status code + body size that streamed past. Defaults to 200 on the
// no-WriteHeader path (handlers that just Write straight into the
// response).
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	// status defaults to 200 at construction time; net/http's implicit-200
	// path therefore yields the right value without us needing to flip
	// wroteHeader here (nothing downstream reads it again — WriteHeader
	// can't be called after Write per the stdlib contract).
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
