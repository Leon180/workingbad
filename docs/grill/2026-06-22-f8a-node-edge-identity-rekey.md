# F8a — node-edge identity unification (the edge re-key)

**Date:** 2026-06-22
**Status:** plan locked (architect-verified against code), implementation pending
**Decision (user):** edges identify endpoints by **node `logical_id`**, resolved
from entries via `entry_node_map`. Completes the D2e re-key that stopped at
entry-`logical_id` (migration 0017).
**Grounds in:** issue #102 (🔴 Slice-F prereqs), the Slice-F kickoff
(`2026-06-14-slice-f-kickoff.md`), F5–F7 (orchestrator + split + aggregate now
create nodes with fresh ids that no edge can reference).

## The bug, precisely

`edges.from_id/to_id` store the **entry's `logical_id`**, not node id:
- `0017_edges_rekey_to_node.sql` remaps `from_id = entries.logical_id` — it never
  consults `nodes`/`entry_node_map`. The name says "to_node"; it keyed to entry
  logical_id because at D1 backfill `node.id == entry.logical_id` (0013).
- `resolveLiveNodeID` (edges.go) is **misnamed**: `GetEntryNodeRef` =
  `SELECT logical_id FROM entries WHERE id=?` → returns entry logical_id.
- `CreateNode` mints a fresh `uuid.NewV7()` (node.id ≠ entry logical_id), so
  split/aggregate nodes have an identity **no edge can reference**. F8b relate
  has nothing to attach to. (For 1:1 backfilled entries the values coincide, so
  AttachToGoal + reads "work" today by accident.)

## Migration

Schema is **not frozen** (no `schema-frozen` tag), so **edit 0017 in place**
(don't append a 0020 to re-correct a same-generation migration). Change the remap
source to the entry's live node id:
`from_id = (SELECT m.node_id FROM entries e JOIN entry_node_map m ON m.entry_id=e.id
JOIN nodes n ON n.id=m.node_id AND n.is_current=1 WHERE e.id = <old edge.from_id>)`.
Keep the `edges_pre_node_rekey` backup, index drop/rebuild, dedup pass. 0017 is
NOT in `sqlc.yaml` (excluded 0012-0017) → editing it doesn't perturb codegen.
**Real data is effectively a no-op** (node.id==logical_id for all backfilled rows;
no edges point at pipeline nodes yet). Edge data is test/seed only.

## Touchpoints

Write: `resolveLiveNodeID` → return the live **node id**
(`entries.id → entry_node_map.node_id → nodes.is_current=1`); new hand-written
query (node tables aren't sqlc). `AttachToGoal` needs no other change.

Read (flip entry-logical_id joins → node id): `GoalActivitiesAt` +
`GetGoalActivitiesByLogicalID` (the 4-way temporal join — riskiest; may force
`GetGoalActivities` off sqlc if sqlc can't parse the node-table join),
`collectAttached` (filter by goal **node id**), web `buildEdges`/`assignLanes`/
`entryKey` (node-id ↦ entry resolution; F8a keeps the graph rendering *entries* —
graph-renders-nodes is a later slice). `EdgesAt` SQL unchanged (opaque pass-through;
only caller filter inputs change).

## Tx-scoped edge writer (for F8b)

Add `Tx.InsertEdge(ctx, domain.Edge) (domain.Edge, error)` backed by an
`insertEdgeTx(ctx, dbtx, domain.Edge)` core in edges.go: validate relation enum +
confidence∈[0,1], mint uuid v7, stamp RFC3339Nano UTC, reuse `GetLiveEdgeByTriple`
idempotency. Refactor `AttachToGoal` to delegate to the same core (one insert
path, DRY). Takes node ids directly (relate operates on node candidates).

## Sequencing (cannot be one PR — write+reads must agree)

- **PR-1 (repository, atomic):** 0017 remap rewrite + `resolveLiveNodeID` →
  node id + `GoalActivitiesAt`/`GetGoalActivities` join flip + `Tx.InsertEdge`.
  TDD: existing edge + queries_temporal tests pass under node-keying; add a
  regression where a pipeline node (node.id ≠ entry logical_id) gets an edge and
  is found by `GoalActivitiesAt`/`EdgesAt`.
- **PR-2 (web resolver):** node-id ↦ entry translation in
  `buildEdges`/`assignLanes`/`collectAttached`. Keeps PR-1's blast radius inside
  the repository package.

**Riskiest:** the `GoalActivitiesAt` 4-way join rewrite + the sqlc-vs-handwritten
call for `GetGoalActivities`. Blast radius: ~6 source + ~8 test files. Run the
big-version-review lens (don't trust green CI alone) before tagging.

> After F8a: **F8b relate** (Layer A source-native / B temporal+vector→LLM /
> C cross-source, per-edge confidence) → **F9 real providers**.
