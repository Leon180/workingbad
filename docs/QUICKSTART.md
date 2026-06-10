# workingbad Quickstart

## What it is

workingbad is a **local + LLM semantic truth source** for your engineering work:
a single Go binary that maintains a per-goal record of notes, decisions, and
activities on your laptop. Phase 1 is the dogfood surface — a CLI plus a
loopback Web UI you drive yourself; later phases add git ingest, sinks
(Slack / ClickUp) and a real AI provider.

Read [.claude/CLAUDE.md](../.claude/CLAUDE.md) for the full positioning.

## First-run setup

Requirements: Go ≥ 1.25. No cgo, no external services needed for Phase 1.

```bash
# 1. clone and enter the repo, then:
make init-config        # writes ~/.workingbad/config.yaml (idempotent)
make build              # produces ./workingbad
```

`make init-config` creates `~/.workingbad/config.yaml` pointing at
`~/.workingbad/db.sqlite`. Migrations auto-run the first time the binary opens
the DB, so there is nothing else to bootstrap.

Sanity check:

```bash
./workingbad --config ~/.workingbad/config.yaml version
./workingbad --config ~/.workingbad/config.yaml --help
```

Tip: export `WORKINGBAD=./workingbad --config ~/.workingbad/config.yaml` (or
alias it) so the rest of this doc reads cleanly.

## CLI tour (5 minutes)

Below uses `wb` as shorthand for `./workingbad --config ~/.workingbad/config.yaml`.

### Create a goal, note, decision

```bash
wb goal     "ship Slice C" "git ingest behind a feature flag"
wb note     "sqlite WAL"   "WAL = concurrent readers + 1 writer"
wb decision "use modernc/sqlite" "pure Go, no cgo, FTS5 built-in"
```

Each command prints `created <type> <entry-id>`. Copy a goal ID — you will
need it in a moment.

### List entries with a type filter

```bash
wb list                       # everything live, newest first
wb list --type goal
wb list --type decision --limit 50
wb list --repo my-repo        # isolate by repo_id
```

### History of an entry (bitemporal "git log")

`history` walks the supersede chain for a `logical_id`. Find it via the
Web UI's entry detail page, or by tracking IDs you have already changed
(e.g. via `status`). Then:

```bash
wb history <logical-id>
```

It prints every version side by side with `occurred_at` (event time) and
`ingested_at` (when we recorded it). Current version is marked.

### Time-travel: list state at a past timestamp

```bash
wb list --at 2026-06-08T14:00:00Z
wb list --at 2026-06-08                    # date-only = midnight UTC
wb list --at 2026-06-08T14:00:00Z --type goal
```

`--at` rolls the supersede chain back to what was live at that wall-clock
moment.

### Attach a note (or decision) to a goal

```bash
wb attach <note-id> <goal-id>              # links note under goal via part_of
wb list --type goal                         # confirm goal still present
```

Detach is append-only (it supersedes the edge, original row is preserved):

```bash
wb detach <edge-id>
```

### Change a goal's status

```bash
wb status <goal-id> in_progress            # valid: open|in_progress|done|archived
wb status <goal-id> done
```

Each transition creates a new supersede version; the old one stays in
history.

### Segments and summarize (Phase 1 mock)

```bash
wb pending                                  # backlog of un-summarised git segments
wb summarize                                # materialise them into activity entries
```

Phase 1 ships the deterministic mock AIProvider; the real local
(Ollama) / api (Claude) providers arrive in Phase 3.

## Web UI tour

Start it:

```bash
make serve                                  # convenience target
# or explicitly:
./workingbad --config ~/.workingbad/config.yaml serve --port 7878
```

Then open <http://127.0.0.1:7878> in your browser. You will see:

- **Index list** of every live entry (5 types). Use the type filter
  dropdown — same semantics as `wb list --type ...`.
- **New entry forms** for `research`, `decision`, `goal` (same fields as
  the CLI subcommands).
- **Goal detail** page: lists attached `part_of` entries flat (no graph yet).
- **Entry detail** page: shows the full supersede chain table — the CLI
  `history` view, in the browser.
- **Time-travel** via `?at=<RFC3339>` query param, e.g.
  <http://127.0.0.1:7878/?at=2026-06-08T14:00:00Z>. Drop it onto any list URL.
- **Pending segments** header banner with a "materialize" CTA — equivalent to
  `wb summarize`.

Security note: the listener is bound to 127.0.0.1 only with a Host
allowlist and `http.CrossOriginProtection`. Do not put it behind a public
reverse proxy yet — multi-user security (CSRF token / local token auth)
lands in Phase 2.

Stop the server with Ctrl-C. `make stop-serve` will kill any leftover
process listening on the default port.

## What's NOT here yet

Phase 1 is intentionally narrow. The following are on the roadmap but not
shipped on `main` today:

- **Real git ingest** (Phase 2): segmentation runs against mock fixtures;
  there is no background worker reading your repos. `pending` / `summarize`
  exercise the pipeline on test data.
- **Sinks** (Phase 2): no Slack, no ClickUp push yet. Goal status changes
  stay local.
- **Real AI provider** (Phase 3): `summarize` calls a deterministic mock,
  not Ollama or Claude. AI is a required capability — there is no rule
  fallback — but Phase 1 lets you exercise the whole pipeline without one.
- **Entry edit** form (Web UI) and **goal page supersede chain** are
  deferred to a later PR (same schema, additive).
- **GitHub / Claude / ClickUp sources** (Phase 4).

See [docs/ROADMAP.md](ROADMAP.md) for the full phase breakdown.
