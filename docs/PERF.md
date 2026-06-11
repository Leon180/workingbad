# Performance evaluation

Two questions this document answers:

1. **How do I know if my change made the code slower?**
2. **What are the current numbers I should be measuring against?**

We use `testing.B` benchmarks + `benchstat` (statistical comparison) per the 2026 perf-eval research (`docs/grill/`). PGO and continuous profiling are explicitly NOT in scope — see issues #28-#32 for rationale.

## Quick: did my change regress?

```bash
# 1. Capture baseline (on main or whatever you started from)
git stash                              # park your changes
go test -bench=. -benchmem -count=10 ./... > /tmp/before.txt
git stash pop

# 2. Capture with your changes
go test -bench=. -benchmem -count=10 ./... > /tmp/after.txt

# 3. Compare
benchstat /tmp/before.txt /tmp/after.txt
```

Reading the output:

- `~`: no significant change (p > 0.05)
- `-N%`: faster by N% (good)
- `+N%`: slower by N% (regression — investigate)

`benchstat` runs a t-test against the 10 samples each side, so a single hot run won't trick it. If you only have 1 sample per side it can't compute significance.

Install: `go install golang.org/x/perf/cmd/benchstat@latest`.

## Current benchmark coverage

| Package | Bench | What it measures |
|---|---|---|
| `internal/repository` | `BenchmarkInsertEntry` | single InsertEntry write tx (validator + sqlc + FTS5 mirror) |
| `internal/repository` | `BenchmarkSupersede` | supersede chain build-up (re-point edges + flip FTS5) |
| `internal/repository` | `BenchmarkListEntries_{100,1000}` | live list query (COALESCE-heavy hand-written SQL) |
| `internal/repository` | `BenchmarkListEntriesAt_{100,1000}` | bitemporal time-travel query (EXISTS subquery) |
| `internal/web/layout` | `BenchmarkBuild_{50,500,2000}nodes_*lanes` | graph layout algorithm (Sugiyama-style rank + ordering) |
| `internal/web` | `BenchmarkSafeRedirectPath` | open-redirect guard (every POST that redirects) |
| `internal/web` | `BenchmarkRenderIndex_Empty` | template render hot path (every GET /) |

## Reference numbers (Apple M5, Go 1.25.11, modernc.org/sqlite)

These are a **rough lower bound** — what we should not regress past. Re-baseline whenever the schema or a hot algorithm changes.

```
BenchmarkInsertEntry-10                        ~100µs  / ~12KB   / ~250 allocs
BenchmarkSupersede-10                          ~250µs  / ~25KB   / ~500 allocs
BenchmarkListEntries_100-10                    ~50µs   / ~30KB   / ~500 allocs
BenchmarkListEntries_1000-10                   ~500µs  / ~300KB  / ~5k allocs
BenchmarkListEntriesAt_100-10                  ~340µs  / ~240KB  / ~4k allocs
BenchmarkListEntriesAt_1000-10                 ~3.3ms  / ~2.5MB  / ~40k allocs
BenchmarkBuild_50nodes_2lanes-10               ~14µs   / ~95KB   / ~90 allocs
BenchmarkBuild_500nodes_5lanes-10              ~150µs  / ~990KB  / ~590 allocs
BenchmarkBuild_2000nodes_10lanes-10            ~720µs  / ~3.9MB  / ~2200 allocs
BenchmarkSafeRedirectPath-10                   <1µs    / minimal
BenchmarkRenderIndex_Empty-10                  ~60µs   / ~21KB   / ~250 allocs
```

These are 10-sample averages. Day-to-day variance is typically <5%.

## CI gate

`.github/workflows/benchmarks.yml` runs every benchmark on every PR + on every push to main:

- **Main pushes**: artifact `bench-main.txt` is uploaded (overwrites on each run).
- **PRs**: download `bench-main.txt` from the latest main workflow run, run benchstat against the PR's results, fail if any benchmark shows a > 5% slowdown with p < 0.05.

Pre-merge: a PR that introduces a real regression visibly fails the `bench: regression check` status check.

Override: if a slowdown is intentional (e.g. necessary for correctness), explain in the PR description and request reviewer override. There is no `[bench: skip]` magic — the principle is "every regression is at least acknowledged".

## Query Plan Reference

For every hot SELECT in the bitemporal layer we lock the expected access path here. The actual test (`TestSlowQueries_AssertNoFullTableScan` in `internal/repository/queries_temporal_plan_test.go`) only asserts that **some** `USING INDEX` / `USING COVERING INDEX` / `USING PRIMARY KEY` token appears AND no bare `SCAN entries` / `SCAN edges` row sneaks in — that's the regression signal we actually care about, and it stays durable across SQLite version bumps that would tank a golden-match test.

The names below are documentation only — if the planner picks a different index that's still index-driven on the hot table, the test passes and you only need to update this doc to track the change.

Last verified: SQLite via `modernc.org/sqlite` v1.34.0 (Go 1.25.11). Re-verify only when:

- a hot-table migration adds/drops/changes an index, OR
- you bump `modernc.org/sqlite` to a new minor version, OR
- a hot query's WHERE / FROM shape changes

### Hot queries

| Query | Where it lives | Expected access path | Forbidden access path |
|---|---|---|---|
| **ListEntriesAt** — bitemporal goal lookup | `internal/repository/queries_temporal.go` | `SEARCH e USING INDEX idx_entries_type_current` (type=?) | `SCAN entries` |
| **EntryHistory** — chain walk by logical_id | `internal/repository/queries_temporal.go` | `SEARCH entries USING INDEX idx_entries_logical_id` (logical_id=?) | `SCAN entries` |
| **EdgesAt — from-side** live edge fetch | `internal/repository/queries_temporal.go` | `SEARCH edges USING INDEX idx_edges_from_occurred_live` (from_id=?) | `SCAN edges` |
| **EdgesAt — to-side** live edge fetch | `internal/repository/queries_temporal.go` | `SEARCH edges USING INDEX idx_edges_to_occurred_live` (to_id=?) | `SCAN edges` |
| **Idempotency lookup** — fetched-event dedupe | `internal/repository/queries.go::GetEntryByLogicalIDAndHash` | `SEARCH entries USING INDEX idx_entries_source_event_hash` (logical_id=? AND source_event_hash=?) | `SCAN entries` |

### How to re-verify by hand

```bash
go test -run 'TestSlowQueries_AssertNoFullTableScan/<sub-name>' -v ./internal/repository/
```

That dumps the EXPLAIN QUERY PLAN output for the failing case. If the index name has just been renamed (e.g. by a migration), update both the table above and the table in `internal/repository/queries_temporal_plan_test.go` and re-run. The test should pass on the new index without code changes if it's still index-driven.

### What this replaces

Earlier we had a golden test that asserted `mustContain []string{"idx_entries_type_current", ...}` per case. SQLite explicitly does NOT guarantee EXPLAIN format stability across versions, so the moment we bumped `modernc.org/sqlite` we would have been chasing diff churn that wasn't a real perf regression. The loose-match + this doc give us the same coverage with zero version-tax.

## What we deliberately don't measure (yet)

- **End-to-end latency** under realistic load: separate issue (#30) for Vegeta-driven load test
- **Memory leaks** over long uptime: out of scope for dogfood phase
- **CPU profile during real work**: covered by `--debug` pprof flag (#29) on-demand

## When to re-baseline the reference numbers

- Schema migration that touches a hot table (e.g. adding an index on `entries`)
- Replacing a hot algorithm (e.g. swapping layout strategy)
- Toolchain bump (Go minor version, modernc.org/sqlite version)

Just re-run the bench-table above and update this file. Don't try to chase noise.
