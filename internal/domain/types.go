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

// Node is the graph-level work unit (v1.0.0 Slice D, issue #68). Entries are
// source-immutable raw records; a Node is what the graph actually renders, and
// an Entry maps to a Node via entry_node_map (many-to-many: split = 1 entry→N
// nodes, aggregate = N entries→1 node). The LLM manages the mapping + the
// node's decided/synthesised Type/Title/Body; it never edits entry content.
//
// Slice D2 (model A, issue #69) gives the node its own supersede chain:
// entries become immutable raw facts; the NODE is the editable/versioned
// graph unit. LogicalID is the stable identity across the node's versions
// (a node supersede mints a new ID but keeps LogicalID); Version is the
// monotonic per-LogicalID counter for optimistic locking; IsCurrent marks
// the live version; OccurredAt/IngestedAt carry the bitemporal pair onto
// the node layer. D2 ships incrementally — this struct holds the columns;
// later D2 sub-PRs move FTS5, the read path, and edges onto nodes.
type Node struct {
	ID           string // uuid v7; a node supersede mints a fresh one
	LogicalID    string // stable identity across the node's supersede chain
	Type         EntryType
	Title        string
	Body         string
	Status       Status
	Version      int
	IsCurrent    bool
	SupersededBy string // node id pointing forward; "" when IsCurrent
	OccurredAt   time.Time
	IngestedAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	// Confidence in [0,1]: 1.0 for human-asserted (manual) edges; the LLM
	// relate step (Slice F) writes its own score. Consumed by the future
	// confidence UI (dashed/solid at the 0.7 threshold); not yet rendered.
	Confidence float64
	OccurredAt time.Time
	IngestedAt time.Time
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
