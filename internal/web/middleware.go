package web

import (
	"net/http"
	"strings"
)

// hostAllowlist blocks any request whose Host header isn't loopback (or
// an operator-supplied addition). DNS-rebinding defence: even after the
// 127.0.0.1 bind, a hostile script could craft requests with a foreign
// Host header; we reject them at HTTP layer too.
//
// Allowed by default:
//   - 127.0.0.1[:port]
//   - localhost[:port]
//   - [::1][:port]   (IPv6, but we listen on tcp4 so it shouldn't arrive)
//
// Additions live in cfg.AllowedHosts (use case: engineers who reverse-
// proxy to a custom dev hostname like "workingbad.local").
func (s *Server) hostAllowlist(next http.Handler) http.Handler {
	allowed := map[string]struct{}{
		"127.0.0.1": {},
		"localhost": {},
		"[::1]":     {},
		"::1":       {},
	}
	for _, h := range s.cfg.AllowedHosts {
		allowed[h] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// Strip port: Host can be "127.0.0.1:7878" or just "127.0.0.1".
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
		if _, ok := allowed[host]; !ok {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
