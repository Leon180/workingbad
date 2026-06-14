package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/domain"
)

// seedNode creates a node directly through the service (the node layer has no
// auto-1:1-on-entry-write; nodes come from backfill / manual ops / the future
// LLM pipeline). opts mutate the node before insert.
func seedNode(t *testing.T, srv *Server, ctx context.Context, typ domain.EntryType, title string, opts ...func(*domain.Node)) domain.Node {
	t.Helper()
	n := domain.Node{Type: typ, Title: title}
	if typ == domain.EntryTypeGoal {
		n.Status = domain.StatusOpen
	}
	for _, o := range opts {
		o(&n)
	}
	out, err := srv.svc.CreateNode(ctx, n)
	if err != nil {
		t.Fatalf("seedNode %q: %v", title, err)
	}
	return out
}

func TestNodes_ListsNodes(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedNode(t, srv, ctx, domain.EntryTypeResearch, "node-investigate-vectors")
	seedNode(t, srv, ctx, domain.EntryTypeGoal, "node-ship-d2")

	body := getBody(t, srv, "/nodes")
	for _, want := range []string{"node-investigate-vectors", "node-ship-d2", "research", "goal", "open"} {
		if !strings.Contains(body, want) {
			t.Errorf("/nodes missing %q", want)
		}
	}
	// Node rows link to the node detail surface, not the entry/goal views.
	if !strings.Contains(body, `href="/nodes/`) {
		t.Error("/nodes rows should link to /nodes/{id}")
	}
}

func TestNodes_TypeFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedNode(t, srv, ctx, domain.EntryTypeResearch, "node-research-row")
	seedNode(t, srv, ctx, domain.EntryTypeGoal, "node-goal-row")

	body := getBody(t, srv, "/nodes?type=goal")
	if !strings.Contains(body, "node-goal-row") {
		t.Error("type=goal should include the goal node")
	}
	if strings.Contains(body, "node-research-row") {
		t.Error("type=goal must exclude research nodes")
	}
}

func TestNodes_Search(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	seedNode(t, srv, ctx, domain.EntryTypeResearch, "indexing strategy", func(n *domain.Node) {
		n.Body = "spike sqlite-vec embeddings"
	})
	seedNode(t, srv, ctx, domain.EntryTypeActivity, "unrelated chore")

	body := getBody(t, srv, "/nodes?q=embeddings")
	if !strings.Contains(body, "indexing strategy") {
		t.Error("search q=embeddings should match the body term")
	}
	if strings.Contains(body, "unrelated chore") {
		t.Error("search q=embeddings must exclude non-matching nodes")
	}
}

func TestNodes_TimeTravel(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v1 := seedNode(t, srv, ctx, domain.EntryTypeDecision, "decision-alpha", func(n *domain.Node) {
		n.OccurredAt = t0
		n.IngestedAt = t0
	})
	if _, err := srv.svc.SupersedeNode(ctx, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeDecision, Title: "decision-omega",
	}); err != nil {
		t.Fatal(err)
	}

	// Live: shows the current version.
	live := getBody(t, srv, "/nodes")
	if !strings.Contains(live, "decision-omega") || strings.Contains(live, "decision-alpha") {
		t.Error("live /nodes should show omega, not alpha")
	}
	// As of a day after v1 but before the supersede: shows the historical version.
	hist := getBody(t, srv, "/nodes?at=2026-01-02")
	if !strings.Contains(hist, "decision-alpha") || strings.Contains(hist, "decision-omega") {
		t.Error("/nodes?at=2026-01-02 should show alpha, not omega")
	}
}

func TestNodeDetail_RendersHistory(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	v1 := seedNode(t, srv, ctx, domain.EntryTypeGoal, "goal-v1")
	v2, err := srv.svc.SupersedeNode(ctx, v1.ID, v1.Version, domain.Node{
		Type: domain.EntryTypeGoal, Title: "goal-v2", Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatal(err)
	}

	body := getBody(t, srv, "/nodes/"+v2.ID)
	for _, want := range []string{"goal-v2", "goal-v1", "v2", "v1", "in_progress"} {
		if !strings.Contains(body, want) {
			t.Errorf("/nodes/%s missing %q", v2.ID, want)
		}
	}
}

func TestNodeDetail_NotFound(t *testing.T) {
	srv := newTestServer(t)
	rec := getRec(t, srv, "/nodes/0192f6c0-7e31-7c2b-9b8a-ffffffffffff")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown node id: got status %d, want 404", rec.Code)
	}
}
