package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestGraph_IsHome — the product lands on the graph at "/". The redesign
// made the swim-lane canvas the front door (entries moved to /entries).
func TestGraph_IsHome(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedGoal(t, srv, ctx, "Ship Slice B")

	body := getBody(t, srv, "/")
	for _, want := range []string{
		`<h1>graph</h1>`,
		`data-theme="dark"`,              // dark is the default shell theme (v2)
		`class="theme-toggle"`,           // header dark/light toggle
		`id="tl-svg"`,                    // pinned-timeline scroll SVG (v2)
		`class="tl-titles"`,              // pinned left lane titles
		`class="tl-goals"`,               // pinned right goal anchors
		`id="graph-zoom"`,                // time-density zoom control
		`class="graph-svg labels-hover"`, // decluttered canvas, hover-reveal labels
		`id="graph-controls"`,            // client search + edge toggles
		`/static/graph.js`,               // progressive-enhancement layer
		"Ship Slice B",                   // the goal anchors a lane
	} {
		if !strings.Contains(body, want) {
			t.Errorf("graph home missing %q", want)
		}
	}
}

// TestGraph_ZoomDensityWired — the timeline's px-per-day density comes from
// ?ppd= (the client zoom control re-requests at a new density via fetch). The
// handler defaults to 160 and threads a requested value (clamped) into the
// rendered canvas so the client can preserve scroll center across the swap.
func TestGraph_ZoomDensityWired(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedGoal(t, srv, ctx, "spanning goal")

	if got := getBody(t, srv, "/"); !strings.Contains(got, `data-ppd="160"`) {
		t.Errorf("default density should be 160, body lacked data-ppd=\"160\"")
	}
	if got := getBody(t, srv, "/?ppd=800"); !strings.Contains(got, `data-ppd="800"`) {
		t.Errorf("?ppd=800 should render data-ppd=\"800\"")
	}
	// out-of-range densities clamp rather than 500 (MaxPxPerDay = 6000).
	if got := getBody(t, srv, "/?ppd=99999"); !strings.Contains(got, `data-ppd="6000"`) {
		t.Errorf("?ppd=99999 should clamp to data-ppd=\"6000\"")
	}
}

// TestGraphRedirect — the old /graph URL 302s to the new home so existing
// bookmarks keep working, carrying any query string (repo / at) through.
func TestGraphRedirect(t *testing.T) {
	srv := newTestServer(t)

	rec := getRec(t, srv, "/graph?at=2026-06-08")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?at=2026-06-08" {
		t.Errorf("Location = %q, want /?at=2026-06-08", loc)
	}
}

// TestGraph_MaterializeToast — after materialize lands back on the graph
// (?materialized=N), the echo is a bottom-right toast, not a banner.
func TestGraph_MaterializeToast(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedGoal(t, srv, ctx, "anything")

	body := getBody(t, srv, "/?materialized=2")
	if !strings.Contains(body, `class="toast-stack"`) {
		t.Error("expected toast-stack on graph after materialize")
	}
	if !strings.Contains(body, "materialized 2 segments") {
		t.Errorf("toast copy missing: %q", body)
	}
}

// TestGraph_GoalStatusBorderClass — goal status drives a border treatment
// (status-<x> class), never a fill colour (grill §3.3). Proves the class
// reaches the rendered shape so the CSS can style it.
func TestGraph_GoalStatusBorderClass(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	g := seedGoal(t, srv, ctx, "in-flight goal")
	if _, err := srv.svc.SetGoalStatus(ctx, g.ID, domain.Status("in_progress")); err != nil {
		t.Fatalf("set status: %v", err)
	}

	body := getBody(t, srv, "/")
	if !strings.Contains(body, "status-in_progress") {
		t.Errorf("goal-status border class missing: %q", body)
	}
}
