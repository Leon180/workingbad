# Manual Node Operations API + Node Read Surface (Slice D2f, reframed)

**Date:** 2026-06-14
**Status:** design locked, implementing
**Supersedes the original D2f framing** ("flip read path to nodes") after discovering nodes have no live write path.

## Finding that reframed D2f

`nodes` are populated **only** by the 0013 backfill (one-shot, at migration time, 1:1 from
the entries that existed then). No live write path — `InsertEntry`, the materialize
pipeline, and `Supersede` never write `nodes` / `entry_node_map`. So a pure "flip reads to
nodes" would show only pre-migration data; anything ingested afterward would be invisible.

## Node-population model (locked)

Three paths; **no auto-1:1-on-entry-write** (that would be throwaway once the LLM pipeline
lands, per karpathy "don't over-engineer"):

1. **Backfill** — `0013` seeds the node layer 1:1 from existing entries. (done)
2. **Manual operations** — the engineer curates nodes by hand (v1.0.0 goal #3,
   "LLM 以外，在本地能夠人工修正歷史和進度"). Backed by existing `CreateNode` / `SupersedeNode`.
3. **LLM split/aggregate** — Slices F→I. The real automated path (1 entry→N nodes, N→1).

## Consequence: the node UI is ADDITIVE, not a replacement

Entries-home stays (raw ingested records). A new node surface (`/nodes`) renders the curated
graph (backfilled + manual + future LLM). Replacing the entry home would hide
post-backfill entries until a node exists for them. When the LLM pipeline auto-populates
nodes from all entries, the node surface becomes primary.

## API design

### Repository (write gate = RepositoryService; all node ops already route through it)
- exists: `CreateNode`, `SupersedeNode(old, expectedVersion, replacement)`, `GetNode`,
  `GetLiveNodeByLogicalID`, `ListNodesAt`, `NodeHistory`, `SearchNodes`, `CountNodes`,
  `NodesForEntry`, `EntriesForNode`.
- ADD `ListNodes(ctx, NodeListFilter) []Node` — live (is_current=1) list, the non-temporal
  sibling of `ListNodesAt` (mirrors `ListEntries`).

### HTTP (localhost, net/http + embed templates; no heavy FE — project rule)
Read:
- `GET /nodes` — live node list (type filter, `?at=` time-travel via `ListNodesAt`, `?q=`
  search via `SearchNodes`).
- `GET /nodes/{id}` — node detail + `NodeHistory` chain.

Manual write (goal #3) — PR-2:
- `GET /nodes/new` + `POST /nodes` — create a node (type/title/body/status) → `CreateNode`.
- `GET /nodes/{id}/edit` + `POST /nodes/{id}` — edit = supersede with `expectedVersion`
  (optimistic lock; conflict → re-render with the live version) → `SupersedeNode`.

CLI (optional, later): `node list` / `node show` mirroring `list` / `history`.

### Field model
Nodes carry the graph fields (type/title/body/status/version/occurred_at/ingested_at). They
intentionally OMIT entry source/sync metadata (source, origin, author, repo_id,
quality_degraded) — those are raw-record concerns shown on the entry detail, not the node.

## PR breakdown (small, reviewable)
- **D2f-1 (this PR):** `ListNodes` + read-only node surface (`GET /nodes`, `GET /nodes/{id}`)
  with type filter + `?at=` + `?q=` search. Additive; entry views untouched.
- **D2f-2:** manual write endpoints (create + edit/supersede) with optimistic-lock conflict
  handling.
- **D2f-3 (optional):** CLI node list/show.

Each: TDD → go-reviewer (+ silent-failure-hunter on the write PR) → fix → auto-merge.
