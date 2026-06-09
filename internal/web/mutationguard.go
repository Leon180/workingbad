package web

import "net/http"

// mutationGuard is the named seam for CSRF / local-token-auth /
// rate-limit / actor-id injection — anything that needs to run on the
// mutation path (POST / PUT / PATCH / DELETE) without touching the
// individual handler signatures.
//
// Today it's identity: single-user Phase 1 has no real threat that the
// listener-level 127.0.0.1 bind + Host allowlist + CrossOriginProtection
// don't already cover (see grill doc §4.6 + security review). But the
// architect review (PR #10) flagged that the seam was previously a
// docstring promise with no code — a future migration smell. Making it
// a real (no-op) middleware means Phase 2's CSRF-token wiring is a
// single-file change inside this file, not a sweep across every
// mutate handler.
//
// Why a method on *Server and not a free function: it lets future logic
// reach into s.svc (e.g. record an actor entry per mutation) and
// s.cfg.Web (e.g. read CSRF secret config) without thread-local
// gymnastics.
func (s *Server) mutationGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// Read paths skip the guard entirely — keeps response time
			// flat for the dominant traffic class.
			next.ServeHTTP(w, r)
			return
		}
		// Mutation path. Future hooks land here:
		//
		//   1. CSRF: if r.Header.Get("X-CSRF-Token") != s.csrfFor(r) → 403
		//   2. Local token auth: bearer token check against s.cfg.Web.Token
		//   3. Rate limit: bucket by remote-addr or session
		//   4. Actor injection: stamp r.Context() with a derived actor id
		//      so handlers can populate Entry.Actor without reading
		//      headers themselves
		//
		// None of those exist today. The seam is wired so they CAN be
		// added without touching handlers_create.go / handlers_mutate.go.
		next.ServeHTTP(w, r)
	})
}
