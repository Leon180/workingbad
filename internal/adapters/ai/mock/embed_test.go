package mock_test

import (
	"context"
	"math"
	"testing"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
)

func TestEmbedder_Deterministic(t *testing.T) {
	e := mock.NewEmbedder(16)
	a, err := e.Embed(context.Background(), []string{"refactor the sync loop"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b, err := e.Embed(context.Background(), []string{"refactor the sync loop"})
	if err != nil {
		t.Fatalf("Embed (2nd): %v", err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("want 1 vector each, got %d and %d", len(a), len(b))
	}
	for i := range a[0] {
		if a[0][i] != b[0][i] {
			t.Fatalf("same text → different vector at %d: %v vs %v", i, a[0][i], b[0][i])
		}
	}
}

func TestEmbedder_DimAndUnitNorm(t *testing.T) {
	const dim = 32
	e := mock.NewEmbedder(dim)
	if e.Dim() != dim {
		t.Fatalf("Dim() = %d, want %d", e.Dim(), dim)
	}
	vecs, err := e.Embed(context.Background(), []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Fatalf("vec %d width = %d, want %d", i, len(v), dim)
		}
		var sum float64
		for _, c := range v {
			sum += float64(c) * float64(c)
		}
		if math.Abs(math.Sqrt(sum)-1) > 1e-5 {
			t.Fatalf("vec %d not unit norm: |v| = %v", i, math.Sqrt(sum))
		}
	}
}

func TestEmbedder_DistinctTextsDiffer(t *testing.T) {
	e := mock.NewEmbedder(16)
	vecs, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	same := true
	for i := range vecs[0] {
		if vecs[0][i] != vecs[1][i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("distinct texts produced identical vectors")
	}
}

func TestEmbedder_EmptyInput(t *testing.T) {
	e := mock.NewEmbedder(8)
	out, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if out == nil {
		t.Fatal("want non-nil empty slice, got nil")
	}
	if len(out) != 0 {
		t.Fatalf("want 0 vectors, got %d", len(out))
	}
}

func TestEmbedder_OrderPreserved(t *testing.T) {
	e := mock.NewEmbedder(8)
	texts := []string{"one", "two", "three"}
	batch, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed (batch): %v", err)
	}
	for i, txt := range texts {
		solo, err := e.Embed(context.Background(), []string{txt})
		if err != nil {
			t.Fatalf("Embed (solo %q): %v", txt, err)
		}
		for j := range batch[i] {
			if batch[i][j] != solo[0][j] {
				t.Fatalf("batch[%d] != solo for %q at %d", i, txt, j)
			}
		}
	}
}

func TestEmbedder_NonPositiveDimFallsBack(t *testing.T) {
	if got := mock.NewEmbedder(0).Dim(); got != mock.DefaultEmbedDim {
		t.Fatalf("dim 0 → %d, want DefaultEmbedDim %d", got, mock.DefaultEmbedDim)
	}
	if got := mock.NewEmbedder(-5).Dim(); got != mock.DefaultEmbedDim {
		t.Fatalf("dim -5 → %d, want DefaultEmbedDim %d", got, mock.DefaultEmbedDim)
	}
}
