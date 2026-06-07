---
name: golang-performance
description: High-leverage Go performance patterns — allocation / escape analysis, pprof workflow, hot-path conventions, bounded concurrency, DB-specific pitfalls. Use BEFORE writing performance-sensitive Go (hot loops, ingest pipelines, sync engines, parallel workloads) and during code review on the same. Bias toward measured wins over folklore.
origin: workingbad
---

# golang-performance

Six high-leverage areas, then "when NOT to optimize" because most reaches
for this skill are premature.

## 0. Rule zero — measure first

Use [`go test -bench`](https://pkg.go.dev/testing#hdr-Benchmarks) plus
[`pprof`](https://go.dev/doc/diagnostics) to find real hot paths. Optimising
without a profile is folklore engineering.

Cheap defaults that almost always apply: preallocate slice/map capacity when
the size is known, use `strings.Builder` for repeated concatenation, prefer
`bytes.Buffer` over manual `append`-then-string. Reach for the harder
techniques (escape analysis, `sync.Pool`, inlining tweaks) only when a
profile points at them.

## 1. Allocation + escape analysis

The dominant single lever in Go perf. Heap allocation = GC pressure = tail
latency spikes. Stack allocation is "free" (a pointer increment).

Inspect with:

```bash
go build -gcflags="-m=2" ./... 2>&1 | grep "escapes"
```

`escapes to heap` means a variable did NOT stay on the stack. Common causes
and fixes:

- **Returning pointers to locals** → escape. Either return by value (small
  structs) or have the caller pass in a preallocated `*T`.
- **Interface boxing** → escape. `var x any = 42` escapes the `42`. Avoid
  using `interface{}` / `any` in hot paths; prefer concrete types or
  generics.
- **Capturing local in a closure** → escape. Pass parameters into the
  goroutine/closure instead of capturing.
- **Map/slice values too large for stack** → escape (stack frame size
  limit). Split into smaller chunks if practical.

Reuse buffers with [`sync.Pool`](https://pkg.go.dev/sync#Pool) when you
allocate many same-sized buffers per request (e.g. JSON encoders, byte
buffers for I/O). Don't use Pool for cheap structs — the synchronization
cost dominates.

Go 1.25 improved escape analysis + inlining; Go 1.26 (Feb 2026) shipped the
**Green Tea GC** with ~40% reduction in GC overhead on real workloads. Both
mean less head-room for unnecessary heap allocation — runtime keeps catching
up to lazy code, but only by so much.

## 2. pprof workflow

Six profile types, used in this order when chasing a real bottleneck:

| Profile | When | Command |
|---|---|---|
| CPU | "spending too much CPU" | `go test -cpuprofile=cpu.out -bench=.` |
| Heap (allocs) | "too many allocs/op" | `go test -memprofile=mem.out -bench=.` |
| Goroutine | "growing goroutine count" / leak | `curl /debug/pprof/goroutine?debug=2` |
| Block | "lock contention or chan stalls" | `runtime.SetBlockProfileRate(1)` + `/debug/pprof/block` |
| Mutex | "specific mutex contention" | `runtime.SetMutexProfileFraction(1)` + `/debug/pprof/mutex` |
| Trace | "scheduling / GC pauses" | `go test -trace=trace.out -bench=.` then `go tool trace trace.out` |

Inspect with `go tool pprof` interactively. Critical commands inside pprof:
`top`, `list <funcname>`, `web` (SVG flame graph), `tree`. For benchmark
flames, also try [`go tool pprof -http=:8080 cpu.out`](https://go.dev/blog/pprof).

`benchstat` ([install: `go install golang.org/x/perf/cmd/benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat))
compares before/after benchmark runs with statistical significance — don't
trust eyeballed delta on a single run.

## 3. Hot-path conventions

In a function that runs at least 10k×/sec:

- **Preallocate** slices: `make([]T, 0, n)` not `make([]T, 0)`.
- **Preallocate** maps when size is roughly known: `make(map[K]V, n)`.
- **Use `strings.Builder`** for concatenation: `var b strings.Builder; b.Grow(estimate); b.WriteString(...)`.
- **Avoid `defer` in tight loops** — `defer` has [~20-40 ns overhead per call](https://go.dev/blog/defer-panic-and-recover) (Go 1.14+ open-coded defers are faster but still non-zero).
- **Avoid interface boxing** — typed slices `[]int` instead of `[]any`.
- **Avoid `fmt.Sprintf` in hot paths** when concatenation suffices — `strconv.Itoa(n) + ":" + ...` is much faster.
- **Avoid recursive locks** — they prevent inlining and add overhead.
- **`time.Now()` is cheap** but called in the millions it adds up — cache for batch operations.
- **Loop-invariant map lookups** — hoist `v := m[k]` out of the loop.

## 4. Bounded concurrency

**Never spawn unbounded goroutines on unbounded input.** A request with N
items spawning N goroutines is a goroutine-leak DoS waiting to happen.

Three patterns, in order of preference:

### errgroup with SetLimit (Go 1.20+, the modern default)

```go
import "golang.org/x/sync/errgroup"

g, ctx := errgroup.WithContext(ctx)
g.SetLimit(runtime.GOMAXPROCS(0))  // or a measured value
for _, item := range items {
    item := item
    g.Go(func() error { return process(ctx, item) })
}
if err := g.Wait(); err != nil { return err }
```

Replaces the manual semaphore + WaitGroup + first-error pattern. Cancellation
propagates via `ctx` on first error.

### Worker pool with a closed jobs channel

```go
jobs := make(chan Job, bufferSize)
results := make(chan Result, bufferSize)
var wg sync.WaitGroup
for i := 0; i < workers; i++ {
    wg.Add(1)
    go func() { defer wg.Done(); for j := range jobs { results <- handle(j) } }()
}
// producer: send jobs, then close(jobs)
// consumer: range results
```

Use when you need fine-grained control over worker count vs queue depth.

### Bounded channel as semaphore (use sparingly)

```go
sem := make(chan struct{}, max)
for _, item := range items {
    sem <- struct{}{}
    go func(item Item) {
        defer func() { <-sem }()
        process(item)
    }(item)
}
```

Cleaner than `sync.WaitGroup` for "at most N concurrent" without needing
error collection. Note Go 1.25 ships **container-aware GOMAXPROCS** —
`runtime.GOMAXPROCS(0)` reflects cgroup CPU quota in containers, so
sizing pools off that value Just Works in k8s.

## 5. Database (SQLite / Postgres) hot paths

- **Connection pool**: `sql.DB.SetMaxOpenConns`, `SetMaxIdleConns`,
  `SetConnMaxLifetime`. SQLite write-heavy workloads benefit from
  `SetMaxOpenConns(1)` (single-writer model); reads scale via a separate
  read pool. This is the model the workingbad repository uses.
- **N+1 query**: caught by inspection. The fix is almost always a single
  `JOIN` or a single `WHERE id IN (?, ?, ?, ...)`.
- **Batch inserts**: a single `INSERT ... VALUES (?, ?), (?, ?), ...` is
  10-100× faster than N single-row inserts. SQLite caps batch size at
  ~999 placeholders by default — chunk accordingly.
- **Prepared statements**: `sql.DB.Prepare` then call `stmt.Exec` for repeat
  use. Saves the parse cost per call. sqlc generates `const queryX = ...`
  strings — `db.ExecContext(ctx, queryX, args...)` reuses the cached parse
  via the driver, so the same win without manual `Prepare`.
- **Avoid `SELECT *`**: column list is cheap to maintain and saves both
  network bytes and reflection cost on scan.
- **PRAGMA journal_mode=WAL** for SQLite under any concurrent read load —
  this is in the workingbad repository.Open already.

## 6. Generics + inlining

- Generics introduce a tiny dispatch overhead in some cases but are usually
  faster than the `interface{}` boxing they replace.
- `go build -gcflags="-m=2"` shows inlining decisions (`can inline X` /
  `cannot inline X because Y`). Move large rarely-hit branches into
  separate functions so the hot caller stays small enough to inline.
- Inlining is capped at ~80 budget points — large functions with many
  branches break it. Profiles show "lots of time in caller, not inlined";
  the fix is to split.

## When NOT to optimize

- The benchmark doesn't exist yet → write it before tuning anything.
- The bottleneck is I/O wait, not CPU → tune the I/O (connection reuse,
  batching, caching), not the in-process Go.
- The code runs once per request and the request is < 100 r/s →
  allocation-level tuning is below the noise floor.
- Readability would drop materially → if the optimised version is harder
  to maintain than the bench delta justifies, don't.
- You haven't profiled production-shaped data → micro-benchmarks lie when
  inputs are unrealistic.

## References

- [Dave Cheney — High Performance Go Workshop](https://dave.cheney.net/high-performance-go-workshop/dotgo-paris.html) — the canonical reference, still load-bearing in 2026
- [Damian Gryski — go-perfbook](https://github.com/dgryski/go-perfbook) — pattern catalogue
- [Go Diagnostics](https://go.dev/doc/diagnostics) — official profiling docs
- [`go tool pprof` blog post](https://go.dev/blog/pprof)
- [Uber Go Style Guide — Performance](https://github.com/uber-go/guide/blob/master/style.md#performance) — production-grade conventions
- [Go 1.25 release notes](https://go.dev/doc/go1.25) — container-aware GOMAXPROCS, escape analysis
- [Go 1.26 release notes](https://go.dev/doc/go1.26) — Green Tea GC
- [Reintech — Go Performance Optimization Best Practices 2026](https://reintech.io/blog/go-performance-optimization-guide-2026)
- [errgroup with SetLimit](https://pkg.go.dev/golang.org/x/sync/errgroup#Group.SetLimit)

Sources:
- [Dave Cheney — High Performance Go Workshop](https://dave.cheney.net/high-performance-go-workshop/dotgo-paris.html)
- [Damian Gryski — go-perfbook](https://github.com/dgryski/go-perfbook)
- [Go 1.25 container-aware GOMAXPROCS](https://go.dev/doc/go1.25)
- [Go 1.26 Green Tea GC](https://go.dev/doc/go1.26)
- [Go Diagnostics](https://go.dev/doc/diagnostics)
- [errgroup.SetLimit](https://pkg.go.dev/golang.org/x/sync/errgroup#Group.SetLimit)
- [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- [Reintech — Go Performance Optimization Production Best Practices 2026](https://reintech.io/blog/go-performance-optimization-guide-2026)
