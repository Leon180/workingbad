package ai

import "context"

// Embedder turns text into dense vectors for the Relate step's cosine
// similarity. It is a SEPARATE port from AIProvider because embeddings are an
// independent capability: the api provider (Claude) has no embeddings endpoint,
// so vectors come from a local or dedicated embeddings backend chosen in config
// independently of the chat/summarize provider. Phase 1 ships a deterministic
// mock; real backends and the vector store land in Slice E.
type Embedder interface {
	// Embed returns one vector per input text, in the same order. Every vector
	// has length Dim(). An empty input slice returns an empty (non-nil) slice,
	// not an error. The contract is deterministic: the same text always maps to
	// the same vector for a given embedder.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dim is the fixed vector width this embedder produces. Callers need it
	// before the first Embed — e.g. to declare the fixed-width sqlite-vec
	// column at store-creation time.
	Dim() int
}
