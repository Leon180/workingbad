package mock

import (
	"context"
	"hash/fnv"
	"math"

	"github.com/Leon180/workingbad/internal/ports/ai"
)

// Embedder is a deterministic in-memory ai.Embedder for Phase 1 tests and the
// mock pipeline. Each text maps to a stable L2-normalized vector derived from
// an FNV-1a hash of its bytes — same text → same vector, different text →
// (almost always) a different one. It carries no semantic similarity; tests
// that need specific cosine relationships construct vectors directly.
type Embedder struct {
	dim int
}

// Compile-time interface assertion.
var _ ai.Embedder = (*Embedder)(nil)

// DefaultEmbedDim is the mock's vector width when NewEmbedder is given a
// non-positive dim. Small but >1 so cosine similarity is meaningful in tests.
const DefaultEmbedDim = 8

// NewEmbedder returns a deterministic Embedder producing dim-wide unit vectors.
// A non-positive dim falls back to DefaultEmbedDim.
func NewEmbedder(dim int) *Embedder {
	if dim <= 0 {
		dim = DefaultEmbedDim
	}
	return &Embedder{dim: dim}
}

// Dim reports the fixed vector width.
func (e *Embedder) Dim() int { return e.dim }

// Embed maps each text to its deterministic unit vector, preserving order.
// Stateless and does no I/O, so ctx is unused and it is safe for concurrent use.
func (e *Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = e.vec(t)
	}
	return out, nil
}

// vec builds a stable unit vector for text: each component is an FNV-1a hash of
// the text salted with the component index, mapped to [-1, 1), then the whole
// vector is L2-normalized so cosine similarity is well-defined.
func (e *Embedder) vec(text string) []float32 {
	v := make([]float32, e.dim)
	var norm float64
	for j := 0; j < e.dim; j++ {
		h := fnv.New64a()
		// hash.Hash.Write never returns an error (documented contract).
		_, _ = h.Write([]byte(text))
		_, _ = h.Write([]byte{byte(j), byte(j >> 8)})
		// Divide by 2^64 (exactly representable in float64; math.MaxUint64
		// rounds up to it, so dividing by that would let the top hash reach
		// exactly 1.0). Maps Sum64 ∈ [0, 2^64) → [0, 1) → [-1, 1).
		c := float64(h.Sum64())/float64(1<<64)*2 - 1
		v[j] = float32(c)
		norm += c * c
	}
	// norm == 0 needs every component hash to land on exactly 0.0 — unreachable
	// for any real text; the guard just avoids a divide-by-zero on a zero vector.
	if norm > 0 {
		inv := float32(1 / math.Sqrt(norm))
		for j := range v {
			v[j] *= inv
		}
	}
	return v
}
