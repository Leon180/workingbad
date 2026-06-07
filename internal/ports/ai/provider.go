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
	// Classify decides the EntryType from segment-level aggregated text plus
	// a confidence score. For the Phase 1 git ingest path, the local-git
	// source short-circuits this and always uses EntryTypeActivity; the
	// generic Classify slot is reserved for Phase 3/4 polymorphism (commits
	// that should land as research / decision / ...).
	Classify(ctx context.Context, content string) (domain.EntryType, float64, error)

	// Summarize synthesizes one activity entry (title + body) from a slice
	// of changes. Required; no rule fallback. The call is the lazy
	// materialize step (push-preview triggers it; daily list views do not).
	Summarize(ctx context.Context, changes []domain.RawChange) (title, body string, err error)

	// Relate proposes typed edges between an entry and candidates. Real
	// providers default to embedding cosine similarity; LLM lift only for
	// strong relations with a precision floor.
	Relate(ctx context.Context, e domain.Entry, candidates []domain.Entry) ([]domain.Edge, error)
}
