package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Leon180/workingbad/internal/adapters/ai/mock"
	"github.com/Leon180/workingbad/internal/domain"
)

// --- UpsertRaw ---

func TestUpsertRaw_NewCommitCreatesChange(t *testing.T) {
	s := newService(t)
	ch, err := s.UpsertRaw(ctx(t), commit("sha-1", "repo-1"), "patch-1")
	if err != nil {
		t.Fatalf("UpsertRaw: %v", err)
	}
	if ch.ChangeID == "" {
		t.Error("change_id not assigned")
	}
	if ch.PatchID != "patch-1" {
		t.Errorf("patch_id = %q, want patch-1", ch.PatchID)
	}

	// Confirm both rows exist.
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM raw_commits WHERE sha = ?`, "sha-1").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("raw_commits sha-1 count = %d, want 1", rows)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM raw_changes WHERE change_id = ?`, ch.ChangeID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("raw_changes count = %d, want 1", rows)
	}
}

func TestUpsertRaw_IdempotentOnSHA(t *testing.T) {
	s := newService(t)
	first, _ := s.UpsertRaw(ctx(t), commit("sha-1", "repo-1"), "patch-1")
	second, err := s.UpsertRaw(ctx(t), commit("sha-1", "repo-1"), "patch-1")
	if err != nil {
		t.Fatalf("second UpsertRaw: %v", err)
	}
	if second.ChangeID != first.ChangeID {
		t.Errorf("idempotency broken: first=%q second=%q", first.ChangeID, second.ChangeID)
	}
	var rawCount int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM raw_commits WHERE sha = ?`, "sha-1").Scan(&rawCount)
	if rawCount != 1 {
		t.Errorf("raw_commits count = %d, want 1", rawCount)
	}
}

func TestUpsertRaw_RewriteLinksToExistingChange(t *testing.T) {
	s := newService(t)
	first, _ := s.UpsertRaw(ctx(t), commit("sha-orig", "repo-1"), "patch-1")
	second, err := s.UpsertRaw(ctx(t), commit("sha-amend", "repo-1"), "patch-1")
	if err != nil {
		t.Fatalf("rewrite UpsertRaw: %v", err)
	}
	if second.ChangeID != first.ChangeID {
		t.Errorf("rewrite did NOT reuse change_id: first=%q second=%q", first.ChangeID, second.ChangeID)
	}

	// Old sha is now superseded; new sha is current; both point at the same change.
	var origCurrent, origSupBy, amendCurrent string
	if err := s.db.QueryRow(`SELECT is_current, COALESCE(superseded_by, '') FROM raw_commits WHERE sha = ?`, "sha-orig").
		Scan(&origCurrent, &origSupBy); err != nil {
		t.Fatal(err)
	}
	if origCurrent != "0" {
		t.Errorf("sha-orig is_current = %q, want 0", origCurrent)
	}
	if origSupBy != "sha-amend" {
		t.Errorf("sha-orig superseded_by = %q, want sha-amend", origSupBy)
	}
	if err := s.db.QueryRow(`SELECT is_current FROM raw_commits WHERE sha = ?`, "sha-amend").Scan(&amendCurrent); err != nil {
		t.Fatal(err)
	}
	if amendCurrent != "1" {
		t.Errorf("sha-amend is_current = %q, want 1", amendCurrent)
	}
}

func TestUpsertRaw_EmptyPatchIDCreatesIndependentChange(t *testing.T) {
	s := newService(t)
	// Two merge commits both with empty patch_id → two distinct change_ids.
	first, _ := s.UpsertRaw(ctx(t), commit("sha-merge1", "repo-1"), "")
	second, _ := s.UpsertRaw(ctx(t), commit("sha-merge2", "repo-1"), "")
	if first.ChangeID == second.ChangeID {
		t.Errorf("empty patch_id collapsed to single change_id: %q", first.ChangeID)
	}
}

func TestUpsertRaw_SamePatchDifferentRepoIsolated(t *testing.T) {
	s := newService(t)
	repoA, _ := s.UpsertRaw(ctx(t), commit("sha-a", "repo-A"), "patch-1")
	repoB, _ := s.UpsertRaw(ctx(t), commit("sha-b", "repo-B"), "patch-1")
	if repoA.ChangeID == repoB.ChangeID {
		t.Errorf("cross-repo patch_id leaked: same change_id %q", repoA.ChangeID)
	}
}

func TestUpsertRaw_MissingRequiredFields(t *testing.T) {
	s := newService(t)
	if _, err := s.UpsertRaw(ctx(t), domain.RawCommit{RepoID: "r"}, ""); err == nil {
		t.Error("expected error for missing sha")
	}
	if _, err := s.UpsertRaw(ctx(t), domain.RawCommit{SHA: "s"}, ""); err == nil {
		t.Error("expected error for missing repo_id")
	}
}

// --- UpsertSegment + LinkSegmentRaw ---

func TestUpsertSegment_FreshInsert(t *testing.T) {
	s := newService(t)
	seg, err := s.UpsertSegment(ctx(t), segment("repo-1", "ref-1", "anchor-1"))
	if err != nil {
		t.Fatalf("UpsertSegment: %v", err)
	}
	if seg.ID == "" {
		t.Error("ID not assigned")
	}
	if seg.SummaryState != domain.SummaryStatePending {
		t.Errorf("default summary_state = %q, want pending", seg.SummaryState)
	}
}

func TestUpsertSegment_IdempotentUpdates(t *testing.T) {
	s := newService(t)
	first, _ := s.UpsertSegment(ctx(t), segment("repo-1", "ref-1", "anchor-1"))

	// Same idempotency key, different anchor → update path.
	second, err := s.UpsertSegment(ctx(t), domain.Segment{
		RepoID:        "repo-1",
		Source:        domain.SourceGit,
		SourceRef:     "ref-1",
		AnchorPatchID: "anchor-2",
		SummaryState:  domain.SummaryStateStale,
	})
	if err != nil {
		t.Fatalf("second UpsertSegment: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("idempotency broken: first ID %q != second ID %q", first.ID, second.ID)
	}

	var anchor, state string
	if err := s.db.QueryRow(`SELECT anchor_patch_id, summary_state FROM segments WHERE id = ?`, first.ID).
		Scan(&anchor, &state); err != nil {
		t.Fatal(err)
	}
	if anchor != "anchor-2" {
		t.Errorf("anchor not updated: %q", anchor)
	}
	if state != "stale" {
		t.Errorf("state not updated: %q", state)
	}
}

func TestLinkSegmentRaw_Idempotent(t *testing.T) {
	s := newService(t)
	seg, _ := s.UpsertSegment(ctx(t), segment("repo-1", "ref-1", "anchor-1"))
	ch, _ := s.UpsertRaw(ctx(t), commit("sha-1", "repo-1"), "patch-1")

	if err := s.LinkSegmentRaw(ctx(t), seg.ID, ch.ChangeID); err != nil {
		t.Fatalf("link 1: %v", err)
	}
	if err := s.LinkSegmentRaw(ctx(t), seg.ID, ch.ChangeID); err != nil {
		t.Fatalf("link 2 (idempotent): %v", err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM segment_raw WHERE segment_id = ?`, seg.ID).Scan(&n)
	if n != 1 {
		t.Errorf("segment_raw count = %d, want 1", n)
	}
}

// --- BatchMaterialize — the core pipeline ---

func TestBatchMaterialize_PendingToMaterialized(t *testing.T) {
	s := newService(t)
	provider := mock.New()

	seg := setupSegmentWithChanges(t, s, "repo-1", "ref-1", "patch-1", "sha-1")

	res, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider)
	if err != nil {
		t.Fatalf("BatchMaterialize: %v", err)
	}
	if res.Materialized != 1 {
		t.Errorf("Materialized = %d, want 1", res.Materialized)
	}
	if res.Failed != 0 {
		t.Errorf("Failed = %d, want 0; errors: %v", res.Failed, res.Errors)
	}

	// Segment state advanced.
	var state string
	_ = s.db.QueryRow(`SELECT summary_state FROM segments WHERE id = ?`, seg.ID).Scan(&state)
	if state != "materialized" {
		t.Errorf("segment state = %q, want materialized", state)
	}

	// Activity entry exists and is searchable in FTS.
	if !ftsHit(t, s.db, "mock") {
		t.Error("FTS missing the materialized 'mock' activity")
	}
	var entryCount int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM entries WHERE type='activity' AND source='git' AND source_ref=? AND is_current=1`,
		"ref-1",
	).Scan(&entryCount)
	if entryCount != 1 {
		t.Errorf("activity entry count = %d, want 1", entryCount)
	}
}

func TestBatchMaterialize_OneSummarizeCallPerSegment(t *testing.T) {
	s := newService(t)
	provider := mock.New()
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-1", "patch-1", "sha-1")
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-2", "patch-2", "sha-2")
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-3", "patch-3", "sha-3")

	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("BatchMaterialize: %v", err)
	}
	summarize, _, _, _ := provider.Counts()
	if summarize != 3 {
		t.Errorf("Summarize calls = %d, want 3 (1 per segment)", summarize)
	}
}

func TestBatchMaterialize_PerSegmentIsolation(t *testing.T) {
	s := newService(t)
	// Mock fails after 2nd Summarize succeeds — 3rd segment will fail.
	provider := mock.New().FailAfterN(2)

	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-1", "patch-1", "sha-1")
	_ = setupSegmentWithChanges(t, s, "repo-1", "ref-2", "patch-2", "sha-2")
	seg3 := setupSegmentWithChanges(t, s, "repo-1", "ref-3", "patch-3", "sha-3")

	res, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider)
	if err != nil {
		t.Fatalf("BatchMaterialize: %v", err)
	}
	if res.Materialized != 2 || res.Failed != 1 {
		t.Errorf("got materialized=%d failed=%d; want 2 / 1", res.Materialized, res.Failed)
	}

	// The failed segment must still be pending (tx rolled back).
	var state string
	_ = s.db.QueryRow(`SELECT summary_state FROM segments WHERE id = ?`, seg3.ID).Scan(&state)
	if state != "pending" {
		t.Errorf("failed segment state = %q, want pending (per-segment rollback)", state)
	}
}

func TestBatchMaterialize_ReMaterializeSupersedes(t *testing.T) {
	s := newService(t)
	provider := mock.New()

	seg := setupSegmentWithChanges(t, s, "repo-1", "ref-1", "patch-1", "sha-1")

	// First materialize.
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("first BatchMaterialize: %v", err)
	}
	var firstEntryID, firstLogicalID string
	_ = s.db.QueryRow(
		`SELECT id, logical_id FROM entries WHERE source_ref=? AND is_current=1`, "ref-1",
	).Scan(&firstEntryID, &firstLogicalID)

	// Mark segment stale + flip provider to inject a new title; re-materialize.
	if _, err := s.db.Exec(
		`UPDATE segments SET summary_state='stale' WHERE id = ?`, seg.ID,
	); err != nil {
		t.Fatal(err)
	}
	provider.WithSummarizeFunc(func(_ context.Context, _ []domain.RawChange) (string, string, error) {
		return "Refreshed conclusion", "different body", nil
	})

	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("re-BatchMaterialize: %v", err)
	}

	var newEntryID, newLogicalID string
	_ = s.db.QueryRow(
		`SELECT id, logical_id FROM entries WHERE source_ref=? AND is_current=1`, "ref-1",
	).Scan(&newEntryID, &newLogicalID)
	if newEntryID == firstEntryID {
		t.Error("re-materialize produced same entry id; supersede did not happen")
	}
	if newLogicalID != firstLogicalID {
		t.Errorf("LogicalID drift across supersede: %q → %q", firstLogicalID, newLogicalID)
	}
	if !ftsHit(t, s.db, "refreshed") {
		t.Error("FTS missing new 'refreshed' content after re-materialize")
	}
	if ftsHit(t, s.db, "mock") {
		t.Error("FTS still contains old 'mock' content — supersede did not drop it")
	}
}

// TestBatchMaterialize_ReMaterialize_PreservesAttachedGoal is the P0
// regression for the bug found in the post-Slice-A multi-agent review:
// before the rePoint-outgoing fix, after re-Summarize, the new activity was
// not returned by GetGoalActivities because the part_of edge still pointed
// to the now-superseded old activity. This test fails on the pre-fix code.
func TestBatchMaterialize_ReMaterialize_PreservesAttachedGoal(t *testing.T) {
	s := newService(t)
	provider := mock.New()

	// Materialise an activity for a segment.
	seg := setupSegmentWithChanges(t, s, "repo-1", "ref-attach", "patch-attach", "sha-attach")
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("first BatchMaterialize: %v", err)
	}
	var activityIDBefore string
	_ = s.db.QueryRow(
		`SELECT id FROM entries WHERE type='activity' AND source_ref=? AND is_current=1`, "ref-attach",
	).Scan(&activityIDBefore)

	// Create a goal and attach the activity to it.
	goal, err := s.InsertEntry(ctx(t), domain.Entry{
		Type: domain.EntryTypeGoal, Origin: domain.OriginLocal,
		Source: domain.SourceManual, SourceRef: "goal-1",
		Title: "Phase 1 Slice A complete", Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachToGoal(ctx(t), activityIDBefore, goal.ID); err != nil {
		t.Fatalf("AttachToGoal: %v", err)
	}

	// Mark segment stale, swap Summarize so the new activity is observably
	// different content, re-materialize.
	if _, err := s.db.Exec(`UPDATE segments SET summary_state='stale' WHERE id = ?`, seg.ID); err != nil {
		t.Fatal(err)
	}
	provider.WithSummarizeFunc(func(_ context.Context, _ []domain.RawChange) (string, string, error) {
		return "Refreshed activity", "new body", nil
	})
	if _, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider); err != nil {
		t.Fatalf("re-BatchMaterialize: %v", err)
	}

	// The goal's activity list MUST still find a live activity. Before the
	// fix this returned an empty list because the part_of edge still pointed
	// to the now-superseded original activity id.
	got, err := s.GetGoalActivities(ctx(t), goal.ID)
	if err != nil {
		t.Fatalf("GetGoalActivities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after re-materialize: got %d attached activities, want 1 (re-point lost the edge)", len(got))
	}
	if got[0].ID == activityIDBefore {
		t.Errorf("got returned old activity %q; expected the newly-materialised replacement", activityIDBefore)
	}
	if got[0].Title != "Refreshed activity" {
		t.Errorf("got title %q, want 'Refreshed activity' — wrong activity returned", got[0].Title)
	}
}

func TestBatchMaterialize_NoChangesIsError(t *testing.T) {
	s := newService(t)
	provider := mock.New()
	// Segment with no linked raw_changes.
	if _, err := s.UpsertSegment(ctx(t), segment("repo-1", "ref-empty", "anchor-x")); err != nil {
		t.Fatal(err)
	}
	res, err := s.BatchMaterialize(ctx(t), MaterializeScope{}, provider)
	if err != nil {
		t.Fatalf("BatchMaterialize: %v", err)
	}
	if res.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (segment with no raw)", res.Failed)
	}
}

// --- fixture helpers (kept local to this file) ---

func commit(sha, repo string) domain.RawCommit {
	return domain.RawCommit{
		SHA:        sha,
		RepoID:     repo,
		Author:     "alice",
		AuthorTime: time.Now().UTC(),
		Committer:  "alice",
		CommitTime: time.Now().UTC(),
		Message:    "test commit " + sha,
		BranchHint: "main",
	}
}

func segment(repo, sourceRef, anchor string) domain.Segment {
	return domain.Segment{
		RepoID:        repo,
		Source:        domain.SourceGit,
		SourceRef:     sourceRef,
		AnchorPatchID: anchor,
	}
}

// setupSegmentWithChanges creates one segment linked to one raw_commit. The
// returned segment is committed and ready to be materialized.
func setupSegmentWithChanges(t *testing.T, s *Service, repo, sourceRef, patchID, sha string) domain.Segment {
	t.Helper()
	seg, err := s.UpsertSegment(ctx(t), segment(repo, sourceRef, patchID))
	if err != nil {
		t.Fatalf("UpsertSegment: %v", err)
	}
	ch, err := s.UpsertRaw(ctx(t), commit(sha, repo), patchID)
	if err != nil {
		t.Fatalf("UpsertRaw: %v", err)
	}
	if err := s.LinkSegmentRaw(ctx(t), seg.ID, ch.ChangeID); err != nil {
		t.Fatalf("LinkSegmentRaw: %v", err)
	}
	return seg
}
