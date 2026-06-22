package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Leon180/workingbad/internal/cluster"
	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/ports/ai"
)

// aggregateTheta is the locked Slice-F cosine threshold: entries whose embeddings
// are at least this similar collapse into one node (Step 2).
const aggregateTheta = 0.80

var (
	errNoEmbeddableText  = errors.New("repository: aggregate: entry has no embeddable text")
	errMixedClusterTypes = errors.New("repository: aggregate: cluster mixes entry types")
)

// AggregateResult summarizes one aggregate pass.
type AggregateResult struct {
	ClustersFormed    int // clusters that produced a node (incl. singletons)
	NodesCreated      int
	EntriesAggregated int           // entries folded into a created node
	Skipped           int           // already-mapped entries (lazy)
	Queued            []QueuedEntry // per-entry, step "aggregate"
}

// AggregateEntries runs Step 2 of the pipeline: embed each pending entry, cluster
// by cosine similarity (epsilon-ball, θ=0.80), and collapse each cluster into one
// node — N entries : 1 node, the inverse of split. Each cluster's node + its
// entry→node maps commit atomically via InTx. Already-mapped entries are skipped
// (lazy, like split). Failure policy matches Orchestrate: a Summarize failure or
// an invalid merged node queues that cluster's entries and the pass continues; an
// embed failure or a real storage error aborts (embeddings are a batch input —
// without them there is nothing to cluster).
//
// This is a distinct pass from Orchestrate (split). Composing the two into a
// single policy — deciding which entries split vs aggregate — is F8 work; here
// the caller chooses which entries to aggregate. The mock Embedder makes this
// deterministic; real backends land in F9, where per-cluster LLM validation
// (accept/reject a proposed merge) also belongs — the mock cannot judge merges.
func (s *Service) AggregateEntries(ctx context.Context, entries []domain.Entry, embedder ai.Embedder, provider ai.AIProvider) (AggregateResult, error) {
	var res AggregateResult

	// Lazy: drop entries already mapped to nodes before paying for embeddings.
	pending := make([]domain.Entry, 0, len(entries))
	for _, e := range entries {
		has, err := s.entryHasNodes(ctx, e.ID)
		if err != nil {
			return res, fmt.Errorf("repository: aggregate: check existing nodes for %s: %w", e.ID, err)
		}
		if has {
			res.Skipped++
			continue
		}
		pending = append(pending, e)
	}
	if len(pending) == 0 {
		return res, nil
	}

	// Build embeddable text per pending entry. An entry with no text (e.g. a
	// whitespace-only title) can't be embedded — queue it with a clear cause
	// rather than letting it become a zero-vector singleton that later fails
	// node validation with a misleading "title required".
	texts := make([]string, 0, len(pending))
	embed := make([]domain.Entry, 0, len(pending))
	for _, e := range pending {
		txt := aggregateText(e)
		if txt == "" {
			res.Queued = append(res.Queued, *queued(e.ID, "aggregate", errNoEmbeddableText))
			continue
		}
		texts = append(texts, txt)
		embed = append(embed, e)
	}
	if len(embed) == 0 {
		return res, nil
	}

	vecs, err := embedder.Embed(ctx, texts)
	if err != nil {
		return res, fmt.Errorf("repository: aggregate: embed: %w", err)
	}
	if len(vecs) != len(embed) {
		return res, fmt.Errorf("repository: aggregate: embedder returned %d vectors for %d entries", len(vecs), len(embed))
	}

	for _, idxs := range cluster.EpsilonBall(vecs, aggregateTheta) {
		members := make([]domain.Entry, len(idxs))
		items := make([]domain.Summarizable, len(idxs))
		ids := make([]string, len(idxs))
		for k, idx := range idxs {
			members[k] = embed[idx]
			items[k] = domain.Summarizable{ID: embed[idx].ID, Text: texts[idx]}
			ids[k] = embed[idx].ID
		}

		// EpsilonBall is type-blind; a cluster mixing entry types would silently
		// inherit members[0]'s type. Refuse it instead of mis-typing the node.
		if !sameType(members) {
			res.queueCluster(ids, errMixedClusterTypes)
			continue
		}

		title, body, serr := provider.Summarize(ctx, items)
		if serr != nil {
			res.queueCluster(ids, serr)
			continue
		}
		if perr := s.persistCluster(ctx, ids, mergedNode(members, title, body)); perr != nil {
			if errors.Is(perr, ErrInvalidInput) {
				res.queueCluster(ids, perr)
				continue
			}
			return res, fmt.Errorf("repository: aggregate: persist cluster: %w", perr)
		}
		res.ClustersFormed++
		res.NodesCreated++
		res.EntriesAggregated += len(ids)
	}
	return res, nil
}

// queueCluster records every entry of a failed cluster under the aggregate step.
func (r *AggregateResult) queueCluster(entryIDs []string, err error) {
	for _, id := range entryIDs {
		r.Queued = append(r.Queued, *queued(id, "aggregate", err))
	}
}

// persistCluster creates the cluster's merged node and maps every member entry to
// it in one transaction (N entries : 1 node, all-or-nothing).
func (s *Service) persistCluster(ctx context.Context, entryIDs []string, n domain.Node) error {
	return s.InTx(ctx, func(ctx context.Context, t *Tx) error {
		created, err := t.CreateNode(ctx, n)
		if err != nil {
			return err
		}
		for _, id := range entryIDs {
			if err := t.MapEntryToNode(ctx, id, created.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// mergedNode builds the node for a cluster: the synthesized title/body, the
// cluster's common type (first member — clusters are within a source block, so
// types are homogeneous in practice), and the earliest member OccurredAt so the
// merged node anchors to when the work began.
func mergedNode(members []domain.Entry, title, body string) domain.Node {
	// Caller guarantees members share a type (sameType), so members[0] carries
	// the cluster's Type and Status (a goal cluster needs Status to validate).
	n := domain.Node{Title: title, Body: body, Type: members[0].Type, Status: members[0].Status}
	for _, e := range members {
		if e.OccurredAt.IsZero() {
			continue
		}
		if n.OccurredAt.IsZero() || e.OccurredAt.Before(n.OccurredAt) {
			n.OccurredAt = e.OccurredAt
		}
	}
	return n
}

// sameType reports whether every member shares the first member's entry type.
func sameType(members []domain.Entry) bool {
	for _, e := range members[1:] {
		if e.Type != members[0].Type {
			return false
		}
	}
	return true
}

// aggregateText is the content embedded + summarized for an entry: title and
// body joined, trimmed. Empty when the entry has neither.
func aggregateText(e domain.Entry) string {
	return strings.TrimSpace(e.Title + "\n" + e.Body)
}
