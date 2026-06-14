package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Leon180/workingbad/internal/domain"
)

// InTx commits every write together. Composes a split-like step (a node + an
// entry→node mapping) and asserts both landed.
func TestInTx_CommitsComposedWrites(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	e := seedActivity(t, s, "an entry")

	var nodeID string
	if err := s.InTx(c, func(ctx context.Context, tx *Tx) error {
		n, err := tx.CreateNode(ctx, domain.Node{Type: domain.EntryTypeActivity, Title: "split node"})
		if err != nil {
			return err
		}
		nodeID = n.ID
		return tx.MapEntryToNode(ctx, e.ID, n.ID)
	}); err != nil {
		t.Fatalf("InTx: %v", err)
	}

	if _, err := s.GetNode(c, nodeID); err != nil {
		t.Errorf("node should be committed: %v", err)
	}
	nodes, err := s.NodesForEntry(c, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == nodeID {
			found = true
		}
	}
	if !found {
		t.Errorf("mapping should be committed: NodesForEntry(%s) = %v", e.ID, nodes)
	}
}

// fn's error rolls the whole transaction back — the node created before the
// error must not persist.
func TestInTx_RollsBackOnError(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	boom := errors.New("boom")

	var createdID string
	err := s.InTx(c, func(ctx context.Context, tx *Tx) error {
		n, e := tx.CreateNode(ctx, domain.Node{Type: domain.EntryTypeResearch, Title: "rollback me"})
		if e != nil {
			return e
		}
		createdID = n.ID
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx should return fn's error unwrapped, got %v", err)
	}
	if _, e := s.GetNode(c, createdID); !errors.Is(e, ErrNotFound) {
		t.Errorf("node should have rolled back, GetNode = %v", e)
	}
	if cnt, cerr := s.CountNodes(c); cerr != nil {
		t.Fatal(cerr)
	} else if cnt != 0 {
		t.Errorf("expected 0 nodes after rollback, got %d", cnt)
	}
}

// A split that writes several nodes and then fails on a bad mapping must roll
// back ALL of them — the atomicity guarantee the orchestrator depends on.
func TestInTx_SplitPartialFailureRollsBackAll(t *testing.T) {
	s := newService(t)
	c := ctx(t)
	err := s.InTx(c, func(ctx context.Context, tx *Tx) error {
		if _, e := tx.CreateNode(ctx, domain.Node{Type: domain.EntryTypeResearch, Title: "n1"}); e != nil {
			return e
		}
		if _, e := tx.CreateNode(ctx, domain.Node{Type: domain.EntryTypeResearch, Title: "n2"}); e != nil {
			return e
		}
		// Map to a non-existent entry → ErrNotFound → the whole tx rolls back.
		return tx.MapEntryToNode(ctx, "no-such-entry", "no-such-node")
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if cnt, cerr := s.CountNodes(c); cerr != nil {
		t.Fatal(cerr)
	} else if cnt != 0 {
		t.Errorf("partial-failure split must roll back all nodes, got %d", cnt)
	}
}

// Calling a Service write inside fn re-enters InTx, which must fail fast (the
// single-connection pool would otherwise deadlock) — not hang.
func TestInTx_NestedRejected(t *testing.T) {
	s := newService(t)
	err := s.InTx(ctx(t), func(c context.Context, _ *Tx) error {
		_, e := s.CreateNode(c, domain.Node{Type: domain.EntryTypeResearch, Title: "nested"})
		return e
	})
	if err == nil || !strings.Contains(err.Error(), "InTx called within InTx") {
		t.Fatalf("nested InTx should fail fast, got %v", err)
	}
	// Nothing should have been written.
	if cnt, cerr := s.CountNodes(ctx(t)); cerr != nil || cnt != 0 {
		t.Errorf("nested-InTx rejection should write nothing, count=%d err=%v", cnt, cerr)
	}
}

// BeginTx fails on an already-cancelled context; InTx surfaces it.
func TestInTx_CancelledContext(t *testing.T) {
	s := newService(t)
	c, cancel := context.WithCancel(ctx(t))
	cancel()
	if err := s.InTx(c, func(context.Context, *Tx) error { return nil }); err == nil {
		t.Error("expected an error from BeginTx on a cancelled context")
	}
}
