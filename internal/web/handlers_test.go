package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
