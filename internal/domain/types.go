// Package domain holds the canonical truth-source types used everywhere:
// the connector interfaces, the repository service, the CLI/HTTP adapters,
// and Phase 1 mocks.
//
// Field semantics and invariants live in the truth-source-schema skill and
// the auto-grill record under docs/grill/. This file is the wire-shape only.
package domain

import "time"

// EntryType is the closed enum of derived-layer entry types. New values are
// product decisions and must update: this file, repository validators, FTS
// maintenance, mock Classify, UI listings.
type EntryType string

const (
	EntryTypeActivity EntryType = "activity"
	EntryTypeResearch EntryType = "research"
	EntryTypeDiscuss  EntryType = "discuss"
	EntryTypeDecision EntryType = "decision"
	EntryTypeGoal     EntryType = "goal"
)

// Source identifies where an entry originated.
type Source string

const (
	SourceGit     Source = "git"
	SourceGitHub  Source = "github"
	SourceSlack   Source = "slack"
	SourceClickUp Source = "clickup"
	SourceClaude  Source = "claude"
	SourceManual  Source = "manual"
)

// Origin classifies how the entry exists locally; enforces the disjoint-set
// sync model (fetched data is read-only; pushed/local can be edited).
type Origin string

const (
	OriginFetched Origin = "fetched"
	OriginPushed  Origin = "pushed"
	OriginLocal   Origin = "local"
)

// Status lifecycle — applies only to goal entries (validator enforces NULL
// on others; truth-source-schema invariant per-type contract).
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusArchived   Status = "archived"
)

// Relation is the closed enum of edge kinds.
type Relation string

const (
	RelationRelatesTo   Relation = "relates_to"
	RelationDerivedFrom Relation = "derived_from"
	RelationBlocks      Relation = "blocks"
	RelationPartOf      Relation = "part_of"
	RelationIterationOf Relation = "iteration_of"
)

// SummaryState is the segment lifecycle.
type SummaryState string

const (
	SummaryStatePending      SummaryState = "pending"
	SummaryStateMaterialized SummaryState = "materialized"
	SummaryStateStale        SummaryState = "stale"
)

// Entry is the derived-layer atom. `source_ref` is for create-dedupe only;
// stable identity across edits lives in `LogicalID`.
//
// Bitemporal model (grill: docs/grill/2026-06-08-time-travel-schema.md):
//   - OccurredAt = event time / valid_time (when this thing happened in the
//     world). For fetched origin this MUST come from upstream (git author_time,
//     Slack ts, ClickUp date_*). For manual/local it defaults to IngestedAt
//     unless the user backdates.
//   - IngestedAt = system time (when we recorded this version locally). Always
//     wall-clock now at write.
//   - UpdatedAt = legacy system-time mark used for supersede bookkeeping.
//     Effectively equal to IngestedAt under append-only.
//
// Audit (optional everywhere; CLI may omit, sync paths populate from source):
//   - Actor = who initiated the write (user id, sync worker name, …)
//   - Reason = git-commit-message equivalent for supersede
//
// Concurrency / idempotency:
//   - Version = monotonic per LogicalID; supersede must pass the expected
//     value of the old row's Version to detect concurrent writes.
//   - SourceEventHash = fetched fingerprint; same (LogicalID, hash) twice
//     ⇒ no-op (partial unique index).
//   - QualityDegraded = true when OccurredAt fell back to IngestedAt because
//     no source-side candidate was usable.
type Entry struct {
	ID              string
	LogicalID       string
	Type            EntryType
	Title           string
	Body            string
	Source          Source
	SourceRef       string
	SourceEventHash string
	Origin          Origin
	RepoID          string
	Author          string
	Actor           string
	Reason          string
	Status          Status
	Version         int
	IsCurrent       bool
	SupersededBy    string
	Metadata        string // JSON
	QualityDegraded bool
	OccurredAt      time.Time
	IngestedAt      time.Time
	UpdatedAt       time.Time
}

// Edge is a typed relationship between two entries. Append-only + supersede;
// partial-unique `(from, to, relation) WHERE is_current = 1` keeps lookups
// deterministic. OccurredAt mirrors the entry semantics — when the relation
// became true in the world; supersede re-points carry the prior occurred_at
// rather than re-stamping with now() so EdgesAt(historical-T) sees the
// link that actually existed at T.
type Edge struct {
	ID           string
	FromID       string
	ToID         string
	Relation     Relation
	Actor        string
	Reason       string
	IsCurrent    bool
	SupersededBy string
	Metadata     string
	OccurredAt   time.Time
	IngestedAt   time.Time
}

// Segment is the work-session lifecycle carrier. Idempotency key authority
// for the git ingest path (truth-source-schema invariant 2).
//
// OccurredAtMin / OccurredAtMax: MIN/MAX of joined raw_commits.author_time
// across the change set, recomputed at materialize time. Drives activity
// OccurredAt assignment and goal-window queries.
type Segment struct {
	ID            string
	RepoID        string
	Source        Source
	SourceRef     string // encode(repo_id, branch, anchor_patch_id)
	SummaryState  SummaryState
	AnchorPatchID string
	Metadata      string
	OccurredAtMin time.Time
	OccurredAtMax time.Time
	IngestedAt    time.Time
	UpdatedAt     time.Time
}

// RawCommit is the self-contained git commit record. sha is idempotency key;
// is_current/superseded_by track rewrite chains under amend/rebase.
type RawCommit struct {
	SHA          string
	RepoID       string
	ChangeID     string
	ParentSHAs   []string
	Author       string
	AuthorTime   time.Time
	Committer    string
	CommitTime   time.Time
	Message      string
	Diff         string
	BranchHint   string
	IsCurrent    bool
	SupersededBy string
	IngestedAt   time.Time
}

// RawChange is the rewrite-transparent logical change layer. Surrogate
// `ChangeID` (uuid v7); `PatchID` is the content fingerprint and can be empty
// (merge commits / patch-id unavailable).
type RawChange struct {
	ChangeID   string
	RepoID     string
	PatchID    string
	IngestedAt time.Time
}
