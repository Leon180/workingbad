package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// TestEntryDetail_TimeTravelShiftsFocalVersion is the architect-flagged
// gap (PR #10 P0): clicking an entry from a time-travel list view dropped
// the engineer back to "live" silently. This test asserts that ?at= on
// the entry-detail page shifts the focal row to whichever version was
// live at that instant, surfaces the time-travel banner, and re-marks
// the chain row.
func TestEntryDetail_TimeTravelShiftsFocalVersion(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

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

	v2, err := srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeDecision, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "d2",
		Title: "Pick SQLite (v2)",
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	// Live view: v2 in the header.
	body := getBody(t, srv, "/entries/"+v2.ID)
	if !strings.Contains(body, `<h1>Pick SQLite (v2)</h1>`) {
		t.Errorf("live: header should show v2, body=%q", body)
	}

	// ?at=<between>: v1 in the header, banner present, mutation-disabled.
	body = getBody(t, srv, "/entries/"+v2.ID+"?at="+between.Format(time.RFC3339Nano))
	if !strings.Contains(body, "viewing this entry as of") {
		t.Error("time-travel banner missing")
	}
	if !strings.Contains(body, `<h1>Pick SQLite (v1)</h1>`) {
		t.Errorf("at-time header should show v1, body=%q", body)
	}
	if !strings.Contains(body, "as of T") {
		t.Error("chain row should mark the focal version as 'as of T'")
	}
}

// TestEntryDetail_AtParseError surfaces a friendly banner and falls back
// to the live focal row instead of 500ing.
func TestEntryDetail_AtParseError(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeResearch, "Some note")

	body := getBody(t, srv, "/entries/"+e.ID+"?at=banana")
	if !strings.Contains(body, "at parse error") {
		t.Error("expected parse error banner")
	}
	if !strings.Contains(body, "Some note") {
		t.Error("should fall back to live entry")
	}
}

// TestGoalDetail_TimeTravelSuppressesMutations is the second half of the
// architect P0 fix: when ?at= is in play on the goal page, the status
// dropdown / attach form / per-row detach buttons all vanish so the user
// can't try to supersede the past.
func TestGoalDetail_TimeTravelSuppressesMutations(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Ship Slice B")
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "act")
	if _, err := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Live view: mutation forms present.
	body := getBody(t, srv, "/goals/"+goal.ID)
	for _, marker := range []string{
		`action="/goals/`,
		`name="status"`,
		`name="entry_id"`,
		`/edges/`, // detach form action prefix
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("live view missing mutation marker %q", marker)
		}
	}

	// ?at=<future>: still alive at that point but read-only.
	atStr := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	body = getBody(t, srv, "/goals/"+goal.ID+"?at="+atStr)
	if !strings.Contains(body, "mutations disabled") {
		t.Error("time-travel banner should warn 'mutations disabled'")
	}
	if strings.Contains(body, `action="/goals/`+goal.ID+`/status"`) {
		t.Error("status form should be suppressed in time-travel mode")
	}
	if strings.Contains(body, `name="entry_id"`) {
		t.Error("attach form should be suppressed in time-travel mode")
	}
}

// TestGoalDetail_TimeTravelEmptyHistoricalActivities — at a moment before
// any attach happened, GoalActivitiesAt should return zero rows and the
// page should show the empty state cleanly.
func TestGoalDetail_TimeTravelEmptyHistoricalActivities(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "Goal")
	beforeAttach := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	activity := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "act")
	if _, err := srv.svc.AttachToGoal(ctx, activity.ID, goal.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}

	body := getBody(t, srv, "/goals/"+goal.ID+"?at="+beforeAttach.Format(time.RFC3339Nano))
	if !strings.Contains(body, "attached activities (0)") {
		t.Errorf("expected empty count at past timestamp, body=%q", body)
	}
}

// TestLiveAtAsOf_TruthTable exercises the in-memory chain walker against
// a 3-version supersede chain.
func TestLiveAtAsOf_TruthTable(t *testing.T) {
	now := time.Now().UTC()
	chain := []domain.Entry{
		// version-DESC, newest first (matches EntryHistory ordering)
		{ID: "v3", Version: 3, IngestedAt: now.Add(2 * time.Hour), SupersededBy: ""},
		{ID: "v2", Version: 2, IngestedAt: now.Add(1 * time.Hour), SupersededBy: "v3"},
		{ID: "v1", Version: 1, IngestedAt: now, SupersededBy: "v2"},
	}

	cases := []struct {
		name string
		asOf time.Time
		want string
	}{
		{"before v1 exists", now.Add(-time.Hour), ""},
		{"during v1", now.Add(30 * time.Minute), "v1"},
		{"during v2", now.Add(90 * time.Minute), "v2"},
		{"during v3", now.Add(3 * time.Hour), "v3"},
		{"far future", now.Add(100 * time.Hour), "v3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := liveAtAsOf(chain, tc.asOf)
			if tc.want == "" {
				if ok {
					t.Errorf("expected no live row, got %s", got.ID)
				}
				return
			}
			if !ok {
				t.Fatalf("expected %s, got no result", tc.want)
			}
			if got.ID != tc.want {
				t.Errorf("expected %s, got %s", tc.want, got.ID)
			}
		})
	}
}
