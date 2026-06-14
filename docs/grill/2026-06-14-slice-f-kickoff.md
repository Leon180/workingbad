# Slice F kickoff — the LLM pipeline (the moat)

**Date:** 2026-06-14
**Status:** plan locked, implementation pending (fresh session)
**Grounds in:** `2026-06-13-v1-llm-aggregation-pipeline-research.md` (8 Qs, cited),
`2026-06-13-v1-pipeline-decisions.md` (8 locked decisions + 2 gap resolutions),
the full-codebase review (issue #102, "🔴 Slice-F prerequisites").

## What Slice F delivers

The **real automated node-population path**: entries (raw records) → the LLM
turns them into the node graph. This closes the gap surfaced in D2f ("nodes have
no live write path") and lights up v1.0.0 goals #1 (sync→local), #2 (LLM trace),
#4 (push-back source needs the graph). It is the moat: LLM cross-domain
translation + local + semantic graph.

Three pipeline steps (locked decisions):
1. **split** — per entry, LLM judges 1 vs N nodes (over-split bias).
2. **aggregate** — cluster (k-means/epsilon-ball, θ=0.80) within a
   `source_instance` block, LLM-validate each cluster → N entries : 1 node.
3. **relate** — Layer A (source-native edges) / Layer B (temporal + vector →
   LLM direction) / Layer C (cross-source, sampled). Per-edge confidence;
   binary dashed/solid (≥0.7) + slider UI.

## Prerequisite PRs (the review's 🔴 list — do FIRST, each additive + reviewed)

**F0 — `InTx` transaction seam.** `func (s *Service) InTx(ctx, fn func(tx *TxService) error) error`.
Refactor the node/edge writers into `…InTx(qtx, …)` internals + thin public
wrappers that open their own tx, so the orchestrator can compose
N-nodes + N-maps + edges atomically (today each method opens its own tx → a
split is not atomic). Architect's #1 structural prep. Pure refactor, behavior
unchanged, existing tests guard it.

**F1 — node-edge identity unification.** Today `CreateNode` mints a fresh uuid
v7 while edges key on entry-`logical_id`; the pipeline's new nodes have no
edge-able identity. Decide + implement: **edges reference `node.id`** (the
graph unit), and `AttachToGoal`/relate resolve entry → node via
`entry_node_map`. This is the other half of the D2e re-key (0017 stopped at
entry-`logical_id`). Fold in the 🟠 **EdgesAt detach** fix here (replace the now
-vestigial `superseded_by` predicate — edge re-point is gone — with a
`detached_at` column + predicate).

**F2 — `edge.confidence`.** Migration: `ALTER edges ADD confidence REAL`;
`domain.Edge.Confidence`; thread through insert + EdgesAt scan. Slice-D leftover;
Step 3 + the confidence UI need it. Bundle the 🧹 **entry-column-list collapse**
(onto `labelEntryColumns` + one scanner) here to de-risk schema additions.

**F3 — `Embedder` interface.** Separate pluggable port (Claude has no embeddings
API): `Embed(ctx, []string) ([][]float32, error)`. Mock returns deterministic
vectors. Plus the `sqlite-vec` spike (viant pure-Go → WASM fallback → exclude
CGO) — Slice E proper, but the interface lands here so H/I can build.

**F4 — `AIProvider` redesign.** Current shape is wrong for the pipeline:
- add `Split(ctx, entry) ([]NodeDraft, error)` (Step 1).
- make `Summarize` content-agnostic (currently `[]domain.RawChange`, git-shaped;
  aggregate summarizes from entries/nodes) — e.g. `Summarize(ctx, []Summarizable)`.
- `Relate` must carry per-edge confidence (already returns `[]domain.Edge`; add
  Confidence to the struct via F2).
- keep `Classify` (LLM type judgment per CLAUDE.md).
Update the deterministic mock + the one caller (`BatchMaterialize`).

## Pipeline PRs (after prerequisites)

**F5 — orchestrator skeleton** over `InTx`: split → aggregate → relate, halt+
queue+JSON-repair fallback on provider failure (locked decision). Wire to the
mock so the full pipeline runs deterministically end-to-end (Phase-1 MVP).

**F6 — step 1 split** · **F7 — step 2 aggregate (clustering + LLM-validate)** ·
**F8 — step 3 relate (Layer A/B/C)**. Each: mock-driven, TDD, cost-aware
(lazy; no fallback for Summarize), eval-harness checks.

**F9 — real providers** (Ollama local / Claude api), behind the `setup` config
choice. Quality caveat documented (Ollama vs Claude on Step-3 directionality).

## Sequencing

F0 → F1 → F2 → F3 → F4 are the prerequisites (mostly additive/refactor, each its
own reviewed PR, auto-merged on green). F5 opens the orchestrator; F6–F8 fill
the steps against the mock; F9 swaps in real providers last. The node read
surface (`/nodes`, built in D2f) + bitemporal time-travel already render whatever
the pipeline produces — no UI rework needed until polish.

## Non-negotiables carried in
performance-first · correctness required · PRs small · CI supply-chain guarded ·
agent-review (+ silent-failure-hunter on dangerous slices) before every merge ·
hand-written SQL for node/edge tables (sqlc only ≤0011).
