// Package ai declares the AIProvider interface. Required capability — Phase 1
// uses an in-memory deterministic mock; Phase 3 replaces it with a real
// local (Ollama) or api (Claude) implementation. No rule-based fallback for
// the flagship Summarize value (truth-source-schema "AI 必要能力").
package ai

import (
	"context"

	"github.com/Leon180/workingbad/internal/domain"
)

// AIProvider is the only seam through which Classify / Summarize / Relate
// are called. All implementations must honour the per-method contract below.
type AIProvider interface {
	// Split decides whether an entry maps to one work-unit node or several,
	// returning one NodeDraft per node (Slice F, Step 1). An entry that maps
	// cleanly to a single node returns a one-element slice; an empty slice means
	// the entry yields no node. Real providers should prefer splitting over
	// conflation when in doubt (over-split bias) — this is implementation
	// guidance, not a return-shape contract. The orchestrator persists each
	// draft and maps the entry to it.
	Split(ctx context.Context, e domain.Entry) ([]domain.NodeDraft, error)

	// Classify decides the EntryType from segment-level aggregated text plus
	// a confidence score. For the Phase 1 git ingest path, the local-git
	// source short-circuits this and always uses EntryTypeActivity; the
	// generic Classify slot is reserved for Phase 3/4 polymorphism (commits
	// that should land as research / decision / ...).
	Classify(ctx context.Context, content string) (domain.EntryType, float64, error)

	// Summarize synthesizes one entry (title + body) from a slice of
	// content-agnostic items (git changes for Step 0, clustered entries/nodes
	// for the Step 2 aggregate). Required; no rule fallback. The call is the
	// lazy materialize step (push-preview triggers it; daily list views do not).
	Summarize(ctx context.Context, items []domain.Summarizable) (title, body string, err error)

	// Relate proposes typed edges between an entry and candidates. Real
	// providers default to embedding cosine similarity; LLM lift only for
	// strong relations with a precision floor.
	Relate(ctx context.Context, e domain.Entry, candidates []domain.Entry) ([]domain.Edge, error)
}
