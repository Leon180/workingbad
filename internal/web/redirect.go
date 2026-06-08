package web

import (
	"net/http"
	"net/url"
	"strings"
)

// safeRedirectPath normalises a redirect target so it can only point at
// our own routes. Used for any redirect destination that comes from
// attacker-controllable input — currently the Referer header (post-
// materialize) and the form-supplied goal_id (post-detach).
//
// Threat model (slice B): the listener is bound to 127.0.0.1, so foreign
// hosts can't reach the port directly, but a same-origin or sibling-
// process attacker can still send crafted Referer / form values. Without
// this normalisation:
//   - Referer: http://evil.example.com/  → 303 Location: http://evil.example.com/...
//     (open redirect; phishing foothold even on a localhost app because
//     the engineer's browser follows the Location)
//   - goal_id: ../../etc/passwd → 303 Location: /goals/../../etc/passwd
//     which browsers normalise to /etc/passwd
//
// Rules:
//   - empty → "/"
//   - absolute URL with a different host than r.Host → "/"
//   - any "../" or "./" segment in the path → "/"
//   - relative path that doesn't start with "/" → "/"
//   - otherwise return the path+query portion (host stripped)
//
// fallback overrides the default "/" — handlers that know a sensible
// destination (e.g. /goals/{id}) can pass it.
func safeRedirectPath(r *http.Request, target, fallback string) string {
	if fallback == "" {
		fallback = "/"
	}
	if target == "" {
		return fallback
	}

	u, err := url.Parse(target)
	if err != nil {
		return fallback
	}
	if u.Host != "" && u.Host != r.Host {
		return fallback
	}

	path := u.RequestURI()
	if path == "" {
		return fallback
	}
	// Reject relative-style or empty paths.
	if !strings.HasPrefix(path, "/") {
		return fallback
	}
	// Reject any path component that would traverse the URL space. Browsers
	// normalise these silently, so a redirect to "/goals/../../etc/passwd"
	// lands the user at "/etc/passwd" without us seeing it on the server.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return fallback
		}
	}
	return path
}

// safeGoalRedirect is the narrower variant used by edge-detach: the
// expected destination is "/goals/{goal_id}", and the goal_id is form
// input. We accept only canonical UUID-shaped values (uuid v7 = 36 chars,
// 8-4-4-4-12 with hex). Anything else falls back to fallback.
func safeGoalRedirect(goalID, fallback string) string {
	if fallback == "" {
		fallback = "/"
	}
	if !looksLikeUUID(goalID) {
		return fallback
	}
	return "/goals/" + goalID
}

// looksLikeUUID is a tight 36-char hex-with-dashes test. We don't validate
// the version nibble — any uuid-shaped string is good enough as a redirect
// gate, and rejecting v4 ids written by external tools would surprise
// users. SQL queries are placeholder-protected regardless.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexRune(r) {
				return false
			}
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
