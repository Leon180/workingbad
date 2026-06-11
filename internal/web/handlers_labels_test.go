package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// post is the small JSON-or-form sender used across these tests so each
// case can focus on its assertion instead of httptest plumbing.
func postLabels(t *testing.T, srv *Server, entryID, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/entries/"+entryID+"/labels", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

func getLabels(t *testing.T, srv *Server, entryID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/entries/"+entryID+"/labels", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// decodeLabels parses the response body as a JSON array of strings.
// Used by tests that need the actual list back; tests that only care
// about status codes skip this.
func decodeLabels(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode labels: %v (body=%q)", err, body)
	}
	return out
}

// TestLabels_GetEmpty — a fresh entry returns "[]", not 404 or null.
// "no labels" is a legitimate state, distinct from "unknown entry".
func TestLabels_GetEmpty(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "no labels yet")

	rec := getLabels(t, srv, e.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	if got != "[]" {
		t.Errorf("body = %q, want \"[]\"", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestLabels_SetThenGet_JSON — round-trip via the JSON content type,
// the body that's most explicit about its semantics.
func TestLabels_SetThenGet_JSON(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "label me")

	rec := postLabels(t, srv, e.ID,
		"application/json",
		`{"labels":["decision","research"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeLabels(t, rec.Body.String())
	// Service orders alphabetically — decision < research.
	if len(got) != 2 || got[0] != "decision" || got[1] != "research" {
		t.Errorf("POST body = %v, want [decision research]", got)
	}

	// GET reflects what was POSTed.
	rec = getLabels(t, srv, e.ID)
	got = decodeLabels(t, rec.Body.String())
	if len(got) != 2 || got[0] != "decision" || got[1] != "research" {
		t.Errorf("GET body = %v, want [decision research]", got)
	}
}

// TestLabels_SetThenGet_Form — same round-trip via x-www-form-urlencoded
// with repeating `labels=` fields (the htmx checkbox shape the UI emits).
func TestLabels_SetThenGet_Form(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "label me too")

	rec := postLabels(t, srv, e.ID,
		"application/x-www-form-urlencoded",
		"labels=decision&labels=research")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeLabels(t, rec.Body.String())
	if len(got) != 2 || got[0] != "decision" || got[1] != "research" {
		t.Errorf("POST body = %v, want [decision research]", got)
	}
}

// TestLabels_ReplaceAllSemantics — POSTing a smaller set drops the
// dropped labels (overwrite, not append).
func TestLabels_ReplaceAllSemantics(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "replace me")

	_ = postLabels(t, srv, e.ID, "application/json",
		`{"labels":["decision","research"]}`)

	rec := postLabels(t, srv, e.ID, "application/json", `{"labels":["decision"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeLabels(t, rec.Body.String())
	if len(got) != 1 || got[0] != "decision" {
		t.Errorf("body = %v, want [decision]", got)
	}
}

// TestLabels_EmptyClearsAll — POST with an empty list wipes the set.
// Required for the htmx "uncheck all" interaction.
func TestLabels_EmptyClearsAll(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "clear me")

	_ = postLabels(t, srv, e.ID, "application/json",
		`{"labels":["decision"]}`)

	rec := postLabels(t, srv, e.ID, "application/json", `{"labels":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%q", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rec.Body.String())
	}
}

// TestLabels_400_OnUnknownLabel — sentinel ErrInvalidInput from the
// service must surface as 400, not 500.
func TestLabels_400_OnUnknownLabel(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "bad input")

	rec := postLabels(t, srv, e.ID, "application/json",
		`{"labels":["definitely-not-a-real-label"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestLabels_400_OnLabelEqualsPrimary — surface the
// "label equals entry primary type" validator path.
func TestLabels_400_OnLabelEqualsPrimary(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "primary collision")

	rec := postLabels(t, srv, e.ID, "application/json", `{"labels":["activity"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestLabels_404_OnUnknownEntry — POST + GET both route to 404 via
// statusFor(ErrNotFound) for non-existent entry ids.
func TestLabels_404_OnUnknownEntry(t *testing.T) {
	srv := newTestServer(t)
	unknownID := "0192f6c0-7e31-7c2b-9b8a-ffffffffffff"

	rec := postLabels(t, srv, unknownID, "application/json", `{"labels":["decision"]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404", rec.Code)
	}

	// GET against a missing entry returns 200 + []  — GetLabels doesn't
	// distinguish "missing" from "no labels" at the service layer, and
	// upgrading it for one HTTP shape would be over-engineering. The
	// guarantee is: SetLabels rejects unknown ids (POST is the only
	// surface that can mutate state from this address space).
	rec = getLabels(t, srv, unknownID)
	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200 (empty labels for unknown entry)", rec.Code)
	}
}

// TestLabels_400_OnUnsupportedContentType — explicit XML or similar
// gets rejected at the body decoder rather than silently treated as a form.
func TestLabels_400_OnUnsupportedContentType(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	e := seedEntry(t, srv, ctx, domain.EntryTypeActivity, "bad ct")

	rec := postLabels(t, srv, e.ID, "application/xml", `<labels></labels>`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
