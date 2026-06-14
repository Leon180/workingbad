package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

func postForm(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

func TestNodeCreate_RoundTrip(t *testing.T) {
	srv := newTestServer(t)
	rec := postForm(t, srv, "/nodes", url.Values{
		"type":  {"research"},
		"title": {"manually curated node"},
		"body":  {"hand-written body"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: want 303, got %d (%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/nodes/") {
		t.Fatalf("create redirect = %q, want /nodes/{id}", loc)
	}
	body := getBody(t, srv, loc)
	if !strings.Contains(body, "manually curated node") || !strings.Contains(body, "hand-written body") {
		t.Error("created node not rendered on its detail page")
	}
}

func TestNodeCreate_RequiresTitle(t *testing.T) {
	srv := newTestServer(t)
	rec := postForm(t, srv, "/nodes", url.Values{"type": {"research"}, "title": {"  "}})
	if rec.Code != http.StatusOK {
		t.Fatalf("blank title should re-render form (200), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "title is required") {
		t.Error("missing 'title is required' message")
	}
	n, _ := srv.svc.CountNodes(context.Background())
	if n != 0 {
		t.Errorf("no node should be created on blank title, got %d", n)
	}
}

func TestNodeCreate_GoalWithStatus(t *testing.T) {
	srv := newTestServer(t)
	rec := postForm(t, srv, "/nodes", url.Values{
		"type": {"goal"}, "title": {"ship it"}, "status": {"in_progress"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("goal create: want 303, got %d (%s)", rec.Code, rec.Body.String())
	}
	live, err := srv.svc.SearchNodes(context.Background(), "ship", 5)
	if err != nil || len(live) != 1 || live[0].Status != domain.StatusInProgress {
		t.Errorf("goal node = %v err=%v, want status in_progress", live, err)
	}
}

func TestNodeCreate_RejectsBadType(t *testing.T) {
	srv := newTestServer(t)
	rec := postForm(t, srv, "/nodes", url.Values{"type": {"banana"}, "title": {"x"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad type: want 400, got %d", rec.Code)
	}
}

func TestNodeEdit_SupersedesAndRedirects(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	n := seedNode(t, srv, ctx, domain.EntryTypeResearch, "v1 title")

	// The edit form is prefilled with the live version.
	form := getBody(t, srv, "/nodes/"+n.ID+"/edit")
	if !strings.Contains(form, "v1 title") || !strings.Contains(form, `name="expected_version" value="1"`) {
		t.Error("edit form not prefilled with live version + expected_version")
	}

	rec := postForm(t, srv, "/nodes/"+n.ID, url.Values{
		"type": {"research"}, "title": {"v2 title"}, "expected_version": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: want 303, got %d (%s)", rec.Code, rec.Body.String())
	}
	live, err := srv.svc.GetLiveNodeByLogicalID(ctx, n.LogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Title != "v2 title" || live.Version != 2 {
		t.Errorf("live node = (%s v%d), want (v2 title, v2)", live.Title, live.Version)
	}
}

func TestNodeEdit_VersionConflict(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	n := seedNode(t, srv, ctx, domain.EntryTypeResearch, "original")

	// Stale expected_version → conflict, re-render, no write.
	rec := postForm(t, srv, "/nodes/"+n.ID, url.Values{
		"type": {"research"}, "title": {"attempted edit"}, "expected_version": {"99"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("conflict should re-render (200), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "changed since") {
		t.Error("conflict banner missing")
	}
	live, _ := srv.svc.GetLiveNodeByLogicalID(ctx, n.LogicalID)
	if live.Version != 1 || live.Title != "original" {
		t.Errorf("conflict must not write: live = (%s v%d)", live.Title, live.Version)
	}
}

func TestNodeEdit_EditsLiveFromStaleURL(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	v1 := seedNode(t, srv, ctx, domain.EntryTypeDecision, "decision v1")
	v2, err := srv.svc.SupersedeNode(ctx, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeDecision, Title: "decision v2",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Opening the edit form on the stale v1 URL must show the live v2 + post to it.
	form := getBody(t, srv, "/nodes/"+v1.ID+"/edit")
	if !strings.Contains(form, "decision v2") {
		t.Error("stale edit URL should render the live version")
	}
	if !strings.Contains(form, `action="/nodes/`+v2.ID+`"`) {
		t.Error("edit form should post to the live node id")
	}
	if !strings.Contains(form, `name="expected_version" value="2"`) {
		t.Error("edit form should carry the live version number")
	}
}

func TestNodeUpdate_NotFound(t *testing.T) {
	srv := newTestServer(t)
	rec := postForm(t, srv, "/nodes/0192f6c0-7e31-7c2b-9b8a-ffffffffffff", url.Values{
		"type": {"research"}, "title": {"x"}, "expected_version": {"1"},
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("update unknown id: want 404, got %d", rec.Code)
	}
}
