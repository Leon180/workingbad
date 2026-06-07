---
name: go-single-binary-ci
description: CI + Makefile + migration discipline for pure-Go single-binary projects (modernc/sqlite + goose + sqlc + golangci-lint v2). Captures the working layout from workingbad (Phase 0-1). Use when scaffolding a new Go service that ships as one binary.
origin: workingbad
---

# go-single-binary-ci

The CI / Makefile / discipline patterns that survived first contact with the
workingbad init PR. Each section lists the trap we hit and the workaround.

## When this skill applies

- Pure-Go project compiled into a single binary (no cgo).
- SQLite store with `modernc.org/sqlite`.
- Migration tooling = `pressly/goose v3` with `embed.FS`.
- (Optional) `sqlc` for type-safe queries.
- Go ≥ 1.25 (often forced by deps like `go-playground/validator/v10`).

If your stack diverges (cgo, PostgreSQL, ORM), most rules still help but the
specific tool versions may not.

## Required CI jobs

A working CI for this stack has **four jobs**, all run on every PR:

1. **build / vet / test** — `go vet ./...` + `go test -race ./...` + `go build`.
2. **golangci-lint** — separate job because lint diagnostics are noisy when
   mixed with test output.
3. **migration discipline** — three gates documented below.
4. **sqlc drift** (only if you use sqlc) — fails CI if committed generated
   code is out of date.

## The golangci-lint Go-version trap (must read)

**golangci-lint v1.x was built with Go ≤ 1.24** and refuses to run on a
project whose `go.mod` targets Go 1.25+:

> `can't load config: the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.25.x)`

`golangci-lint-action@v6` defaults to v1.x even with `version: latest`.

Working combination:

```yaml
- uses: golangci/golangci-lint-action@v7   # @v7, not @v6
  with:
    version: v2.4.0                         # explicit v2.x pin
```

Plus the v2 config format is different from v1 — `default: none` instead of
`disable-all: true`, formatters split from linters, exclusions under
`linters.exclusions`. See [.golangci.yml](.golangci.yml).

## Migration discipline — three CI gates

For the locked rationale see [docs/ROADMAP.md](docs/ROADMAP.md)
"Migration 紀律". The mechanical rules:

- **Gate 1 — immutable post-tag**: after `v0.1.0` (the freeze tag), files
  that existed at the tag must not change.
- **Gate 2 — sequential numbering**: files are `0001_X.sql`, `0002_X.sql`,
  ... with no gaps.
- **Gate 3 — count == max**: number of files equals the highest version.

Implementation: [scripts/check-migration-gates.sh](scripts/check-migration-gates.sh).

Pre-`v0.1.0`, gate 1 SKIPs (we may still squash unreleased migrations).
After tagging, edits to an already-tagged file fail CI. Tooling enforces the
discipline so it can't be accidentally bypassed.

## sqlc

If you adopt sqlc, **install the binary; do not use `go run sqlc@version`** —
on macOS that pulls cgo via `pganalyze/pg_query_go` and conflicts with Xcode's
`strchrnul` declaration.

Working setup:

- Local: `brew install sqlc` (or platform equivalent).
- CI: `sqlc-dev/setup-sqlc@v4` with `sqlc-version` pinned.
- Makefile uses a `SQLC ?= sqlc` variable so contributors can override.

CI guard against drift:

```yaml
- uses: sqlc-dev/setup-sqlc@v4
  with:
    sqlc-version: '1.21.0'
- run: sqlc diff
```

`sqlc diff` exits non-zero if generated code disagrees with the `.sql` files.

If your schema contains FTS5 `CREATE VIRTUAL TABLE`, sqlc's SQLite parser
will choke. Workaround: maintain a separate "schema shim" file declaring
the FTS5 table as an ordinary `CREATE TABLE` and feed it to sqlc instead
of the real migration. The runtime still uses the real virtual table; only
the codegen path sees the shim.

## Makefile template

```make
.PHONY: build test test-race lint fmt tidy clean ci ci-migration-gates ci-sqlc sqlc

BINARY := myapp
SQLC ?= sqlc

build:
	go build -o $(BINARY) ./cmd/$(BINARY)

test-race:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	goimports -local github.com/<owner>/<repo> -w .
	gofmt -s -w .

sqlc:
	$(SQLC) generate

ci-sqlc:
	$(SQLC) diff

ci-migration-gates:
	@./scripts/check-migration-gates.sh

ci: lint test-race build ci-migration-gates ci-sqlc
```

## CI workflow template

```yaml
name: CI
on:
  push: { branches: [ main ] }
  pull_request:
permissions:
  contents: read

jobs:
  build-and-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # migration gates compare against tags
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go vet ./...
      - run: go test -race ./...
      - run: go build -o myapp ./cmd/myapp

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - uses: golangci/golangci-lint-action@v7
        with:
          version: v2.4.0

  migration-gates:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - run: bash scripts/check-migration-gates.sh

  sqlc-diff:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: sqlc-dev/setup-sqlc@v4
        with: { sqlc-version: '1.21.0' }
      - run: sqlc diff
```

## Local pre-push checklist (Makefile target `make ci`)

Same commands CI runs, in the same order. Catches the same failures locally
so you don't burn a CI run on something `gofmt` would have caught:

```
go vet ./... && \
go test -race ./... && \
go build && \
golangci-lint run ./... && \
bash scripts/check-migration-gates.sh && \
sqlc diff
```

## Known traps the workingbad PR series hit

| Symptom | Root cause | Fix |
|---|---|---|
| `golangci-lint exit 3 — Go language version too low` | `@v6` + `latest` = v1.x golangci-lint, built with Go 1.24 < project Go 1.25 | `@v7` + `version: v2.4.0` |
| `goimports` complains "not properly formatted" but file looks fine | `local-prefixes` not set, third-party imports drift into the local group | `goimports -local github.com/<owner>/<repo> -w .` |
| `staticcheck S1025: should use String() instead of fmt.Sprintf("%s", ...)` on a test specifically testing the Stringer path | The whole point of that assertion is `fmt.Stringer` via `%s` | `//nolint:staticcheck` on the offending line with a comment |
| `sqlc generate` on macOS errors with `static declaration of 'strchrnul' follows non-static declaration` | `go run sqlc@version` pulls cgo for `pganalyze/pg_query_go` | Install the binary instead (brew / setup-sqlc action) |
| sqlc parser fails on `CREATE VIRTUAL TABLE ... USING fts5(...)` | sqlc's SQLite grammar doesn't understand FTS5 syntax | Maintain a separate `fts_schema_shim.sql` with ordinary `CREATE TABLE`; reference it in `sqlc.yaml schema:` instead of the real migration |
| Migration tests pass locally with `:memory:` but flake in CI | modernc opens multiple connections, each gets its own empty in-memory DB | Always use `t.TempDir()` + a real file path for migration tests |

## Reusable as a workflow?

Some of these jobs (the YAML) could be promoted to a callable GitHub
Workflow (`uses: <owner>/<repo>/.github/workflows/go-ci.yml@v1`). That
needs the path inputs parameterised. Not yet done in workingbad — this skill
captures the pattern as documentation; callable workflow is a follow-up.
