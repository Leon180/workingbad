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

	body := getBody(t, srv, "/")
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

	body := getBody(t, srv, "/?type=goal")
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

	rec := getRec(t, srv, "/?type=banana")
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

	body := getBody(t, srv, "/?at="+between.Format(time.RFC3339Nano))
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

	body := getBody(t, srv, "/?at=banana")
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

// TestIndex_EmptyState renders the friendly "no entries" copy + the CLI
// hint without crashing on a zero-row list.
func TestIndex_EmptyState(t *testing.T) {
	srv := newTestServer(t)
	body := getBody(t, srv, "/")
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
