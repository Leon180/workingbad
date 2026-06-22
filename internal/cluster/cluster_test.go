package cluster_test

import (
	"math"
	"testing"

	"github.com/Leon180/workingbad/internal/cluster"
)

// vec builds a 2D unit vector at the given angle (degrees); cosine between two
// such vectors equals cos(angle difference), so tests read as plain geometry.
func vec(deg float64) []float32 {
	r := deg * math.Pi / 180
	return []float32{float32(math.Cos(r)), float32(math.Sin(r))}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", vec(0), vec(0), 1},
		{"orthogonal", vec(0), vec(90), 0},
		{"opposite", vec(0), vec(180), -1},
		{"sixty-degrees", vec(0), vec(60), 0.5},
		{"zero-vector", []float32{0, 0}, vec(0), 0},
		{"length-mismatch", []float32{1}, []float32{1, 0}, 0},
		{"empty", []float32{}, []float32{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cluster.Cosine(tc.a, tc.b); !approx(got, tc.want) {
				t.Fatalf("Cosine = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEpsilonBall_AllSimilar(t *testing.T) {
	got := cluster.EpsilonBall([][]float32{vec(0), vec(1), vec(2)}, 0.80)
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("got %v, want one cluster of 3", got)
	}
}

func TestEpsilonBall_AllDistinct(t *testing.T) {
	// 0°, 90°, 180° are mutually below θ=0.80 → three singletons.
	got := cluster.EpsilonBall([][]float32{vec(0), vec(90), vec(180)}, 0.80)
	want := [][]int{{0}, {1}, {2}}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEpsilonBall_TwoClusters(t *testing.T) {
	// {0°,5°} tight; {90°,95°} tight; the two groups are orthogonal.
	got := cluster.EpsilonBall([][]float32{vec(0), vec(90), vec(5), vec(95)}, 0.80)
	want := [][]int{{0, 2}, {1, 3}}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEpsilonBall_ChainingIsTransitive(t *testing.T) {
	// 0~30 (0.87) and 30~60 (0.87) link, but 0~60 (0.50) does not. Single-linkage
	// chains all three into one cluster.
	got := cluster.EpsilonBall([][]float32{vec(0), vec(30), vec(60)}, 0.80)
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("got %v, want one chained cluster of 3", got)
	}
}

func TestEpsilonBall_ThresholdIsInclusive(t *testing.T) {
	// cos(60°) = 0.5 exactly; theta 0.5 must include the pair (>=).
	got := cluster.EpsilonBall([][]float32{vec(0), vec(60)}, 0.5)
	if len(got) != 1 {
		t.Fatalf("got %v, want one cluster (>= is inclusive)", got)
	}
}

func TestEpsilonBall_Empty(t *testing.T) {
	got := cluster.EpsilonBall(nil, 0.80)
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v, want non-nil empty", got)
	}
}

func TestEpsilonBall_Singleton(t *testing.T) {
	got := cluster.EpsilonBall([][]float32{vec(0)}, 0.80)
	if !equal(got, [][]int{{0}}) {
		t.Fatalf("got %v, want [[0]]", got)
	}
}

func TestEpsilonBall_ZeroVectorsStaySingletons(t *testing.T) {
	// Cosine of a zero vector is 0 (undefined direction); at the locked θ=0.80
	// that's below threshold, so two zero vectors do not merge.
	got := cluster.EpsilonBall([][]float32{{0, 0}, {0, 0}}, 0.80)
	if !equal(got, [][]int{{0}, {1}}) {
		t.Fatalf("got %v, want two singletons", got)
	}
}

func equal(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
