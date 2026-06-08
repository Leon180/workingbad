package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestSafeRedirectPath_TruthTable covers the redirect-safety helper that
// gates handleMaterialize's Referer-driven destination. Pre-fix, a Referer
// of http://evil.example.com/ produced an open redirect; this test fails
// the moment that regression returns.
func TestSafeRedirectPath_TruthTable(t *testing.T) {
	cases := []struct {
		host   string
		target string
		want   string
	}{
		// Same-host relative paths pass through (path+query only).
		{"127.0.0.1:7890", "/", "/"},
		{"127.0.0.1:7890", "/?type=goal", "/?type=goal"},
		{"127.0.0.1:7890", "/goals/abc", "/goals/abc"},

		// Same-host absolute URLs reduce to path+query.
		{"127.0.0.1:7890", "http://127.0.0.1:7890/?materialized=1", "/?materialized=1"},

		// Foreign host → fallback (closes the open-redirect path).
		{"127.0.0.1:7890", "http://evil.example.com/steal", "/"},
		{"127.0.0.1:7890", "https://attacker.test/", "/"},
		{"127.0.0.1:7890", "//evil.example.com/", "/"},

		// Path traversal → fallback (closes the /etc/passwd path).
		{"127.0.0.1:7890", "/../etc/passwd", "/"},
		{"127.0.0.1:7890", "/goals/../../etc/passwd", "/"},
		{"127.0.0.1:7890", "/./foo", "/"},

		// Garbage → fallback (no panics on malformed URLs).
		{"127.0.0.1:7890", "", "/"},
		{"127.0.0.1:7890", "javascript:alert(1)", "/"},
		{"127.0.0.1:7890", "data:text/html,foo", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/", nil)
			got := safeRedirectPath(req, tc.target, "/")
			if got != tc.want {
				t.Errorf("safeRedirectPath(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}

// TestSafeGoalRedirect_UUIDOnly verifies the form-driven goal_id can only
// produce /goals/{uuid-shaped-value} or the fallback. This is the second
// half of the path-traversal fix.
func TestSafeGoalRedirect_UUIDOnly(t *testing.T) {
	cases := []struct {
		goalID string
		want   string
	}{
		// uuid v7 / v4 shapes pass.
		{"019ea707-8f07-7493-8843-1d0b196a6fa6", "/goals/019ea707-8f07-7493-8843-1d0b196a6fa6"},
		{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "/goals/a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},

		// Path traversal / non-UUID → fallback (no /goals/ prefix at all).
		{"../../etc/passwd", "/"},
		{"abc", "/"},
		{"", "/"},
		{"019ea707-8f07-7493-8843-1d0b196a6fa6/extra", "/"},
		{"019ea707-8f07-7493-8843-1d0b196a6fa6?evil=1", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.goalID, func(t *testing.T) {
			got := safeGoalRedirect(tc.goalID, "/")
			if got != tc.want {
				t.Errorf("safeGoalRedirect(%q) = %q, want %q", tc.goalID, got, tc.want)
			}
		})
	}
}

// TestMaterialize_RejectsForeignRefererRedirect is the integration-level
// proof: a POST /materialize with a hostile Referer must land the
// engineer back on / + flash params, NEVER on the attacker's site.
func TestMaterialize_RejectsForeignRefererRedirect(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/materialize", nil)
	req.Header.Set("Referer", "http://evil.example.com/steal")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "evil.example.com") {
		t.Errorf("OPEN REDIRECT: Location=%q leaks to attacker host", loc)
	}
	if !strings.HasPrefix(loc, "/") {
		t.Errorf("Location should be a relative path, got %q", loc)
	}
	if !strings.Contains(loc, "materialized=") {
		t.Errorf("flash params dropped: %q", loc)
	}
}

// TestEdgeDetach_RejectsPathTraversalGoalID is the integration-level
// proof for the second half: a hostile goal_id in the form must not
// reach the Location header verbatim.
func TestEdgeDetach_RejectsPathTraversalGoalID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Goal")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "act")
	edge, err := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	form := "goal_id=../../etc/passwd"
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/edges/"+edge.ID+"/detach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "..") || strings.Contains(loc, "/etc/") {
		t.Errorf("PATH TRAVERSAL: Location=%q reflects raw goal_id", loc)
	}
	if loc != "/" {
		t.Errorf("hostile goal_id should fall back to /, got %q", loc)
	}
}

// TestEdgeDetach_RequiresGoalID closes the silent-fallback gap: a
// missing goal_id is a 400 now, not a quiet redirect to /. The form
// always carries goal_id; missing means something upstream is broken.
func TestEdgeDetach_RequiresGoalID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Goal")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "act")
	edge, _ := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID)

	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/edges/"+edge.ID+"/detach", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing goal_id should be 400, got %d", rec.Code)
	}
}
