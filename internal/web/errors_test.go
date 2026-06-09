package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/repository"
)

// TestStatusFor_SentinelMapping locks the error → HTTP status contract:
// pre-fix every mutation handler returned 400 for every error, which
// masked DB outages as client mistakes. Each sentinel now maps to its
// semantically correct code.
func TestStatusFor_SentinelMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil → 200", nil, http.StatusOK},
		{"ErrNotFound → 404", repository.ErrNotFound, http.StatusNotFound},
		{"ErrNotFound wrapped → 404", fmt.Errorf("goal X: %w", repository.ErrNotFound), http.StatusNotFound},
		{"ErrInvalidInput → 400", repository.ErrInvalidInput, http.StatusBadRequest},
		{"ErrInvalidInput wrapped → 400", fmt.Errorf("bad status: %w", repository.ErrInvalidInput), http.StatusBadRequest},
		{"ErrVersionConflict → 409", repository.ErrVersionConflict, http.StatusConflict},
		{"context.Canceled → 499", context.Canceled, 499},
		{"context.DeadlineExceeded → 499", context.DeadlineExceeded, 499},
		{"unclassified → 500", errors.New("DB locked"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusFor(tc.err); got != tc.want {
				t.Errorf("statusFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestGoalStatus_NotFoundReturns404 — POST status change on a goal id
// that doesn't exist now produces 404, not 400. (Live probe confirmed
// the pre-fix bug; this is the regression test.)
func TestGoalStatus_NotFoundReturns404(t *testing.T) {
	srv := newTestServer(t)
	bogus := "00000000-0000-7000-8000-000000000000"

	form := "status=done"
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+bogus+"/status", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("non-existent goal: status=%d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestGoalStatus_InvalidStatusReturns400 — POST status=banana is genuine
// caller input error, must remain 400.
func TestGoalStatus_InvalidStatusReturns400(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "G")

	form := "status=banana"
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+goal.ID+"/status", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid status: status=%d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestDetach_NotFoundReturns404
func TestDetach_NotFoundReturns404(t *testing.T) {
	srv := newTestServer(t)
	bogus := "00000000-0000-7000-8000-000000000000"

	form := "goal_id=" + bogus
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/edges/"+bogus+"/detach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("non-existent edge: status=%d, want 404", rec.Code)
	}
}

// TestAttach_NotCurrentReturns404 — attaching a superseded activity
// (no longer the live row) is "not found" semantically.
func TestAttach_NotCurrentReturns404(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	goal := seedGoal(t, srv, ctx, "G")
	v1 := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "v1")
	// Supersede v1 so its id is no longer the live row.
	if _, err := srv.svc.Supersede(ctx, v1.ID, v1.Version, domain.Entry{
		Type: domain.EntryTypeActivity, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "v2",
		Title: "v2",
	}); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	form := "entry_id=" + v1.ID
	req := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1/goals/"+goal.ID+"/attach", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("superseded activity: status=%d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}
