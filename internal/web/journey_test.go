package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestWebJourney_FullDogfoodingLoop is the Slice B canonical journey:
// every key surface exercised end-to-end through the same handler chain
// a browser drives, with no shortcuts into the repository.
//
// The flow:
//  1. POST /new/goal creates a goal via the web form
//  2. GET / lists it; type=goal filter narrows; goal title is linked
//  3. Seed a pending segment (Phase 2 git ingest doesn't exist yet so we
//     drive UpsertSegment + UpsertRaw directly; the materialize POST
//     itself goes through HTTP)
//  4. GET / shows the pending CTA in the header
//  5. POST /materialize runs the materialize loop; redirect carries
//     materialized=1 in the query string; index shows the success banner
//  6. GET / lists the freshly-synthesised activity
//  7. POST /goals/{id}/attach links it to the goal
//  8. GET /goals/{id} shows the attached activity
//  9. POST /goals/{id}/status flips the goal to done; redirect target
//     is the new (superseded) goal id
//  10. After supersede, the goal page at the NEW id still sees the
//     activity attached (because rePointAllLiveEdges from PR #9 carries
//     the part_of edge over) — the bitemporal supersede regression test
//     at the UI layer.
//  11. GET / shows the goal as status=done
//
// If any of those steps regresses, Slice B's dogfooding loop is broken
// from the UI perspective and the test should fail loudly.
func TestWebJourney_FullDogfoodingLoop(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// 1. Create goal via web form.
	goalForm := "title=Ship+Slice+B"
	rec := doPostForm(t, srv, "/new/goal", goalForm)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create goal: status=%d", rec.Code)
	}
	goalURL := rec.Header().Get("Location")
	goalID := strings.TrimPrefix(goalURL, "/goals/")
	if goalID == "" || goalID == goalURL {
		t.Fatalf("malformed redirect: %q", goalURL)
	}

	// 2. List page shows the goal under type=goal filter.
	body := getBody(t, srv, "/entries?type=goal")
	if !strings.Contains(body, "Ship Slice B") {
		t.Errorf("created goal not visible in list: %q", body)
	}

	// 3. Seed pending segment (no HTTP route for git ingest yet — Phase 2).
	seedPendingSegment(t, srv, ctx, "repo-journey", "ref-journey", "patch-journey", "sha-journey")

	// 4. Header CTA visible on next render.
	body = getBody(t, srv, "/entries")
	if !strings.Contains(body, "1 pending segment") {
		t.Errorf("pending CTA missing: %q", body)
	}

	// 5. Materialize through the POST endpoint.
	rec = doPostForm(t, srv, "/materialize", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("materialize: status=%d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Location"), "materialized=1") {
		t.Errorf("materialize redirect missing flash: %q", rec.Header().Get("Location"))
	}

	// 6. New activity appears in the list (under type=activity).
	body = getBody(t, srv, "/entries?type=activity")
	if !strings.Contains(body, "activity") {
		t.Errorf("materialized activity not visible: %q", body)
	}

	// Locate the activity id via the repository (the alternative would be
	// HTML scraping which is fragile; the goal here is to verify the
	// surface, not the parser).
	activities, err := srv.svc.ListEntries(ctx, listFilterActivity())
	if err != nil || len(activities) == 0 {
		t.Fatalf("locate activity: %v / count=%d", err, len(activities))
	}
	activityID := activities[0].ID

	// 7. Attach via web POST.
	rec = doPostForm(t, srv, "/goals/"+goalID+"/attach", "entry_id="+activityID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attach: status=%d, body=%s", rec.Code, rec.Body.String())
	}

	// 8. Goal page shows the activity attached.
	body = getBody(t, srv, "/goals/"+goalID)
	if !strings.Contains(body, "attached activities (1)") {
		t.Errorf("attach not reflected on goal page: %q", body)
	}

	// 9. Status → done; redirect lands on the new live goal id.
	rec = doPostForm(t, srv, "/goals/"+goalID+"/status", "status=done")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status change: status=%d", rec.Code)
	}
	newGoalURL := rec.Header().Get("Location")
	if newGoalURL == "/goals/"+goalID {
		t.Errorf("supersede should mint a new id; got same URL %q", newGoalURL)
	}

	// 10. New (superseded) goal still sees the activity — bitemporal
	// supersede contract from PR #9, tested through the UI now.
	body = getBody(t, srv, newGoalURL)
	if !strings.Contains(body, "attached activities (1)") {
		t.Errorf("post-supersede goal lost its activity (rePoint regression): %q", body)
	}
	if !strings.Contains(body, "done") {
		t.Errorf("new goal status missing: %q", body)
	}

	// 11. The live list reflects the done status.
	body = getBody(t, srv, "/entries?type=goal")
	if !strings.Contains(body, "done") {
		t.Errorf("list view didn't pick up done status: %q", body)
	}
}

// TestWebJourney_TimeTravelMirrorsBitemporalState exercises the Web UI
// time-travel pathway: create v1 of an entry, supersede to v2, then
// list with ?at=<between> must show v1 and the time-travel banner.
//
// This is the canonical "git-like time travel works in the browser"
// assertion: failure here means the bitemporal foundation has shifted
// or the UI plumbing has regressed.
func TestWebJourney_TimeTravelMirrorsBitemporalState(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Two manual entries: one stable, one we'll supersede mid-journey.
	stable := seedEntry(t, srv, ctx, domain.EntryTypeResearch, "stable-baseline")
	_ = stable
	v1, err := srv.svc.InsertEntry(ctx, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d1",
		Title: "Pick SQLite (v1)",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	between := v1.IngestedAt.Add(20 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	_, err = srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d2",
		Title: "Pick SQLite (v2)",
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	// Live view shows v2.
	body := getBody(t, srv, "/entries?type=decision")
	if !strings.Contains(body, "Pick SQLite (v2)") {
		t.Errorf("live should show v2: %q", body)
	}

	// At-time view shows v1 + the banner.
	body = getBody(t, srv, "/entries?type=decision&at="+between.UTC().Format(time.RFC3339Nano))
	if !strings.Contains(body, "viewing state at") {
		t.Error("missing time-travel banner")
	}
	if !strings.Contains(body, "Pick SQLite (v1)") {
		t.Errorf("at-time should show v1: %q", body)
	}
	if strings.Contains(body, "Pick SQLite (v2)") {
		t.Error("at-time should NOT show v2 (it didn't exist yet)")
	}
}

// --- helpers shared by journey tests ---

func doPostForm(t *testing.T, srv *Server, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// listFilterActivity is a thin wrapper so the test doesn't have to import
// repository directly. Keeps the test focused on HTTP behaviour.
func listFilterActivity() repoFilterAlias {
	return repoFilterAlias{Type: domain.EntryTypeActivity, Limit: 10}
}

// repoFilterAlias mirrors repository.ListFilter at the field level so we
// can build one without bringing the repository import into this file
// just for the constructor. The type is converted to repository.ListFilter
// implicitly via a struct-literal in the test.
type repoFilterAlias = struct {
	Type            domain.EntryType
	RepoID          string
	Limit           int
	IncludeArchived bool
}
