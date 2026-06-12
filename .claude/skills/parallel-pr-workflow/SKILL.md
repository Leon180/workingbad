---
name: parallel-pr-workflow
description: Worktree-per-PR + cross-session coordination patterns for running multiple Claude Code sessions (or one session + parallel UI/backend work) against the same repository without merge-conflict pain. Captures lessons from the 2026-06-11 retro (workingbad #64). Use when a parallel session is dirty-tree-editing or when you need to ship 2+ PRs simultaneously off main.
origin: workingbad
---

# parallel-pr-workflow

When more than one session, person, or autonomous loop is working the same repository concurrently, **uncommitted state in the main worktree becomes radioactive**. This skill is the playbook that survived workingbad's first multi-session sprint (UI redesign in one session, backend cleanups in another, ~12 PRs in 24h).

## When this skill applies

- Two Claude Code sessions sharing the same repo (e.g. one Claude Design / UI-coupled session + one backend session).
- One session with multiple parallel branches off main where uncommitted files would collide.
- A reviewer is asking you to fix-up a PR while you have other in-flight branch work.

## The core invariant

**The main worktree (the path the other session is using) is read-only to you. Never `git stash` someone else's uncommitted work without consent. Use a separate worktree.**

Symptoms when you violate this:

- `git stash` shows entries you don't recognise → another session was editing
- `git status` lists 10+ files modified you didn't touch → cross-session dirty state followed you to a new branch
- Build green in your branch but PR shows surprise files in the diff → you committed someone else's work

## The pattern

### 1. Create a sibling worktree per concurrent PR

```bash
# Sibling directory keeps paths predictable and gitignored
git worktree add ../workingbad-feature-X feat/feature-X
cd ../workingbad-feature-X
```

Each worktree is its own checkout of the same repo. `git worktree list` shows all active ones. The main worktree (the one you `cd ~/project/workingbad` to) stays untouched — that's where the other session lives.

### 2. Always branch off `origin/main`, never the current checkout

```bash
# Inside your sibling worktree
git fetch origin main
git checkout -B feat/feature-X origin/main
```

The `-B` form forces the branch ref to `origin/main`'s HEAD even if it existed before. This is the single biggest source of "why does my PR contain commit Y" — when you `git checkout -b new-branch` from a worktree that happens to be on the previous PR's branch, the new branch inherits those commits.

### 3. When you need to touch the OTHER session's working tree (rare)

Don't. If you genuinely must (e.g. to deploy a hot-fix), the pattern is:

```bash
# In the main worktree
git stash push -m "other-session-in-flight" -- <files-you-DIDN'T-touch>
# ... your fix ...
git stash pop  # restore their work
```

The `-- <files>` form is critical: stash only their files, leave yours alone.

### 4. Co-existence with a running dev server

If the other session is running `./binary serve` against `/tmp/their-config/db.sqlite`, your seeder / migration runs are SAFE as long as you point at a different DB path. Don't `wipe + reseed` on their DB unless they asked.

If you have to swap out the running server (e.g. to demonstrate richer data), the order is:

1. `kill <PID>` the existing process (or `pkill -f workingbad`).
2. Run your wipe + seed against THEIR config.
3. Restart THEIR binary against the now-fresh DB.

That way they get to keep their UI/binary work and just inherit your data.

## Cross-session coordination via GitHub

Sessions can't talk directly — they share state through GitHub:

- **Open issues label-tagged by owner area** (`ui-ux`, `backend`) so each session picks from a clean queue.
- **PR labels** flag the owning session: `ui-ux` PRs go to the design session, `backend` PRs to yours.
- **A consolidated retro issue** (e.g. workingbad #64) becomes the cross-session memory once a window of work closes. Auto-retro cadence ≥10 merged PRs avoids spam.

## What `git checkout` traps to watch for

| Symptom | Root cause | Fix |
|---|---|---|
| Your new PR has commits you don't recognise | `checkout -b` from a non-main branch | `git reset --hard origin/main` immediately after; re-apply your work |
| `git stash pop` warns "conflict" | Multiple stashes from different sessions interleaved | Use named stashes (`stash push -m "session-X-purpose"`) and `stash pop <name>` |
| New worktree fails: "branch already checked out" | Same branch in two worktrees | `git worktree list` to find which; either remove the other or pick a new branch name |
| Building shows `undefined: X` for code that obviously exists | gopls workspace warning (worktree not in `go.work`) | Ignore IDE diagnostics; trust `go build` |

## What to commit-test before pushing

Before `git push -u origin <branch>`:

1. `git status --short` — confirm only YOUR files listed. Any cross-session file present → unstaged check.
2. `git diff --stat origin/main..HEAD` — the diff that the PR will show. Anything unexpected → reset.
3. `go build ./... && go test ./... && gofmt -l . && golangci-lint run` — local CI parity.

If step 1 or 2 surprises you, abort. You're about to leak someone else's work into your PR.

## When to fall back to a fresh clone

Worktrees share the same `.git/` directory. If `.git/` itself is corrupted (rare — happens with interrupted `git stash`/`git rebase --abort` mid-conflict), no amount of worktree juggling helps. Symptom: `fatal: bad object ...`. Fix: `git clone <repo>` to a brand-new directory and continue work there.

## Tested boundaries

- 2 simultaneous Claude Code sessions × 4 hours × ~12 merged PRs: zero accidental cross-commits.
- 1 session × 3 simultaneous PRs (queued sequentially as worktrees): zero stash collisions.
- Mixing worktree + `git stash` + auto-fixup commits: works as long as `stash push -m` names are unique per session.