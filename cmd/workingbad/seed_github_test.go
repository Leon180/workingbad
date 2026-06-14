package main

import "testing"

// ghSlugRe guards the repo slug before it's interpolated into the GitHub API
// URL. It must accept real owner/name slugs and reject anything that could
// inject query params or redirect the request to another host.
func TestGhSlugRe(t *testing.T) {
	valid := []string{
		"Leon180/workingbad",
		"golang/go",
		"a/b",
		"with.dots/and-dashes_and_underscores",
	}
	for _, s := range valid {
		if !ghSlugRe.MatchString(s) {
			t.Errorf("expected %q to be a valid slug", s)
		}
	}

	invalid := []string{
		"",                       // empty
		"owner",                  // no repo
		"owner/repo/extra",       // path traversal-ish
		"owner/repo?state=all&x", // query injection
		"@evil.example.com",      // userinfo host redirect
		"owner/repo#frag",        // fragment
		"owner /repo",            // space
		"../../etc",              // traversal
		"owner/repo ",            // trailing space
	}
	for _, s := range invalid {
		if ghSlugRe.MatchString(s) {
			t.Errorf("expected %q to be rejected", s)
		}
	}
}
