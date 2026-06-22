// Package cluster groups items by embedding similarity for the Slice-F aggregate
// step (Step 2): over-split nodes whose vectors sit within a cosine threshold are
// merged into one node. Pure and deterministic — no I/O, no domain types, so it
// is trivially testable and reusable. The Embedder (ports/ai) supplies vectors;
// the orchestrator turns clusters into merged nodes.
package cluster

import "math"

// Cosine returns the cosine similarity of two equal-length vectors, in [-1, 1].
// It returns 0 (treated as "unrelated") when the lengths differ, the vectors are
// empty, or either has zero magnitude — all cases where direction is undefined.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// EpsilonBall groups vector indices by single-linkage: indices i and j join the
// same cluster when Cosine(vecs[i], vecs[j]) >= theta. Linkage is transitive, so
// a~b and b~c put a, b, c together even if a≁c directly (the standard
// epsilon-ball / DBSCAN-minPts-1 behaviour; chaining is a known trade-off of the
// locked θ=0.80 decision). Every index lands in exactly one cluster (singletons
// included). Output is deterministic: members within a cluster ascend, and
// clusters are ordered by their smallest member.
//
// Complexity is O(n²·d) (d = vector dimension); n up to a few hundred is
// comfortable. For larger n, index with an ANN structure before calling.
func EpsilonBall(vecs [][]float32, theta float64) [][]int {
	n := len(vecs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]] // path-halving
			x = parent[x]
		}
		return x
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if Cosine(vecs[i], vecs[j]) >= theta {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	members := make(map[int][]int, n)
	for i := 0; i < n; i++ {
		r := find(i)
		members[r] = append(members[r], i) // i ascends → members stay sorted
	}
	// Determinism comes from this output pass, not the union root (which is
	// arbitrary): visiting i in 0..n-1 means each cluster is first seen at its
	// smallest member, so clusters are ordered by smallest member.
	out := make([][]int, 0, len(members))
	seen := make(map[int]bool, len(members))
	for i := 0; i < n; i++ {
		r := find(i)
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, members[r])
	}
	return out
}
