# Development process & harness

How work is driven on workingbad — the durable version of the working agreement,
so it survives across sessions and contributors. Conventions in `.claude/CLAUDE.md`
(product/architecture) and the global `~/.claude` rules (coding style/security)
still apply; this doc is the *process*.

## v1.0.0 goals (the north star)
Every slice serves one of these; if it doesn't, question it.
1. Sync multiple sources' data into the local store.
2. LLM-assisted full trace of each project's history & progress.
3. Manual correction of history/progress locally (beyond the LLM).
4. Push local corrections back to chosen sinks.
5. Smooth UI/UX.

## The autonomous PR loop
Each change is one focused, reviewable PR (small > big):
1. Branch off `main` (`feat/…`, `fix/…`, `docs/…`).
2. TDD: tests first, 80%+ on new code; mirror existing patterns.
3. **Agent review before every merge** — `go-reviewer` on all Go changes;
   add `silent-failure-hunter` on dangerous slices (destructive migrations,
   the sole write gate, the edge/identity layer).
4. **Auto-fix every finding** (review + CI), or reply why it's deferred.
5. Request Copilot review (when the account has it enabled).
6. **Merge when CI is fully green AND no outstanding review comments** — a PR
   may auto-merge if it's sat ~10 min after open with no comments and all
   required checks pass. `release-please` then auto-cuts the version.

CI gate (the `main` ruleset, required + strict): build/vet/test · golangci-lint ·
govulncheck · CodeQL · sqlc drift · migration discipline. Auto-merge waits for
these; if a PR goes `BEHIND`, update-branch and let it re-run.

## Releases
`release-please` + a fine-grained PAT (`RELEASE_PLEASE_TOKEN`) cut versions from
conventional commits (feat→minor, fix/perf→patch; pre-1.0 breaking stays 0.x).
The release PR auto-merges on green; nothing is hand-tagged. Schema-freeze is a
separate `schema-frozen` git tag, decoupled from the version.

## Big-version-jump full review
At each **major version jump** (notably the run-up to v1.0.0), run a full
multi-agent review before tagging — not just the per-PR reviews:
- `architect` (integrity + the next-arc readiness),
- `go-reviewer` ×N over the codebase (correctness/perf/security),
- `silent-failure-hunter` (swallowed errors).
Beyond bugs, the review explicitly **streamlines**:
- **Comments** — cut over-verbose / stale / now-false comments; a comment must
  earn its place (the *why*, not the *what*).
- **Reusable functions** — collapse duplication (e.g. repeated SQL column lists,
  copy-pasted scan loops) into shared helpers; DRY the accreted incremental code.
Findings either ship as fixes or are tracked in an issue with rationale.

## Design records
Decisions live in `docs/grill/` (dated). Locked pipeline/architecture decisions
are referenced from the slice that implements them. The Slice-F plan is
`docs/grill/2026-06-14-slice-f-kickoff.md`.

## Standing technical discipline
- `RepositoryService` is the **sole write gate**; compose multi-write steps via
  `Service.InTx` (single-connection pool — never call `s.*` inside the `fn`).
- Migrations: goose, forward-only, additive after `schema-frozen`.
- sqlc for tables ≤ migration 0011 + edges; node-layer tables use hand-written
  SQL. Regenerate + commit `sqlcdb/` whenever a sqlc query/schema changes.
- Timestamps are RFC3339Nano UTC everywhere (lexical compare = chronological).
