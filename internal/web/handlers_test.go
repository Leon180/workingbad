package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestIndex_ListsEntries seeds three entries of different types and asserts
// the index renders all three. Verifies template binding + repository
// wiring at once.
func TestIndex_ListsEntries(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	seedEntry(t, srv, ctx, domain.EntryTypeResearch, "Investigate sqlc")
	seedEntry(t, srv, ctx, domain.EntryTypeDecision, "Pick modernc.org/sqlite")
	seedGoal(t, srv, ctx, "Ship Slice B")

	body := getBody(t, srv, "/entries")
	for _, want := range []string{
		"Investigate sqlc",
		"Pick modernc.org/sqlite",
		"Ship Slice B",
		"research",
		"decision",
		"goal",
		"open", // default goal status
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/ missing %q", want)
		}
	}
}

// TestIndex_TypeFilter — ?type=goal narrows the result set; other types
// must disappear from the response body.
func TestIndex_TypeFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedEntry(t, srv, ctx, domain.EntryTypeResearch, "research-only-row")
	seedGoal(t, srv, ctx, "goal-only-row")

	body := getBody(t, srv, "/entries?type=goal")
	if !strings.Contains(body, "goal-only-row") {
		t.Error("type=goal should include the goal entry")
	}
	if strings.Contains(body, "research-only-row") {
		t.Error("type=goal must exclude research entries")
	}
}

// TestIndex_TypeFilter_IgnoresBogus — unknown types fall back to "all"
// rather than 400; UI errors on garbage input are noisier than helpful.
func TestIndex_TypeFilter_IgnoresBogus(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedGoal(t, srv, ctx, "ship-it")

	rec := getRec(t, srv, "/entries?type=banana")
	if rec.Code != http.StatusOK {
		t.Errorf("bogus type should be ignored, got status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ship-it") {
		t.Error("bogus type filter should fall through to all-entries")
	}
}

// TestIndex_TimeTravelShowsHistoricalVersion proves the bitemporal payoff
// at the Web UI layer: create entry v1, supersede it to v2, list with
// ?at=<between> must show v1's title and a banner indicating snapshot mode.
func TestIndex_TimeTravelShowsHistoricalVersion(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	v1 := seedEntry(t, srv, ctx, domain.EntryTypeResearch, "Initial finding")
	between := v1.IngestedAt.Add(10 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if _, err := srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeResearch, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "tt-2",
		Title: "Revised finding",
	}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	body := getBody(t, srv, "/entries?at="+between.Format(time.RFC3339Nano))
	if !strings.Contains(body, "Initial finding") {
		t.Errorf("at-time view should show v1 title — body=%q", body)
	}
	if strings.Contains(body, "Revised finding") {
		t.Errorf("at-time view should NOT show v2 (it didn't exist yet)")
	}
	if !strings.Contains(body, "viewing state at") {
		t.Error("time-travel banner missing")
	}
}

// TestIndex_AtParseError surfaces a friendly banner and falls back to
// live entries instead of 500ing.
func TestIndex_AtParseError(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedEntry(t, srv, ctx, domain.EntryTypeResearch, "live-row")

	body := getBody(t, srv, "/entries?at=banana")
	if !strings.Contains(body, "at parse error") {
		t.Error("expected parse error banner")
	}
	if !strings.Contains(body, "live-row") {
		t.Error("expected fallback to live entries")
	}
}

// TestEntryDetail_RendersHistory creates v1+v2 of an entry and asserts the
// detail page shows both versions with v2 marked current — the bitemporal
// "git log <file>" equivalent.
func TestEntryDetail_RendersHistory(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	v1 := seedEntry(t, srv, ctx, domain.EntryTypeDecision, "Pick SQLite")
	v2, err := srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "pick-2",
		Title: "Pick SQLite (revised)",
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	// Both id and logical_id should resolve to the same detail page.
	for _, lookup := range []string{v2.ID, v1.LogicalID, v1.ID} {
		t.Run("lookup="+lookup[:8], func(t *testing.T) {
			body := getBody(t, srv, "/entries/"+lookup)
			if !strings.Contains(body, "Pick SQLite (revised)") {
				t.Errorf("missing live title")
			}
			if !strings.Contains(body, "Pick SQLite") {
				t.Errorf("missing original title in history")
			}
			if !strings.Contains(body, "v2") || !strings.Contains(body, "v1") {
				t.Errorf("history table missing version numbers")
			}
			if !strings.Contains(body, "current") {
				t.Errorf("missing current marker")
			}
		})
	}
}

func TestEntryDetail_NotFound(t *testing.T) {
	srv := newTestServer(t)
	rec := getRec(t, srv, "/entries/no-such-id")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestGoalDetail_ShowsAttachedActivities — attach an activity to a goal,
// then GET /goals/{id} and assert the activity appears.
func TestGoalDetail_ShowsAttachedActivities(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Ship Slice B")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "Wire web list")

	if _, err := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}

	body := getBody(t, srv, "/goals/"+goal.ID)
	if !strings.Contains(body, "Ship Slice B") {
		t.Error("goal title missing")
	}
	if !strings.Contains(body, "Wire web list") {
		t.Error("attached activity missing")
	}
	if !strings.Contains(body, "attached activities (1)") {
		t.Errorf("expected '(1)' count, body: %q", body)
	}
}

func TestGoalDetail_RejectsNonGoal(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeResearch, "Not a goal")

	rec := getRec(t, srv, "/goals/"+e.ID)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-goal, got %d", rec.Code)
	}
}

// TestNewForm_RendersEmpty seeds nothing then GETs the form page; it must
// 200 and contain the type-specific copy.
func TestNewForm_RendersEmpty(t *testing.T) {
	srv := newTestServer(t)
	for _, typ := range []string{"research", "decision", "goal"} {
		t.Run(typ, func(t *testing.T) {
			body := getBody(t, srv, "/new/"+typ)
			if !strings.Contains(body, "new "+typ) {
				t.Errorf("missing heading for type %q", typ)
			}
			if !strings.Contains(body, `name="title"`) {
				t.Error("title field missing")
			}
		})
	}
}

func TestNewForm_RejectsUnknownType(t *testing.T) {
	srv := newTestServer(t)
	rec := getRec(t, srv, "/new/banana")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown type, got %d", rec.Code)
	}
}

// TestNewSubmit_CreatesAndRedirects walks the happy path: POST title+body,
// follow the redirect, confirm the entry is persisted + visible.
func TestNewSubmit_CreatesAndRedirects(t *testing.T) {
	srv := newTestServer(t)
	form := "title=Investigate+sqlc&body=Worth+the+setup+pain%3F"
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/new/research",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/entries/") {
		t.Errorf("redirect target wrong: %q", loc)
	}

	// Follow redirect — entry must be reachable.
	body := getBody(t, srv, loc)
	if !strings.Contains(body, "Investigate sqlc") {
		t.Errorf("created entry not visible on follow: %q", body)
	}
}

// TestNewSubmit_GoalRedirectsToGoalDetail — goals go to /goals/{id} so the
// attached-activities view is the first thing the engineer sees after
// creating one.
func TestNewSubmit_GoalRedirectsToGoalDetail(t *testing.T) {
	srv := newTestServer(t)
	form := "title=Ship+Slice+B"
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/new/goal",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/goals/") {
		t.Errorf("goal redirect target wrong: %q", loc)
	}
}

// TestNewSubmit_RequiresTitle — empty title re-renders the form with the
// error banner + the body preserved.
func TestNewSubmit_RequiresTitle(t *testing.T) {
	srv := newTestServer(t)
	form := "title=&body=Body+without+a+title"
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/new/research",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (re-render), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "title is required") {
		t.Errorf("missing error banner")
	}
	if !strings.Contains(rec.Body.String(), "Body without a title") {
		t.Error("body should be preserved on re-render")
	}
}

// TestGoalStatus_SupersedesAndRedirects — POST status change creates a new
// goal version and redirects to the new id (so the URL bar reflects the
// live row).
func TestGoalStatus_SupersedesAndRedirects(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Ship Slice B")

	form := "status=in_progress"
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+goal.ID+"/status", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/goals/") || loc == "/goals/"+goal.ID {
		t.Errorf("redirect should point at a new goal id, got %q", loc)
	}

	body := getBody(t, srv, loc)
	if !strings.Contains(body, "in_progress") {
		t.Error("new goal status not visible")
	}
}

// TestGoalAttach_LinksEntryAndRedirects — only activity entries surface
// on the goal detail page (GetGoalActivities filters to type='activity'),
// so the test attaches one of those.
func TestGoalAttach_LinksEntryAndRedirects(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Goal")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "Some activity")

	form := "entry_id=" + activity.ID
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+goal.ID+"/attach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	body := getBody(t, srv, "/goals/"+goal.ID)
	if !strings.Contains(body, "Some activity") {
		t.Errorf("attached activity not visible: %q", body)
	}
}

// TestEdgeDetach_RemovesEdgeFromGoalDetail
func TestEdgeDetach_RemovesEdgeFromGoalDetail(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Goal")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "Wired")

	edge, err := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	form := "goal_id=" + goal.ID
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/edges/"+edge.ID+"/detach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}

	body := getBody(t, srv, "/goals/"+goal.ID)
	if strings.Contains(body, "Wired") {
		t.Errorf("detached activity should not appear: %q", body)
	}
	if !strings.Contains(body, "attached activities (0)") {
		t.Error("count should be 0 after detach")
	}
}

// TestGoalAttach_MissingEntryID_400
func TestGoalAttach_MissingEntryID_400(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "G")

	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+goal.ID+"/attach", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing entry_id, got %d", rec.Code)
	}
}

// TestHeader_NoPendingCTAWhenZero — clean DB shows no CTA in the nav bar.
func TestHeader_NoPendingCTAWhenZero(t *testing.T) {
	srv := newTestServer(t)
	body := getBody(t, srv, "/")
	if strings.Contains(body, "materialize now") {
		t.Error("materialize CTA should not appear when nothing is pending")
	}
}

// TestHeader_ShowsPendingCTAWhenSegmentsExist — seed a pending segment,
// then load the index and assert the CTA + count are rendered.
func TestHeader_ShowsPendingCTAWhenSegmentsExist(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedPendingSegment(t, srv, ctx, "repo-1", "ref-tt-pending", "patch-tt", "sha-tt")

	body := getBody(t, srv, "/")
	if !strings.Contains(body, "materialize now") {
		t.Error("materialize CTA missing")
	}
	if !strings.Contains(body, "1 pending segment") {
		t.Errorf("count missing or wrong: %q", body)
	}
}

// TestMaterialize_ProcessesAndRedirects — POST /materialize runs
// BatchMaterialize and redirects with the result in the query string.
func TestMaterialize_ProcessesAndRedirects(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedPendingSegment(t, srv, ctx, "repo-2", "ref-mat", "patch-mat", "sha-mat")

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/materialize", nil)
	req.Header.Set("Referer", "http://127.0.0.1/")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "materialized=1") {
		t.Errorf("redirect should carry materialized count, got %q", loc)
	}

	// Pending count should drop to 0 after materialize.
	body := getBody(t, srv, "/")
	if strings.Contains(body, "materialize now") {
		t.Error("pending CTA should be gone after materialize")
	}
}

// seedPendingSegment is the test helper for the materialize tests. Inserts
// a segment + a raw commit + the link row so BatchMaterialize has work
// to do. UpsertSegment returns the segment with its assigned id, so we
// don't need to reach into the repository's private *sql.DB to look it
// up.
func seedPendingSegment(t *testing.T, srv *Server, ctx context.Context, repo, sourceRef, patchID, sha string) {
	t.Helper()
	seg, err := srv.svc.UpsertSegment(ctx, domain.Segment{
		RepoID:        repo,
		Source:        domain.SourceGit,
		SourceRef:     sourceRef,
		AnchorPatchID: patchID,
	})
	if err != nil {
		t.Fatalf("UpsertSegment: %v", err)
	}
	now := time.Now().UTC()
	ch, err := srv.svc.UpsertRaw(ctx, domain.RawCommit{
		SHA: sha, RepoID: repo,
		Author: "alice", Committer: "alice",
		AuthorTime: now, CommitTime: now,
		Message: "seeded for materialize",
	}, patchID)
	if err != nil {
		t.Fatalf("UpsertRaw: %v", err)
	}
	if err := srv.svc.LinkSegmentRaw(ctx, seg.ID, ch.ChangeID); err != nil {
		t.Fatalf("LinkSegmentRaw: %v", err)
	}
}

// TestResolveLogical_BeyondScanLimit closes the architect-flagged latent
// 404 (PR #10 P1): pre-fix the resolveLogical fallback scanned
// ListEntries(Limit=1000) and silently 404'd at row 1001. The PK lookup
// via GetEntryLogicalIDByID is bounded by the entries primary key, so
// the lookup works at any scale.
//
// We don't seed 1001 entries (would slow CI); instead we seed >1 entries
// and supersede one of them, then look up by the SUPERSEDED id. The
// pre-fix scan would only find entries with is_current=1, so the
// superseded id would always 404. The new PK lookup resolves it.
func TestResolveLogical_BeyondScanLimit(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	v1, err := srv.svc.InsertEntry(ctx, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "scan-1",
		Title: "Original",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "scan-2",
		Title: "Revised",
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	// Look up the SUPERSEDED id directly. EntryHistory(v1.ID) returns
	// empty (v1.ID isn't a logical_id), so resolveLogical falls through
	// to GetEntryLogicalIDByID which finds it.
	body := getBody(t, srv, "/entries/"+v1.ID)
	if !strings.Contains(body, "Revised") {
		t.Errorf("resolveLogical should reach the live row from a superseded id, body=%q", body)
	}
	_ = v2 // appease unused-var
}

// TestIndex_EmptyState renders the friendly "no entries" copy + the CLI
// hint without crashing on a zero-row list.
func TestIndex_EmptyState(t *testing.T) {
	srv := newTestServer(t)
	body := getBody(t, srv, "/entries")
	if !strings.Contains(body, "No entries match") {
		t.Error("empty list should show empty-state copy")
	}
	if !strings.Contains(body, "workingbad note") {
		t.Error("empty state should hint at the CLI command")
	}
}

// --- helpers ---

func seedEntry(t *testing.T, srv *Server, ctx context.Context, typ domain.EntryType, title string) domain.Entry {
	t.Helper()
	e, err := srv.svc.InsertEntry(ctx, domain.Entry{
		Type: typ, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: title,
		Title: title,
	})
	if err != nil {
		t.Fatalf("InsertEntry(%s, %q): %v", typ, title, err)
	}
	return e
}

func seedGoal(t *testing.T, srv *Server, ctx context.Context, title string) domain.Entry {
	t.Helper()
	e, err := srv.svc.InsertEntry(ctx, domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: title,
		Title: title, Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("InsertEntry goal %q: %v", title, err)
	}
	return e
}

func getBody(t *testing.T, srv *Server, path string) string {
	t.Helper()
	return getRec(t, srv, path).Body.String()
}

func getRec(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code >= 500 {
		t.Fatalf("%s: 5xx response: %d, body: %s", path, rec.Code, mustRead(rec.Body))
	}
	return rec
}

func mustRead(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}
