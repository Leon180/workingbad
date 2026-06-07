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
type Entry struct {
	ID           string
	LogicalID    string
	Type         EntryType
	Title        string
	Body         string
	Source       Source
	SourceRef    string
	Origin       Origin
	RepoID       string
	Author       string
	Status       Status
	IsCurrent    bool
	SupersededBy string
	Metadata     string // JSON
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Edge is a typed relationship between two entries. Append-only + supersede;
// partial-unique `(from, to, relation) WHERE is_current = 1` keeps lookups
// deterministic.
type Edge struct {
	ID           string
	FromID       string
	ToID         string
	Relation     Relation
	IsCurrent    bool
	SupersededBy string
	Metadata     string
	CreatedAt    time.Time
}

// Segment is the work-session lifecycle carrier. Idempotency key authority
// for the git ingest path (truth-source-schema invariant 2).
type Segment struct {
	ID            string
	RepoID        string
	Source        Source
	SourceRef     string // encode(repo_id, branch, anchor_patch_id)
	SummaryState  SummaryState
	AnchorPatchID string
	Metadata      string
	CreatedAt     time.Time
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
	CreatedAt    time.Time
}

// RawChange is the rewrite-transparent logical change layer. Surrogate
// `ChangeID` (uuid v7); `PatchID` is the content fingerprint and can be empty
// (merge commits / patch-id unavailable).
type RawChange struct {
	ChangeID  string
	RepoID    string
	PatchID   string
	CreatedAt time.Time
}
