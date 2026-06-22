package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/ports/ai"
)

// OrchestrateResult summarizes one pipeline run. For its split+persist outcome
// an entry lands in exactly one bucket — Skipped (already split), EntriesProcessed
// (succeeded), or Queued with step "split"/"aggregate" (failed, no writes) — and
// may ALSO appear in Queued with step "relate" when persist succeeded but the
// later relate step failed (its nodes still stand).
type OrchestrateResult struct {
	EntriesProcessed int // entries whose split + persist succeeded this run
	Skipped          int // entries already mapped to nodes (lazy: not re-split)
	NodesCreated     int
	EdgesProposed    int // proposed by relate; persistence is F8
	Queued           []QueuedEntry
}

// QueuedEntry records an entry whose pipeline run halted, to be retried later.
// Locked Slice-F decision: halt + queue, never leave a partial graph for the
// failing entry. Step is where it halted: "split" | "aggregate" | "relate".
type QueuedEntry struct {
	EntryID string
	Step    string
	Err     string
}

// Orchestrate runs the Slice-F LLM pipeline over the supplied entries:
//
//  1. split     — provider.Split(entry) → node drafts        (Step 1)
//  2. aggregate — cluster drafts into nodes                  (Step 2; F7 — identity for now)
//  3. relate    — provider.Relate(entry, candidates) → edges (Step 3; F8 resolves + persists)
//
// Each entry's node writes commit atomically via InTx, so a split yielding N
// nodes + N entry→node maps is all-or-nothing. Provider calls run OUTSIDE InTx
// (the pool is single-connection — see InTx). Failure policy: a provider failure
// or an invalid draft halts that entry and records it in Queued (the run
// continues); a real storage error aborts the whole run, since it signals a
// broken invariant rather than a transient model hiccup.
//
// The caller selects the entry set (e.g. ListEntries by repo). The mock makes
// this deterministic end-to-end; real providers (F9) add a JSON-repair attempt
// before queuing a malformed response.
func (s *Service) Orchestrate(ctx context.Context, entries []domain.Entry, provider ai.AIProvider) (OrchestrateResult, error) {
	var res OrchestrateResult
	for _, e := range entries {
		out, err := s.orchestrateEntry(ctx, e, provider)
		if err != nil {
			return res, err
		}
		if out.skipped {
			res.Skipped++
			continue
		}
		if out.processed {
			res.EntriesProcessed++
		}
		res.NodesCreated += out.nodesCreated
		res.EdgesProposed += out.edgesProposed
		if out.queued != nil {
			res.Queued = append(res.Queued, *out.queued)
		}
	}
	return res, nil
}

// entryOutcome is the per-entry result orchestrateEntry reports back to the loop.
type entryOutcome struct {
	skipped       bool // already mapped to nodes; left untouched (lazy)
	processed     bool // split + persist succeeded
	nodesCreated  int
	edgesProposed int
	queued        *QueuedEntry
}

// orchestrateEntry runs the three steps for a single entry. A non-nil error is a
// storage failure that aborts the whole run; provider/validation failures are
// reported via outcome.queued and leave no partial writes for the entry.
func (s *Service) orchestrateEntry(ctx context.Context, e domain.Entry, provider ai.AIProvider) (entryOutcome, error) {
	// Lazy + idempotent: an entry already mapped to nodes is left untouched, so a
	// re-run neither re-pays the LLM split nor duplicates nodes. Entries are
	// immutable raw records — their node mapping is created once. The cheap
	// EXISTS guard runs before the expensive provider.Split.
	has, err := s.entryHasNodes(ctx, e.ID)
	if err != nil {
		return entryOutcome{}, fmt.Errorf("repository: orchestrate entry %s: check existing nodes: %w", e.ID, err)
	}
	if has {
		return entryOutcome{skipped: true}, nil
	}

	// Step 1 — split (outside the tx; provider may do I/O).
	drafts, err := provider.Split(ctx, e)
	if err != nil {
		return entryOutcome{queued: queued(e.ID, "split", err)}, nil
	}

	// Step 2 — aggregate. F7 clusters drafts across entries; the skeleton maps
	// each draft to one node (identity).
	nodes := make([]domain.Node, len(drafts))
	for i, d := range drafts {
		nodes[i] = draftToNode(e, d)
	}

	// Persist the entry's nodes + maps atomically. An invalid draft (e.g. empty
	// title) is a provider-quality failure → queue; other errors abort.
	if err := s.persistEntryNodes(ctx, e.ID, nodes); err != nil {
		if errors.Is(err, ErrInvalidInput) {
			return entryOutcome{queued: queued(e.ID, "aggregate", err)}, nil
		}
		// A real storage error aborts the run; name the entry so the caller can
		// tell where the partial run stopped (the failing entry's writes already
		// rolled back via InTx).
		return entryOutcome{}, fmt.Errorf("repository: orchestrate entry %s: %w", e.ID, err)
	}

	// Step 3 — relate (outside the tx). F8 resolves candidates + persists the
	// edges (Layer A/B/C); the skeleton wires the call and counts proposals.
	proposed, err := provider.Relate(ctx, e, nil)
	if err != nil {
		return entryOutcome{
			processed:    true,
			nodesCreated: len(nodes),
			queued:       queued(e.ID, "relate", err),
		}, nil
	}

	return entryOutcome{
		processed:     true,
		nodesCreated:  len(nodes),
		edgesProposed: len(proposed),
	}, nil
}

// entryHasNodes reports whether the entry already maps to ≥1 node, without
// loading the node rows. Keeps the split step lazy: the guard is one indexed
// EXISTS lookup, far cheaper than the LLM split call it gates.
func (s *Service) entryHasNodes(ctx context.Context, entryID string) (bool, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM entry_node_map WHERE entry_id = ?)`, entryID,
	).Scan(&exists); err != nil {
		return false, err
	}
	return exists != 0, nil
}

// persistEntryNodes creates the entry's nodes and their entry→node maps in one
// transaction, so the split's N writes commit or roll back as a unit.
func (s *Service) persistEntryNodes(ctx context.Context, entryID string, nodes []domain.Node) error {
	return s.InTx(ctx, func(ctx context.Context, t *Tx) error {
		for _, n := range nodes {
			created, err := t.CreateNode(ctx, n)
			if err != nil {
				return err
			}
			if err := t.MapEntryToNode(ctx, entryID, created.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// draftToNode builds a persistable Node from a split draft: OccurredAt is
// preserved from the source entry; all identity/version fields are assigned by
// CreateNode.
func draftToNode(e domain.Entry, d domain.NodeDraft) domain.Node {
	return domain.Node{
		Type:       d.Type,
		Title:      d.Title,
		Body:       d.Body,
		Status:     d.Status,
		OccurredAt: e.OccurredAt,
	}
}

// queued is a small constructor keeping the call sites terse.
func queued(entryID, step string, err error) *QueuedEntry {
	return &QueuedEntry{EntryID: entryID, Step: step, Err: err.Error()}
}
